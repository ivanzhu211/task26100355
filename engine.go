package planadjust

import "math/bits"

// engine 在给定商品结构上维护统计预测与计划值，
// 并保证计划值恒等于从统计预测出发按提交顺序重放所有生效调整的结果。
//
// 关键不变量：生效调整的范围两两嵌套或互不相交（层叠族），
// 因为相交的调整在提交时就已被作废。因此：
//   - 一条调整被包含的更早生效调整（子调整）两两不相交，其格子合计恒等于各自目标值；
//   - 包含一条调整的更早生效调整全线性嵌套，存在唯一的"最近包含者"。
type engine struct {
	l        *layout
	forecast *grid
	plan     *grid
	acts     []*Adjustment // 生效中的调整，按 Seq 升序

	stamp  []uint32 // 格子标记（分阶段排除用）
	epoch  uint32
	cells  []int // 暂存：可动格子
	cells2 []int // 暂存：消化格子
	kids   []*Adjustment
	kids2  []*Adjustment
}

// newEngine 创建引擎。forecast 为 nil 时视为全零。
func newEngine(l *layout, forecast *grid) *engine {
	if forecast == nil {
		forecast = newGrid(l)
	}
	return &engine{
		l:        l,
		forecast: forecast,
		plan:     forecast.clone(),
		stamp:    make([]uint32, len(l.skus)*Days),
	}
}

// activeAdjustments 返回当前生效调整（按提交顺序）。
func (e *engine) activeAdjustments() []*Adjustment {
	return e.acts
}

// setForecast 整版更换统计预测并重算，返回被标为冲突的调整 ID。
func (e *engine) setForecast(f *grid) []int64 {
	e.forecast.loadFrom(f)
	return e.replay()
}

// withdraw 撤销一条生效调整并重算，返回被标为冲突的调整 ID。
func (e *engine) withdraw(id int64) ([]int64, error) {
	idx := -1
	for i, a := range e.acts {
		if a.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, ErrNotFound
	}
	e.acts = append(e.acts[:idx], e.acts[idx+1:]...)
	return e.replay(), nil
}

// submit 尝试提交一条新调整（adj.Seq 必须大于所有现有生效调整的 Seq）。
// 成功时返回作废/被取代/冲突的调整 ID；ErrInfeasible 表示被拒绝且无任何状态变化。
func (e *engine) submit(adj *Adjustment) (voided, superseded, conflicts []int64, err error) {
	var voidSet, supSet map[int64]bool
	for _, a := range e.acts {
		switch e.l.relate(a.Scope, adj.Scope) {
		case relCross:
			voided = append(voided, a.ID)
		case relEqual:
			superseded = append(superseded, a.ID)
		}
	}
	if len(voided) == 0 && len(superseded) == 0 {
		// 增量路径：当前计划值已是重放结果，直接在其副本上试应用。
		trial := e.plan.clone()
		if !e.applyOne(trial, e.acts, adj) {
			return nil, nil, nil, ErrInfeasible
		}
		e.plan.loadFrom(trial)
		e.acts = append(e.acts, adj)
		return nil, nil, nil, nil
	}
	// 重放路径：先作废/取代，再从统计预测重放其余生效调整，最后应用新调整。
	voidSet = make(map[int64]bool, len(voided))
	for _, id := range voided {
		voidSet[id] = true
	}
	supSet = make(map[int64]bool, len(superseded))
	for _, id := range superseded {
		supSet[id] = true
	}
	kept := make([]*Adjustment, 0, len(e.acts)+1)
	for _, a := range e.acts {
		if !voidSet[a.ID] && !supSet[a.ID] {
			kept = append(kept, a)
		}
	}
	trial := e.forecast.clone()
	applied := make([]*Adjustment, 0, len(kept)+1)
	var trialConflicts []int64
	for _, a := range kept {
		if e.applyOne(trial, applied, a) {
			applied = append(applied, a)
		} else {
			trialConflicts = append(trialConflicts, a.ID)
		}
	}
	if !e.applyOne(trial, applied, adj) {
		// 新调整无法满足：拒绝，所有状态不变。
		return nil, nil, nil, ErrInfeasible
	}
	applied = append(applied, adj)
	// 提交生效。
	e.plan.loadFrom(trial)
	e.acts = applied
	return voided, superseded, trialConflicts, nil
}

// replay 从统计预测出发按顺序应用所有生效调整，重算计划值。
// 应用失败的调整被标记为冲突并跳过，返回其 ID。
func (e *engine) replay() []int64 {
	e.plan.loadFrom(e.forecast)
	applied := make([]*Adjustment, 0, len(e.acts))
	var conflicts []int64
	for _, a := range e.acts {
		if e.applyOne(e.plan, applied, a) {
			applied = append(applied, a)
		} else {
			conflicts = append(conflicts, a.ID)
		}
	}
	e.acts = applied
	return conflicts
}

// maximalContained 返回 earlier 中严格包含于 s 的极大调整（两两不相交）。
func (e *engine) maximalContained(earlier []*Adjustment, s Scope, buf []*Adjustment) []*Adjustment {
	out := buf[:0]
	for _, a := range earlier {
		if e.l.relate(s, a.Scope) != relContains {
			continue
		}
		kept := out[:0]
		maximal := true
		for _, c := range out {
			switch e.l.relate(c.Scope, a.Scope) {
			case relContains: // c 已包含 a，a 不是极大
				maximal = false
				kept = append(kept, c)
			case relContainedBy: // a 包含 c，c 不再是极大
			default: // 不相交
				kept = append(kept, c)
			}
		}
		out = kept
		if maximal {
			out = append(out, a)
		}
	}
	return out
}

// stampScope 标记一个范围覆盖的全部格子。
func (e *engine) stampScope(s Scope) {
	rows, _ := e.l.skuRows(s.Product)
	days := daysOf(s.Time)
	if rows == nil {
		for _, d := range days {
			for r := 0; r < e.plan.rows; r++ {
				e.stamp[r*Days+d] = e.epoch
			}
		}
		return
	}
	for _, r := range rows {
		base := r * Days
		for _, d := range days {
			e.stamp[base+d] = e.epoch
		}
	}
}

// collectCells 收集 s 范围内未被标记的格子（行优先顺序）及其当前值合计。
func (e *engine) collectCells(plan *grid, s Scope, buf []int) ([]int, int64) {
	out := buf[:0]
	rows, _ := e.l.skuRows(s.Product)
	days := daysOf(s.Time)
	var sum int64
	if rows == nil {
		for r := 0; r < plan.rows; r++ {
			base := r * Days
			for _, d := range days {
				i := base + d
				if e.stamp[i] != e.epoch {
					out = append(out, i)
					sum += plan.values[i]
				}
			}
		}
		return out, sum
	}
	for _, r := range rows {
		base := r * Days
		for _, d := range days {
			i := base + d
			if e.stamp[i] != e.epoch {
				out = append(out, i)
				sum += plan.values[i]
			}
		}
	}
	return out, sum
}

// applyOne 在 plan 上应用一条调整，earlier 为本次重放中已应用的调整（按 Seq 升序）。
// 不可满足时返回 false 且 plan 不被修改。
func (e *engine) applyOne(plan *grid, earlier []*Adjustment, adj *Adjustment) bool {
	s := adj.Scope

	// 1. 被完全包含的更早生效调整：格子原样不动，合计恒等于各自目标值。
	kids := e.maximalContained(earlier, s, e.kids)
	var frozenSum int64
	for _, k := range kids {
		frozenSum += k.Target
	}
	residual := adj.Target - frozenSum
	if residual < 0 {
		return false // 目标小于原样不动部分的合计
	}

	// 2. 可动格子 = 范围内不在任何子调整里的格子。
	e.epoch++
	for _, k := range kids {
		e.stampScope(k.Scope)
	}
	movable, movSum := e.collectCells(plan, s, e.cells)
	if residual > 0 && len(movable) == 0 {
		return false // 有量要分摊，却没有能动的格子
	}

	// 3. 若存在包含新范围的更早生效调整，最近包含者消化增减量。
	delta := residual - movSum // 新范围合计的变化量
	var enc *Adjustment
	for _, a := range earlier {
		if e.l.relate(a.Scope, s) != relContains {
			continue
		}
		if enc == nil || e.l.relate(a.Scope, enc.Scope) == relContainedBy {
			enc = a
		}
	}
	var abs []int
	var absSum int64
	if enc != nil && delta != 0 {
		// 消化格子 = 最近包含者范围内、新范围之外、也不在其他更小生效调整里的格子。
		e.epoch++
		e.stampScope(s)
		bkids := e.maximalContained(earlier, enc.Scope, e.kids2)
		for _, k := range bkids {
			if e.l.relate(k.Scope, s) == relDisjoint {
				e.stampScope(k.Scope)
			}
		}
		abs, absSum = e.collectCells(plan, enc.Scope, e.cells2)
		if len(abs) == 0 {
			return false // 有量要消化，却没有能动的格子
		}
		if delta > 0 && absSum < delta {
			return false // 需要反向消化的格子不够减
		}
	}

	// 4. 执行：先分摊进新范围，再在包含者内反向消化。
	distribute(plan, movable, movSum, residual)
	if enc != nil && delta != 0 {
		if delta > 0 {
			reduce(plan, abs, absSum, delta)
		} else {
			boost(plan, abs, absSum, -delta)
		}
	}
	return true
}

// distribute 把 amount 精确分摊到格子上：按当前值比例（全为 0 时平均分），
// 每格为精确份额向下或向上取整，合计分毫不差。amount 必须非负。
func distribute(plan *grid, cells []int, curSum, amount int64) {
	n := len(cells)
	if n == 0 {
		return
	}
	if curSum <= 0 {
		base := amount / int64(n)
		rem := amount % int64(n)
		for i, c := range cells {
			v := base
			if int64(i) < rem {
				v++
			}
			plan.setCell(c, v)
		}
		return
	}
	var cum, prev int64
	for _, c := range cells {
		cum += plan.values[c]
		cur := mulDiv(cum, amount, curSum)
		plan.setCell(c, cur-prev)
		prev = cur
	}
}

// reduce 从格子上精确减去的合计 amount：按当前值比例，每格减少量为精确份额
// 向下或向上取整，且不超过格子当前值。要求 0 <= amount <= curSum。
func reduce(plan *grid, cells []int, curSum, amount int64) {
	var cum, prev int64
	for _, c := range cells {
		cum += plan.values[c]
		cur := mulDiv(cum, amount, curSum)
		plan.setCell(c, plan.values[c]-(cur-prev))
		prev = cur
	}
}

// boost 把 amount 精确加到格子上（在现有值基础上增加）：按当前值比例
// （全为 0 时平均分），每格增量为精确份额向下或向上取整，合计分毫不差。
func boost(plan *grid, cells []int, curSum, amount int64) {
	n := len(cells)
	if n == 0 {
		return
	}
	if curSum <= 0 {
		base := amount / int64(n)
		rem := amount % int64(n)
		for i, c := range cells {
			v := base
			if int64(i) < rem {
				v++
			}
			plan.setCell(c, plan.values[c]+v)
		}
		return
	}
	var cum, prev int64
	for _, c := range cells {
		cum += plan.values[c]
		cur := mulDiv(cum, amount, curSum)
		plan.setCell(c, plan.values[c]+cur-prev)
		prev = cur
	}
}

// mulDiv 计算 floor(a*b/m)，要求 0 <= a <= m 且 m > 0（保证不溢出）。
func mulDiv(a, b, m int64) int64 {
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	q, _ := bits.Div64(hi, lo, uint64(m))
	return int64(q)
}
