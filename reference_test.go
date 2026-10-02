package planadjust_test

// 独立参考实现：刻意用最朴素、显然正确的写法（显式 SKU 集合、逐格扫描），
// 与被测库不共享任何代码，用于逐格对拍。

import (
	"sort"

	pa "github.com/example/planadjust"
)

type refScope struct {
	skus   map[int]bool
	d0, d1 int
}

func (s refScope) cells() []int {
	out := []int{}
	for k := range s.skus {
		for d := s.d0; d < s.d1; d++ {
			out = append(out, k*pa.Days+d)
		}
	}
	sort.Ints(out)
	return out
}

func (s refScope) contains(o refScope) bool {
	if !(s.d0 <= o.d0 && o.d1 <= s.d1) {
		return false
	}
	for k := range o.skus {
		if !s.skus[k] {
			return false
		}
	}
	return true
}

func (s refScope) equal(o refScope) bool {
	return s.d0 == o.d0 && s.d1 == o.d1 && s.contains(o) && o.contains(s)
}

func (s refScope) intersects(o refScope) bool {
	if s.d0 >= o.d1 || o.d0 >= s.d1 {
		return false
	}
	for k := range s.skus {
		if o.skus[k] {
			return true
		}
	}
	return false
}

type refAdj struct {
	id     int64
	sc     refScope
	target int64
	status pa.Status
}

type refState struct {
	nSKU     int
	forecast []int64
	grid     []int64
	all      []*refAdj
}

func (rs *refState) active() []*refAdj {
	var out []*refAdj
	for _, a := range rs.all {
		if a.status == pa.StatusActive {
			out = append(out, a)
		}
	}
	return out
}

// maximalContained applied 中严格包含于 sc 的极大调整。
func maximalContainedRef(applied []*refAdj, sc refScope) []*refAdj {
	var out []*refAdj
	for _, a := range applied {
		if a.sc.equal(sc) || !sc.contains(a.sc) {
			continue
		}
		maximal := true
		for _, b := range applied {
			if b != a && !b.sc.equal(a.sc) && !b.sc.equal(sc) &&
				sc.contains(b.sc) && b.sc.contains(a.sc) {
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

func nearestContainerRef(applied []*refAdj, sc refScope) *refAdj {
	var best *refAdj
	for _, a := range applied {
		if a.sc.equal(sc) || !a.sc.contains(sc) {
			continue
		}
		if best == nil || best.sc.contains(a.sc) {
			best = a
		}
	}
	return best
}

// refShare 最大余数法份额（与规格一致：精确份额向下/向上取整，合计严格相等）。
func refShare(total int64, weights []int64) []int64 {
	n := len(weights)
	out := make([]int64, n)
	if n == 0 {
		return out
	}
	var W int64
	for _, w := range weights {
		W += w
	}
	if W == 0 {
		base := total / int64(n)
		extra := total % int64(n)
		for i := range out {
			out[i] = base
			if int64(i) < extra {
				out[i]++
			}
		}
		return out
	}
	type frac struct {
		idx int
		rem int64
	}
	fr := make([]frac, n)
	var floorSum int64
	for i, w := range weights {
		q := total * w / W
		out[i] = q
		floorSum += q
		fr[i] = frac{i, total * w % W}
	}
	sort.Slice(fr, func(i, j int) bool {
		if fr[i].rem != fr[j].rem {
			return fr[i].rem > fr[j].rem
		}
		return fr[i].idx < fr[j].idx
	})
	for k := int64(0); k < total-floorSum; k++ {
		out[fr[k].idx]++
	}
	return out
}

// refApply 在 grid 上应用调整 a；不可满足时返回 false 且 grid 不变。
func refApply(grid []int64, a *refAdj, applied []*refAdj) bool {
	frozen := maximalContainedRef(applied, a.sc)
	frozenCells := map[int]bool{}
	var frozenSum int64
	for _, f := range frozen {
		frozenSum += f.target
		for _, c := range f.sc.cells() {
			frozenCells[c] = true
		}
	}
	var movable []int
	var curSum int64
	for _, c := range a.sc.cells() {
		curSum += grid[c]
		if !frozenCells[c] {
			movable = append(movable, c)
		}
	}
	remainder := a.target - frozenSum
	if remainder < 0 {
		return false
	}
	if remainder > 0 && len(movable) == 0 {
		return false
	}
	delta := a.target - curSum
	container := nearestContainerRef(applied, a.sc)
	var absorb []int
	if container != nil && delta != 0 {
		excluded := map[int]bool{}
		for _, o := range maximalContainedRef(applied, container.sc) {
			if !a.sc.contains(o.sc) {
				for _, c := range o.sc.cells() {
					excluded[c] = true
				}
			}
		}
		inNew := map[int]bool{}
		for _, c := range a.sc.cells() {
			inNew[c] = true
		}
		var absorbSum int64
		for _, c := range container.sc.cells() {
			if !inNew[c] && !excluded[c] {
				absorb = append(absorb, c)
				absorbSum += grid[c]
			}
		}
		if len(absorb) == 0 {
			return false
		}
		if delta > 0 && absorbSum < delta {
			return false
		}
	}
	// 写入
	w := make([]int64, len(movable))
	for i, c := range movable {
		w[i] = grid[c]
	}
	share := refShare(remainder, w)
	for i, c := range movable {
		grid[c] = share[i]
	}
	if container != nil && delta != 0 {
		w = w[:0]
		for _, c := range absorb {
			w = append(w, grid[c])
		}
		if delta > 0 {
			share = refShare(delta, w)
			for i, c := range absorb {
				grid[c] -= share[i]
			}
		} else {
			share = refShare(-delta, w)
			for i, c := range absorb {
				grid[c] += share[i]
			}
		}
	}
	return true
}

// refReplay 从预测出发按提交顺序应用 active，返回计划值与冲突列表。
func (rs *refState) refReplay(active []*refAdj) ([]int64, []*refAdj) {
	grid := make([]int64, len(rs.forecast))
	copy(grid, rs.forecast)
	var applied, conflicts []*refAdj
	for _, a := range active {
		if refApply(grid, a, applied) {
			applied = append(applied, a)
		} else {
			conflicts = append(conflicts, a)
		}
	}
	return grid, conflicts
}

// refSubmit 返回是否被接受；不接受时参考状态完全不变。
func (rs *refState) refSubmit(sc refScope, target int64) bool {
	na := &refAdj{id: int64(len(rs.all)) + 1, sc: sc, target: target, status: pa.StatusActive}
	var voids, replaces, kept []*refAdj
	for _, a := range rs.active() {
		switch {
		case a.sc.equal(sc):
			replaces = append(replaces, a)
		case a.sc.intersects(sc) && !a.sc.contains(sc) && !sc.contains(a.sc):
			voids = append(voids, a)
		default:
			kept = append(kept, a)
		}
	}
	candidate := append(append([]*refAdj{}, kept...), na)
	grid, conflicts := rs.refReplay(candidate)
	for _, c := range conflicts {
		if c == na {
			return false
		}
	}
	for _, a := range voids {
		a.status = pa.StatusVoided
	}
	for _, a := range replaces {
		a.status = pa.StatusReplaced
	}
	for _, a := range conflicts {
		a.status = pa.StatusConflict
	}
	rs.all = append(rs.all, na)
	rs.grid = grid
	return true
}

func (rs *refState) refUndo(id int64) bool {
	var target *refAdj
	for _, a := range rs.all {
		if a.id == id {
			target = a
		}
	}
	if target == nil || target.status != pa.StatusActive {
		return false
	}
	target.status = pa.StatusUndone
	grid, conflicts := rs.refReplay(rs.active())
	for _, a := range conflicts {
		a.status = pa.StatusConflict
	}
	rs.grid = grid
	return true
}

func (rs *refState) refReplaceForecast(f []int64) {
	rs.forecast = f
	grid, conflicts := rs.refReplay(rs.active())
	for _, a := range conflicts {
		a.status = pa.StatusConflict
	}
	rs.grid = grid
}
