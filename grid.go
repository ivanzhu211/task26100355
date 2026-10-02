package planadjust

// grid 保存逐 SKU 逐日的整数网格及预聚合，支持任意范围的快速合计查询。
// 行 = SKU（与 layout 行号一致），列 = 天（0..90）。
type grid struct {
	rows   int
	values []int64 // rows*91，行优先
	catDay []int64 // 品类数*91
	allDay []int64 // 91
	total  int64
	catOf  []int // 行号 -> 品类序号
}

// newGrid 创建全零网格。
func newGrid(l *layout) *grid {
	catOf := make([]int, len(l.skus))
	for i := range catOf {
		catOf[i] = l.catIndex[l.catOfSKU[i]]
	}
	return &grid{
		rows:   len(l.skus),
		values: make([]int64, len(l.skus)*Days),
		catDay: make([]int64, len(l.cats)*Days),
		allDay: make([]int64, Days),
		catOf:  catOf,
	}
}

// get 返回 (row, day) 的值。
func (g *grid) get(row, day int) int64 {
	return g.values[row*Days+day]
}

// setCell 按扁平下标设置值并维护聚合。
func (g *grid) setCell(i int, v int64) {
	delta := v - g.values[i]
	if delta == 0 {
		return
	}
	g.values[i] = v
	day := i % Days
	g.catDay[g.catOf[i/Days]*Days+day] += delta
	g.allDay[day] += delta
	g.total += delta
}

// set 设置 (row, day) 的值并维护聚合。
func (g *grid) set(row, day int, v int64) {
	i := row*Days + day
	delta := v - g.values[i]
	if delta == 0 {
		return
	}
	g.values[i] = v
	g.catDay[g.catOf[row]*Days+day] += delta
	g.allDay[day] += delta
	g.total += delta
}

// add 将 (row, day) 增加 delta 并维护聚合。
func (g *grid) add(row, day int, delta int64) {
	if delta == 0 {
		return
	}
	i := row*Days + day
	g.values[i] += delta
	g.catDay[g.catOf[row]*Days+day] += delta
	g.allDay[day] += delta
	g.total += delta
}

// sum 返回范围内合计。
func (g *grid) sum(l *layout, s Scope) int64 {
	days := daysOf(s.Time)
	if days == nil {
		return 0
	}
	var total int64
	switch s.Product.Kind {
	case ProductAll:
		for _, d := range days {
			total += g.allDay[d]
		}
	case ProductCategory:
		ci, ok := l.catIndex[s.Product.Category]
		if !ok {
			return 0
		}
		base := ci * Days
		for _, d := range days {
			total += g.catDay[base+d]
		}
	case ProductSKU:
		row, ok := l.skuIndex[s.Product.SKU]
		if !ok {
			return 0
		}
		base := row * Days
		for _, d := range days {
			total += g.values[base+d]
		}
	}
	return total
}

// clone 返回深拷贝。
func (g *grid) clone() *grid {
	c := &grid{
		rows:   g.rows,
		values: make([]int64, len(g.values)),
		catDay: make([]int64, len(g.catDay)),
		allDay: make([]int64, len(g.allDay)),
		total:  g.total,
		catOf:  g.catOf,
	}
	copy(c.values, g.values)
	copy(c.catDay, g.catDay)
	copy(c.allDay, g.allDay)
	return c
}

// loadFrom 用 src 的内容覆盖自身（尺寸需一致）。
func (g *grid) loadFrom(src *grid) {
	copy(g.values, src.values)
	copy(g.catDay, src.catDay)
	copy(g.allDay, src.allDay)
	g.total = src.total
}
