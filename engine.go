package planadjust

// 重算引擎：任何时刻计划值 == 从当前统计预测出发、按提交先后依次应用所有生效调整。
// 生效调整集始终构成层叠（laminar）族：相交即嵌套。
//
// 关键不变量：任意已应用调整 e 被应用之后，其范围内格子合计恒等于 e.Target——
// 之后包含它的调整通过反向消化保持它的合计，被它包含的调整只动自己的格子并同样消化。

// adjEntry 一条调整及其解析后的内部范围。
type adjEntry struct {
	adj Adjustment
	sc  iscope
}

// prodMember 某个 SKU 是否属于商品维度。
func prodMember(s prodScope, sku int, skuCat []int) bool {
	switch s.kind {
	case ProductAll:
		return true
	case ProductCategory:
		return skuCat[sku] == s.cat
	default:
		return sku == s.sku
	}
}

// maximalContained 返回 applied 中严格包含于 sc 的极大调整（它们的格子互不相交）。
func maximalContained(applied []*adjEntry, sc iscope, skuCat []int) []*adjEntry {
	var out []*adjEntry
	for _, a := range applied {
		if a.sc.equal(sc) || !sc.contains(a.sc, skuCat) {
			continue
		}
		maximal := true
		for _, b := range applied {
			if b != a && !b.sc.equal(a.sc) && !b.sc.equal(sc) &&
				sc.contains(b.sc, skuCat) && b.sc.contains(a.sc, skuCat) {
				maximal = false
				break
			}
		}
		if maximal {
			out = append(out, a)
		}
	}
	return out
}

// nearestContainer 返回 applied 中严格包含 sc 的最小范围调整。
func nearestContainer(applied []*adjEntry, sc iscope, skuCat []int) *adjEntry {
	var best *adjEntry
	for _, a := range applied {
		if a.sc.equal(sc) || !a.sc.contains(sc, skuCat) {
			continue
		}
		if best == nil || best.sc.contains(a.sc, skuCat) {
			best = a
		}
	}
	return best
}

// markCells 把范围内的格子在位图中置位。
func (p *Planner) markCells(marks []uint64, sc iscope) {
	d0, d1 := sc.days()
	set := func(sku int) {
		base := sku * Days
		for d := d0; d < d1; d++ {
			c := base + d
			marks[c>>6] |= 1 << (uint(c) & 63)
		}
	}
	switch sc.prod.kind {
	case ProductAll:
		for k := range p.skuKeys {
			set(k)
		}
	case ProductCategory:
		for _, k := range p.catSKUs[sc.prod.cat] {
			set(k)
		}
	default:
		set(sc.prod.sku)
	}
}

func marked(marks []uint64, cell int) bool {
	return marks[cell>>6]&(1<<(uint(cell)&63)) != 0
}

// cellsWhere 收集 sc 范围内满足 keep 的格子下标，写入 dst 返回。
func (p *Planner) cellsWhere(dst []int, sc iscope, keep func(sku, cell int) bool) []int {
	d0, d1 := sc.days()
	out := dst[:0]
	collect := func(sku int) {
		base := sku * Days
		for d := d0; d < d1; d++ {
			if keep(sku, base+d) {
				out = append(out, base+d)
			}
		}
	}
	switch sc.prod.kind {
	case ProductAll:
		for k := range p.skuKeys {
			collect(k)
		}
	case ProductCategory:
		for _, k := range p.catSKUs[sc.prod.cat] {
			collect(k)
		}
	default:
		collect(sc.prod.sku)
	}
	return out
}

// applyOne 在 plan 上应用调整 e；applied 为先于 e 提交且当前生效的调整。
// 所有可行性检查通过后才写 plan；不可满足时返回 false 且 plan 逐格不变。
func (p *Planner) applyOne(plan []int64, e *adjEntry, applied []*adjEntry) bool {
	skuCat := p.skuCat
	d0, d1 := e.sc.days()

	// 1. 被 e 完全包含的更早生效调整：格子原样不动。
	frozen := maximalContained(applied, e.sc, skuCat)
	var frozenSum int64
	clear(p.marks)
	for _, f := range frozen {
		frozenSum += f.adj.Target
		p.markCells(p.marks, f.sc)
	}

	// 2. 可动格子与当前合计。不变量：被包含的生效调整范围内合计恒等于其目标，
	// 因此当前合计 = 冻结合计 + 可动格子合计。
	movable := p.cellsWhere(p.tmpCells, e.sc, func(_, cell int) bool { return !marked(p.marks, cell) })
	curSum := frozenSum
	for _, c := range movable {
		curSum += plan[c]
	}

	remainder := e.adj.Target - frozenSum
	if remainder < 0 {
		return false // 目标小于原样不动部分的合计
	}
	if remainder > 0 && len(movable) == 0 {
		return false // 有量要分摊，却没有能动的格子
	}

	// 3. 反向消化：最近的包含者吸收增减量，保持它的合计不变。
	delta := e.adj.Target - curSum
	container := nearestContainer(applied, e.sc, skuCat)
	var absorb []int
	if container != nil && delta != 0 {
		// 可消化格子 = 包含者范围内、新范围之外、也不在其他更小生效调整里的格子。
		clear(p.marks)
		for _, o := range maximalContained(applied, container.sc, skuCat) {
			if !e.sc.contains(o.sc, skuCat) { // 层叠结构下即与 e 无关的那些
				p.markCells(p.marks, o.sc)
			}
		}
		absorb = p.cellsWhere(p.tmpCells2, container.sc, func(sku, cell int) bool {
			inNew := prodMember(e.sc.prod, sku, skuCat) && cell%Days >= d0 && cell%Days < d1
			return !inNew && !marked(p.marks, cell)
		})
		if len(absorb) == 0 {
			return false // 有量要消化，却没有能动的格子
		}
		if delta > 0 {
			var absorbSum int64
			for _, c := range absorb {
				absorbSum += plan[c]
			}
			if absorbSum < delta {
				return false // 需要反向消化的格子不够减
			}
		}
	}

	// 4. 检查全部通过，开始写入。
	p.tmpW = p.tmpW[:0]
	for _, c := range movable {
		p.tmpW = append(p.tmpW, plan[c])
	}
	alloc := allocate(remainder, p.tmpW, p.tmpAlloc[:0])
	p.tmpAlloc = alloc
	for i, c := range movable {
		plan[c] = alloc[i]
	}
	if container != nil && delta != 0 {
		p.tmpW = p.tmpW[:0]
		for _, c := range absorb {
			p.tmpW = append(p.tmpW, plan[c])
		}
		if delta > 0 {
			deallocate(delta, p.tmpW)
			for i, c := range absorb {
				plan[c] = p.tmpW[i]
			}
		} else {
			inc := allocate(-delta, p.tmpW, p.tmpAlloc[:0])
			p.tmpAlloc = inc
			for i, c := range absorb {
				plan[c] += inc[i]
			}
		}
	}
	return true
}

// replay 从 forecast 出发按提交顺序应用 active 中的调整，返回新的计划值。
// 重算中发现无法满足的调整进入 conflicts 列表（不再生效，其余照常）；
// 本函数不修改任何调整的状态，由调用方在确认提交后统一落状态。
func (p *Planner) replay(active []*adjEntry) ([]int64, []*adjEntry) {
	plan := make([]int64, len(p.forecast))
	copy(plan, p.forecast)
	var applied, conflicts []*adjEntry
	for _, e := range active {
		if p.applyOne(plan, e, applied) {
			applied = append(applied, e)
		} else {
			conflicts = append(conflicts, e)
		}
	}
	return plan, conflicts
}
