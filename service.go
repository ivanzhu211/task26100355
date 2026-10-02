package planadjust

import (
	"fmt"
	"time"
)

// Service 是门店销量预测人工调整模块的入口，一个实例对应一个门店。
// 上层需保证同一门店同一时间只有一个操作（本类型不做并发控制）。
type Service struct {
	st          *store
	l           *layout
	eng         *engine
	acts        []*Adjustment // 全部调整（含非生效），按 Seq 升序
	initialized bool
	periodStart string
}

// Open 打开门店数据库（path 为 SQLite 文件路径），加载并恢复状态。
// 若数据库为空，返回的 Service 尚未初始化，需先调用 Init。
func Open(path string) (*Service, error) {
	st, err := openStore(path)
	if err != nil {
		return nil, err
	}
	s := &Service{st: st}
	periodStart, ok, err := st.loadMeta()
	if err != nil {
		st.close()
		return nil, err
	}
	if !ok {
		return s, nil // 未初始化
	}
	skus, err := st.loadSKUs()
	if err != nil {
		st.close()
		return nil, err
	}
	forecast, err := st.loadForecast(newLayout(skus))
	if err != nil {
		st.close()
		return nil, err
	}
	acts, err := st.loadAdjustments()
	if err != nil {
		st.close()
		return nil, err
	}
	s.initialized = true
	s.periodStart = periodStart
	s.l = newLayout(skus)
	s.acts = acts
	s.eng = newEngine(s.l, forecast)
	for _, a := range acts {
		if a.Status == StatusActive {
			s.eng.acts = append(s.eng.acts, a)
		}
	}
	// 重放恢复计划值。确定性重放，正常不会新产生冲突；若有则落盘。
	if conflicts := s.eng.replay(); len(conflicts) > 0 {
		for _, id := range conflicts {
			s.markStatus(id, StatusConflict)
		}
	}
	return s, nil
}

// Init 初始化门店：计划期起始日（须为周一）与 SKU 清单。仅可调用一次。
func (s *Service) Init(periodStart time.Time, skus []SKUInfo) error {
	if s.initialized {
		return fmt.Errorf("planadjust: already initialized: %w", ErrInvalid)
	}
	if periodStart.Weekday() != time.Monday {
		return fmt.Errorf("planadjust: period start must be a Monday: %w", ErrInvalid)
	}
	if len(skus) == 0 {
		return fmt.Errorf("planadjust: empty SKU list: %w", ErrInvalid)
	}
	seen := make(map[string]bool, len(skus))
	for _, info := range skus {
		if info.SKU == "" || info.Category == "" {
			return fmt.Errorf("planadjust: blank SKU or category: %w", ErrInvalid)
		}
		if seen[info.SKU] {
			return fmt.Errorf("planadjust: duplicate SKU %q: %w", info.SKU, ErrInvalid)
		}
		seen[info.SKU] = true
	}
	start := periodStart.Format("2006-01-02")
	if err := s.st.initStore(start, skus); err != nil {
		return err
	}
	s.initialized = true
	s.periodStart = start
	s.l = newLayout(skus)
	s.eng = newEngine(s.l, nil)
	return nil
}

// ReplaceForecast 整版更换统计预测。f 的键为 SKU，值为 91 个非负整数。
// 更换后重放所有生效调整；重算中无法满足的旧调整被标为冲突。
func (s *Service) ReplaceForecast(f map[string][]int64) error {
	if !s.initialized {
		return ErrNotInitialized
	}
	g := newGrid(s.l)
	for sku, vals := range f {
		row, ok := s.l.skuIndex[sku]
		if !ok {
			return fmt.Errorf("planadjust: unknown SKU %q: %w", sku, ErrInvalid)
		}
		if len(vals) != Days {
			return fmt.Errorf("planadjust: forecast for %q must have %d values: %w", sku, Days, ErrInvalid)
		}
		for d, v := range vals {
			if v < 0 {
				return fmt.Errorf("planadjust: negative forecast value: %w", ErrInvalid)
			}
			g.setCell(row*Days+d, v)
		}
	}
	conflicts := s.eng.setForecast(g)
	if err := s.st.saveForecast(g, s.l); err != nil {
		return err
	}
	for _, id := range conflicts {
		if err := s.markStatus(id, StatusConflict); err != nil {
			return err
		}
	}
	return nil
}

// Submit 提交一条人工调整。不可满足时返回 ErrInfeasible，且计划值与
// 所有调整的状态均不发生任何变化。
func (s *Service) Submit(scope Scope, target int64) (*Adjustment, error) {
	if !s.initialized {
		return nil, ErrNotInitialized
	}
	if target < 0 {
		return nil, fmt.Errorf("planadjust: negative target: %w", ErrInvalid)
	}
	if !s.l.valid(scope) {
		return nil, fmt.Errorf("planadjust: invalid scope: %w", ErrInvalid)
	}
	adj := &Adjustment{
		Seq:       s.nextSeq(),
		Scope:     scope,
		Target:    target,
		Status:    StatusActive,
		CreatedAt: time.Now().Unix(),
	}
	voided, superseded, conflicts, err := s.eng.submit(adj)
	if err != nil {
		return nil, err
	}
	if err := s.st.insertAdjustment(adj); err != nil {
		return nil, err
	}
	s.acts = append(s.acts, adj)
	for _, id := range voided {
		if err := s.markStatus(id, StatusVoided); err != nil {
			return nil, err
		}
	}
	for _, id := range superseded {
		if err := s.markStatus(id, StatusSuperseded); err != nil {
			return nil, err
		}
	}
	for _, id := range conflicts {
		if err := s.markStatus(id, StatusConflict); err != nil {
			return nil, err
		}
	}
	return adj, nil
}

// Withdraw 撤销一条生效中的调整，触发重算。
func (s *Service) Withdraw(id int64) error {
	if !s.initialized {
		return ErrNotInitialized
	}
	adj := s.find(id)
	if adj == nil {
		return ErrNotFound
	}
	if adj.Status != StatusActive {
		return fmt.Errorf("planadjust: adjustment %d is %s, not active: %w", id, adj.Status, ErrInvalid)
	}
	conflicts, err := s.eng.withdraw(id)
	if err != nil {
		return err
	}
	if err := s.markStatus(id, StatusWithdrawn); err != nil {
		return err
	}
	for _, cid := range conflicts {
		if err := s.markStatus(cid, StatusConflict); err != nil {
			return err
		}
	}
	return nil
}

// SumForecast 返回任意范围的统计预测合计。
func (s *Service) SumForecast(scope Scope) (int64, error) {
	if !s.initialized {
		return 0, ErrNotInitialized
	}
	if !s.l.valid(scope) {
		return 0, fmt.Errorf("planadjust: invalid scope: %w", ErrInvalid)
	}
	return s.eng.forecast.sum(s.l, scope), nil
}

// SumPlan 返回任意范围的调整后合计。
func (s *Service) SumPlan(scope Scope) (int64, error) {
	if !s.initialized {
		return 0, ErrNotInitialized
	}
	if !s.l.valid(scope) {
		return 0, fmt.Errorf("planadjust: invalid scope: %w", ErrInvalid)
	}
	return s.eng.plan.sum(s.l, scope), nil
}

// PlanCell 返回某 SKU 某天的调整后值（day 为 1..91）。
func (s *Service) PlanCell(sku string, day int) (int64, error) {
	return s.cell(s.eng == nil, sku, day, true)
}

// ForecastCell 返回某 SKU 某天的统计预测值（day 为 1..91）。
func (s *Service) ForecastCell(sku string, day int) (int64, error) {
	return s.cell(s.eng == nil, sku, day, false)
}

func (s *Service) cell(noEng bool, sku string, day int, plan bool) (int64, error) {
	if !s.initialized || noEng {
		return 0, ErrNotInitialized
	}
	row, ok := s.l.skuIndex[sku]
	if !ok {
		return 0, fmt.Errorf("planadjust: unknown SKU %q: %w", sku, ErrInvalid)
	}
	if day < 1 || day > Days {
		return 0, fmt.Errorf("planadjust: day out of range: %w", ErrInvalid)
	}
	if plan {
		return s.eng.plan.get(row, day-1), nil
	}
	return s.eng.forecast.get(row, day-1), nil
}

// Adjustments 返回全部调整及其状态（按提交顺序）。
func (s *Service) Adjustments() []*Adjustment {
	out := make([]*Adjustment, len(s.acts))
	copy(out, s.acts)
	return out
}

// Close 关闭服务并落盘。
func (s *Service) Close() error {
	return s.st.close()
}

// nextSeq 返回下一条调整的提交序号。
func (s *Service) nextSeq() int64 {
	var max int64
	for _, a := range s.acts {
		if a.Seq > max {
			max = a.Seq
		}
	}
	return max + 1
}

// find 按 ID 查找调整。
func (s *Service) find(id int64) *Adjustment {
	for _, a := range s.acts {
		if a.ID == id {
			return a
		}
	}
	return nil
}

// markStatus 更新内存与数据库中一条调整的状态。
func (s *Service) markStatus(id int64, st Status) error {
	if a := s.find(id); a != nil {
		a.Status = st
	}
	return s.st.updateStatus(id, st)
}
