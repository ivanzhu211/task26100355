package planadjust

import "testing"

func testLayout() *layout {
	return newLayout([]SKUInfo{
		{SKU: "A1", Category: "catA"},
		{SKU: "A2", Category: "catA"},
		{SKU: "B1", Category: "catB"},
	})
}

func ps(kind ProductKind, cat, sku string) ProductScope {
	return ProductScope{Kind: kind, Category: cat, SKU: sku}
}

func ts(kind TimeKind, week, day int) TimeScope {
	return TimeScope{Kind: kind, Week: week, Day: day}
}

func TestRelate(t *testing.T) {
	l := testLayout()
	cases := []struct {
		name string
		a, b Scope
		want scopeRelation
	}{
		{"equal all-period",
			Scope{ps(ProductAll, "", ""), ts(TimePeriod, 0, 0)},
			Scope{ps(ProductAll, "", ""), ts(TimePeriod, 0, 0)}, relEqual},
		{"all contains cat",
			Scope{ps(ProductAll, "", ""), ts(TimePeriod, 0, 0)},
			Scope{ps(ProductCategory, "catA", ""), ts(TimePeriod, 0, 0)}, relContains},
		{"cat contains own sku",
			Scope{ps(ProductCategory, "catA", ""), ts(TimePeriod, 0, 0)},
			Scope{ps(ProductSKU, "", "A1"), ts(TimePeriod, 0, 0)}, relContains},
		{"cat disjoint other cat sku",
			Scope{ps(ProductCategory, "catA", ""), ts(TimePeriod, 0, 0)},
			Scope{ps(ProductSKU, "", "B1"), ts(TimePeriod, 0, 0)}, relDisjoint},
		{"week contains its day",
			Scope{ps(ProductAll, "", ""), ts(TimeWeek, 3, 0)},
			Scope{ps(ProductAll, "", ""), ts(TimeDay, 0, 15)}, relContains},
		{"day disjoint other week",
			Scope{ps(ProductAll, "", ""), ts(TimeDay, 0, 15)},
			Scope{ps(ProductAll, "", ""), ts(TimeWeek, 4, 0)}, relDisjoint},
		{"cross cat-week vs sku-period",
			Scope{ps(ProductCategory, "catA", ""), ts(TimeWeek, 1, 0)},
			Scope{ps(ProductSKU, "", "A1"), ts(TimePeriod, 0, 0)}, relCross},
		{"cross week vs day-of-other-dim",
			Scope{ps(ProductCategory, "catA", ""), ts(TimeWeek, 1, 0)},
			Scope{ps(ProductSKU, "", "A1"), ts(TimeDay, 0, 3)}, relContains},
		{"disjoint weeks",
			Scope{ps(ProductAll, "", ""), ts(TimeWeek, 1, 0)},
			Scope{ps(ProductAll, "", ""), ts(TimeWeek, 2, 0)}, relDisjoint},
		{"sku in cat contained",
			Scope{ps(ProductSKU, "", "A2"), ts(TimePeriod, 0, 0)},
			Scope{ps(ProductCategory, "catA", ""), ts(TimePeriod, 0, 0)}, relContainedBy},
	}
	for _, c := range cases {
		if got := l.relate(c.a, c.b); got != c.want {
			t.Errorf("%s: relate = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDaysOf(t *testing.T) {
	if d := daysOf(ts(TimePeriod, 0, 0)); len(d) != 91 || d[0] != 0 || d[90] != 90 {
		t.Errorf("period days wrong: %v", d)
	}
	if d := daysOf(ts(TimeWeek, 13, 0)); len(d) != 7 || d[0] != 84 || d[6] != 90 {
		t.Errorf("week 13 days wrong: %v", d)
	}
	if d := daysOf(ts(TimeWeek, 14, 0)); d != nil {
		t.Errorf("week 14 should be invalid")
	}
	if d := daysOf(ts(TimeDay, 0, 91)); len(d) != 1 || d[0] != 90 {
		t.Errorf("day 91 wrong: %v", d)
	}
	if d := daysOf(ts(TimeDay, 0, 92)); d != nil {
		t.Errorf("day 92 should be invalid")
	}
}

func TestValid(t *testing.T) {
	l := testLayout()
	if !l.valid(Scope{ps(ProductCategory, "catB", ""), ts(TimeWeek, 5, 0)}) {
		t.Error("catB week5 should be valid")
	}
	if l.valid(Scope{ps(ProductCategory, "catX", ""), ts(TimeWeek, 5, 0)}) {
		t.Error("catX should be invalid")
	}
	if l.valid(Scope{ps(ProductSKU, "", "ZZ"), ts(TimeWeek, 5, 0)}) {
		t.Error("unknown SKU should be invalid")
	}
}
