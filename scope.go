package planadjust

// 内部范围表示：商品维度是 全部>品类>SKU 的树，时间维度是 计划期>周>天 的树，
// 两个维度各自都是层叠（laminar）结构，因此包含/相交判断都是 O(1)。

type prodScope struct {
	kind ProductKind
	cat  int // ProductCategory 时有效
	sku  int // ProductSKU 时有效
}

type timeScope struct {
	d0, d1 int // 半开区间 [d0, d1)，0-based 天
}

type iscope struct {
	prod prodScope
	time timeScope
}

// resolve 把外部 Scope 解析为内部表示，校验 SKU/品类/周/日合法性。
func (p *Planner) resolve(s Scope) (iscope, error) {
	var out iscope
	switch s.Product {
	case ProductAll:
		out.prod = prodScope{kind: ProductAll}
	case ProductCategory:
		c, ok := p.catIdx[s.Category]
		if !ok {
			return out, ErrInvalidScope
		}
		out.prod = prodScope{kind: ProductCategory, cat: c}
	case ProductSKU:
		k, ok := p.skuIdx[s.SKU]
		if !ok {
			return out, ErrInvalidScope
		}
		out.prod = prodScope{kind: ProductSKU, sku: k}
	default:
		return out, ErrInvalidScope
	}
	switch s.Time {
	case TimePeriod:
		out.time = timeScope{0, Days}
	case TimeWeek:
		if s.Week < 1 || s.Week > Weeks {
			return out, ErrInvalidScope
		}
		out.time = timeScope{(s.Week - 1) * DaysPerWeek, s.Week * DaysPerWeek}
	case TimeDay:
		if s.Day < 1 || s.Day > Days {
			return out, ErrInvalidScope
		}
		out.time = timeScope{s.Day - 1, s.Day}
	default:
		return out, ErrInvalidScope
	}
	return out, nil
}

// prodContains a 的商品维度是否完全包含 b（含相等）。
func prodContains(a, b prodScope, skuCat []int) bool {
	switch a.kind {
	case ProductAll:
		return true
	case ProductCategory:
		switch b.kind {
		case ProductAll:
			return false
		case ProductCategory:
			return a.cat == b.cat
		default: // ProductSKU
			return skuCat[b.sku] == a.cat
		}
	default: // ProductSKU
		return b.kind == ProductSKU && a.sku == b.sku
	}
}

// prodEqual 商品维度是否相等。
func prodEqual(a, b prodScope) bool {
	return a.kind == b.kind && a.cat == b.cat && a.sku == b.sku
}

// contains a 是否完全包含 b（含相等）。
func (s iscope) contains(b iscope, skuCat []int) bool {
	return prodContains(s.prod, b.prod, skuCat) &&
		s.time.d0 <= b.time.d0 && b.time.d1 <= s.time.d1
}

// equal 范围是否完全相同。
func (s iscope) equal(b iscope) bool {
	return prodEqual(s.prod, b.prod) && s.time == b.time
}

// intersects 两个范围是否有交集。两个维度各自是树，相交当且仅当互相嵌套。
func (s iscope) intersects(b iscope, skuCat []int) bool {
	prodHit := prodContains(s.prod, b.prod, skuCat) || prodContains(b.prod, s.prod, skuCat)
	timeHit := s.time.d0 < b.time.d1 && b.time.d0 < s.time.d1
	return prodHit && timeHit
}

// days 返回时间维度的天区间。
func (s iscope) days() (d0, d1 int) {
	return s.time.d0, s.time.d1
}
