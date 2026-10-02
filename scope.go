package planadjust

// layout 保存门店的商品结构：SKU 顺序、品类归属及索引映射。
type layout struct {
	skus     []SKUInfo        // 行号 -> SKU
	skuIndex map[string]int   // SKU -> 行号
	catSKUs  map[string][]int // 品类 -> 行号列表（升序）
	catOfSKU []string         // 行号 -> 品类
	cats     []string         // 品类列表（首次出现顺序）
	catIndex map[string]int   // 品类 -> 品类序号
}

// newLayout 由 SKU 清单构建商品结构。
func newLayout(skus []SKUInfo) *layout {
	l := &layout{
		skus:     skus,
		skuIndex: make(map[string]int, len(skus)),
		catSKUs:  make(map[string][]int),
		catOfSKU: make([]string, len(skus)),
		catIndex: make(map[string]int),
	}
	for i, s := range skus {
		l.skuIndex[s.SKU] = i
		l.catOfSKU[i] = s.Category
		if _, ok := l.catIndex[s.Category]; !ok {
			l.catIndex[s.Category] = len(l.cats)
			l.cats = append(l.cats, s.Category)
		}
		l.catSKUs[s.Category] = append(l.catSKUs[s.Category], i)
	}
	return l
}

// skuRows 返回商品维度覆盖的行号；ok=false 表示范围非法。
// ProductAll 返回 nil（调用方按全部行处理）。
func (l *layout) skuRows(p ProductScope) ([]int, bool) {
	switch p.Kind {
	case ProductAll:
		return nil, true
	case ProductCategory:
		rows, ok := l.catSKUs[p.Category]
		return rows, ok
	case ProductSKU:
		row, ok := l.skuIndex[p.SKU]
		if !ok {
			return nil, false
		}
		return []int{row}, true
	}
	return nil, false
}

// daysOf 返回时间维度覆盖的 0 起天序号；非法时返回 nil。
func daysOf(t TimeScope) []int {
	switch t.Kind {
	case TimePeriod:
		d := make([]int, Days)
		for i := range d {
			d[i] = i
		}
		return d
	case TimeWeek:
		if t.Week < 1 || t.Week > Weeks {
			return nil
		}
		d := make([]int, DaysPerWeek)
		for i := range d {
			d[i] = (t.Week-1)*DaysPerWeek + i
		}
		return d
	case TimeDay:
		if t.Day < 1 || t.Day > Days {
			return nil
		}
		return []int{t.Day - 1}
	}
	return nil
}

// scopeRelation 描述两个范围之间的位置关系。
type scopeRelation int

const (
	relEqual       scopeRelation = iota // 完全相同
	relContains                         // a 严格包含 b
	relContainedBy                      // a 被 b 严格包含
	relCross                            // 相交但互不包含
	relDisjoint                         // 不相交
)

// dimRelation 是单维度上的关系。
type dimRelation int

const (
	dimEqual dimRelation = iota
	dimContains
	dimContainedBy
	dimCross // 相交但互不包含（商品/时间维度上实际不会出现，保留完整）
	dimDisjoint
)

// productRel 计算商品维度上 a 相对 b 的关系。
func (l *layout) productRel(a, b ProductScope) dimRelation {
	if a.Kind == ProductAll && b.Kind == ProductAll {
		return dimEqual
	}
	if a.Kind == ProductAll {
		return dimContains
	}
	if b.Kind == ProductAll {
		return dimContainedBy
	}
	if a.Kind == ProductCategory && b.Kind == ProductCategory {
		if a.Category == b.Category {
			return dimEqual
		}
		return dimDisjoint
	}
	if a.Kind == ProductSKU && b.Kind == ProductSKU {
		if a.SKU == b.SKU {
			return dimEqual
		}
		return dimDisjoint
	}
	if a.Kind == ProductCategory && b.Kind == ProductSKU {
		row, ok := l.skuIndex[b.SKU]
		if !ok {
			return dimDisjoint
		}
		if l.catOfSKU[row] == a.Category {
			return dimContains
		}
		return dimDisjoint
	}
	// a 是 SKU，b 是品类
	row, ok := l.skuIndex[a.SKU]
	if !ok {
		return dimDisjoint
	}
	if l.catOfSKU[row] == b.Category {
		return dimContainedBy
	}
	return dimDisjoint
}

// timeRel 计算时间维度上 a 相对 b 的关系。
func timeRel(a, b TimeScope) dimRelation {
	if a.Kind == TimePeriod && b.Kind == TimePeriod {
		return dimEqual
	}
	if a.Kind == TimePeriod {
		return dimContains
	}
	if b.Kind == TimePeriod {
		return dimContainedBy
	}
	if a.Kind == TimeWeek && b.Kind == TimeWeek {
		if a.Week == b.Week {
			return dimEqual
		}
		return dimDisjoint
	}
	if a.Kind == TimeDay && b.Kind == TimeDay {
		if a.Day == b.Day {
			return dimEqual
		}
		return dimDisjoint
	}
	if a.Kind == TimeWeek && b.Kind == TimeDay {
		if b.Day >= (a.Week-1)*DaysPerWeek+1 && b.Day <= a.Week*DaysPerWeek {
			return dimContains
		}
		return dimDisjoint
	}
	// a 是天，b 是周
	if a.Day >= (b.Week-1)*DaysPerWeek+1 && a.Day <= b.Week*DaysPerWeek {
		return dimContainedBy
	}
	return dimDisjoint
}

// combine 把两个维度上的关系合成范围关系。
// 两维各自为相等/包含/被包含/不相交；任一维不相交则整体不相交。
func combine(p, t dimRelation) scopeRelation {
	if p == dimDisjoint || t == dimDisjoint {
		return relDisjoint
	}
	if p == dimEqual && t == dimEqual {
		return relEqual
	}
	if (p == dimContains || p == dimEqual) && (t == dimContains || t == dimEqual) {
		return relContains
	}
	if (p == dimContainedBy || p == dimEqual) && (t == dimContainedBy || t == dimEqual) {
		return relContainedBy
	}
	return relCross
}

// relate 计算 a 相对 b 的关系。
func (l *layout) relate(a, b Scope) scopeRelation {
	return combine(l.productRel(a.Product, b.Product), timeRel(a.Time, b.Time))
}

// intersect 判断两个范围是否有共同格子。
func (l *layout) intersect(a, b Scope) bool {
	return l.relate(a, b) != relDisjoint
}

// valid 校验范围引用的品类/SKU/周/天是否合法。
func (l *layout) valid(s Scope) bool {
	if _, ok := l.skuRows(s.Product); !ok {
		return false
	}
	return daysOf(s.Time) != nil
}
