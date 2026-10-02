package planadjust

import (
	"database/sql"
	"errors"
)

// Planner 单个门店的调整与分摊引擎。同一门店同一时间只允许一个操作（上层保证），
// 因此内部不加锁。
type Planner struct {
	db *sql.DB

	skuKeys []string
	skuIdx  map[string]int
	catKeys []string
	catIdx  map[string]int
	skuCat  []int   // sku -> category
	catSKUs [][]int // category -> skus

	forecast []int64 // len = nSKU*91，行优先：sku*91+day
	plan     []int64

	entries []*adjEntry // 全部调整，按提交先后（ID 即顺序）

	marks     []uint64 // 格子位图暂存
	tmpCells  []int
	tmpCells2 []int
	tmpW      []int64
	tmpAlloc  []int64
}

func newPlanner(db *sql.DB) *Planner {
	return &Planner{
		db:     db,
		skuIdx: make(map[string]int),
		catIdx: make(map[string]int),
	}
}

func (p *Planner) initScratch() {
	n := len(p.skuKeys) * Days
	p.marks = make([]uint64, (n+63)/64)
}

// Create 新建一个门店的计划库；path 指向已初始化的数据库时返回 ErrExists。
func Create(path string, cfg Config) (*Planner, error) {
	if len(cfg.SKUs) == 0 {
		return nil, errors.New("planadjust: empty SKU list")
	}
	db, err := openDB(path)
	if err != nil {
		return nil, err
	}
	p := newPlanner(db)
	if err := p.initSchema(); err != nil {
		db.Close()
		return nil, err
	}
	var existing string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='format_version'`).Scan(&existing); err == nil {
		db.Close()
		return nil, ErrExists
	}
	for _, def := range cfg.SKUs {
		if def.SKU == "" || def.Category == "" {
			db.Close()
			return nil, errors.New("planadjust: empty SKU or category key")
		}
		if _, dup := p.skuIdx[def.SKU]; dup {
			db.Close()
			return nil, errors.New("planadjust: duplicate SKU " + def.SKU)
		}
		c, ok := p.catIdx[def.Category]
		if !ok {
			c = len(p.catKeys)
			p.catIdx[def.Category] = c
			p.catKeys = append(p.catKeys, def.Category)
		}
		p.skuIdx[def.SKU] = len(p.skuKeys)
		p.skuKeys = append(p.skuKeys, def.SKU)
		p.skuCat = append(p.skuCat, c)
	}
	p.catSKUs = make([][]int, len(p.catKeys))
	for k, c := range p.skuCat {
		p.catSKUs[c] = append(p.catSKUs[c], k)
	}
	n := len(p.skuKeys) * Days
	p.forecast = make([]int64, n)
	p.plan = make([]int64, n)
	p.initScratch()
	if err := p.saveCatalog(cfg.StoreID); err != nil {
		db.Close()
		return nil, err
	}
	if err := p.persist(); err != nil {
		db.Close()
		return nil, err
	}
	return p, nil
}

// Open 打开已有的门店计划库，恢复计划值、全部调整及其状态。
func Open(path string) (*Planner, error) {
	db, err := openDB(path)
	if err != nil {
		return nil, err
	}
	p := newPlanner(db)
	if err := p.initSchema(); err != nil {
		db.Close()
		return nil, err
	}
	if err := p.load(); err != nil {
		db.Close()
		return nil, err
	}
	p.initScratch()
	return p, nil
}

// Close 关闭底层数据库。
func (p *Planner) Close() error {
	return p.db.Close()
}

// activeEntries 当前生效的调整，按提交先后排序。
func (p *Planner) activeEntries() []*adjEntry {
	var out []*adjEntry
	for _, e := range p.entries {
		if e.adj.Status == StatusActive {
			out = append(out, e)
		}
	}
	return out
}

// ReplaceForecast 整版更换统计预测。values 必须覆盖每个 SKU 的 91 天，全部非负。
// 更换后按规则重算；重算中无法满足的旧调整被标为冲突。
func (p *Planner) ReplaceForecast(values map[string][Days]int64) error {
	if len(values) != len(p.skuKeys) {
		return errors.New("planadjust: forecast must cover every SKU")
	}
	next := make([]int64, len(p.forecast))
	for sku, days := range values {
		k, ok := p.skuIdx[sku]
		if !ok {
			return errors.New("planadjust: unknown SKU " + sku)
		}
		for d, v := range days {
			if v < 0 {
				return errors.New("planadjust: negative forecast value")
			}
			next[k*Days+d] = v
		}
	}
	p.forecast = next
	plan, conflicts := p.replay(p.activeEntries())
	for _, e := range conflicts {
		e.adj.Status = StatusConflict
	}
	p.plan = plan
	return p.persist()
}

// Submit 提交一条调整。无法满足时返回 ErrInfeasible，计划值与所有调整状态不变。
// 成功时返回新调整 ID。
func (p *Planner) Submit(scope Scope, target int64) (int64, error) {
	if target < 0 {
		return 0, ErrInvalidTarget
	}
	sc, err := p.resolve(scope)
	if err != nil {
		return 0, err
	}
	entry := &adjEntry{
		adj: Adjustment{ID: p.nextID(), Scope: scope, Target: target, Status: StatusActive},
		sc:  sc,
	}

	// 相交但互不包含的旧调整作废；范围完全相同的被取代。
	var voids, replaces []*adjEntry
	var kept []*adjEntry
	for _, e := range p.activeEntries() {
		switch {
		case e.sc.equal(sc):
			replaces = append(replaces, e)
		case e.sc.intersects(sc, p.skuCat) &&
			!e.sc.contains(sc, p.skuCat) && !sc.contains(e.sc, p.skuCat):
			voids = append(voids, e)
		default:
			kept = append(kept, e)
		}
	}

	if len(voids) == 0 && len(replaces) == 0 {
		// 快速路径：生效集只增不减，直接在当前计划上增量应用。
		if !p.applyOne(p.plan, entry, kept) {
			return 0, ErrInfeasible
		}
		p.entries = append(p.entries, entry)
		if err := p.persist(); err != nil {
			return 0, err
		}
		return entry.adj.ID, nil
	}

	// 慢速路径：先生成候选生效集试算，可行才提交。
	candidate := append(append([]*adjEntry{}, kept...), entry)
	plan, conflicts := p.replay(candidate)
	for _, e := range conflicts {
		if e == entry {
			return 0, ErrInfeasible // 新调整无法满足：一切不变
		}
	}
	for _, e := range voids {
		e.adj.Status = StatusVoided
	}
	for _, e := range replaces {
		e.adj.Status = StatusReplaced
	}
	for _, e := range conflicts {
		e.adj.Status = StatusConflict
	}
	p.entries = append(p.entries, entry)
	p.plan = plan
	if err := p.persist(); err != nil {
		return 0, err
	}
	return entry.adj.ID, nil
}

// Undo 撤销一条生效中的调整，触发重算。
func (p *Planner) Undo(id int64) error {
	var target *adjEntry
	for _, e := range p.entries {
		if e.adj.ID == id {
			target = e
			break
		}
	}
	if target == nil {
		return ErrNotFound
	}
	if target.adj.Status != StatusActive {
		return ErrNotActive
	}
	target.adj.Status = StatusUndone
	plan, conflicts := p.replay(p.activeEntries())
	for _, e := range conflicts {
		e.adj.Status = StatusConflict
	}
	p.plan = plan
	return p.persist()
}

// sumScope 合计 grid 中范围内的格子。
func (p *Planner) sumScope(grid []int64, sc iscope) int64 {
	d0, d1 := sc.days()
	var sum int64
	addRow := func(sku int) {
		row := grid[sku*Days+d0 : sku*Days+d1]
		for _, v := range row {
			sum += v
		}
	}
	switch sc.prod.kind {
	case ProductAll:
		for k := range p.skuKeys {
			addRow(k)
		}
	case ProductCategory:
		for _, k := range p.catSKUs[sc.prod.cat] {
			addRow(k)
		}
	default:
		addRow(sc.prod.sku)
	}
	return sum
}

// PlanSum 查询任意范围调整后合计。
func (p *Planner) PlanSum(scope Scope) (int64, error) {
	sc, err := p.resolve(scope)
	if err != nil {
		return 0, err
	}
	return p.sumScope(p.plan, sc), nil
}

// ForecastSum 查询任意范围统计预测合计。
func (p *Planner) ForecastSum(scope Scope) (int64, error) {
	sc, err := p.resolve(scope)
	if err != nil {
		return 0, err
	}
	return p.sumScope(p.forecast, sc), nil
}

// PlanValue 查询某 SKU 某天的调整后值。
func (p *Planner) PlanValue(sku string, day int) (int64, error) {
	k, ok := p.skuIdx[sku]
	if !ok || day < 1 || day > Days {
		return 0, ErrInvalidScope
	}
	return p.plan[k*Days+day-1], nil
}

// ForecastValue 查询某 SKU 某天的统计预测值。
func (p *Planner) ForecastValue(sku string, day int) (int64, error) {
	k, ok := p.skuIdx[sku]
	if !ok || day < 1 || day > Days {
		return 0, ErrInvalidScope
	}
	return p.forecast[k*Days+day-1], nil
}

// Adjustments 返回全部调整及其当前状态，按提交先后排序。
func (p *Planner) Adjustments() []Adjustment {
	out := make([]Adjustment, len(p.entries))
	for i, e := range p.entries {
		out[i] = e.adj
	}
	return out
}
