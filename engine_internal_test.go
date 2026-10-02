package planadjust

import (
	"path/filepath"
	"testing"
)

// 重算安全网：若某条旧调整在重算时不可满足（正常流程下不会发生，
// 这里直接构造不一致的生效集验证），它被标记冲突、不再生效，其余照常。
func TestReplayMarksConflict(t *testing.T) {
	defs := []SKUDef{
		{SKU: "SKU1", Category: "CAT1"},
		{SKU: "SKU2", Category: "CAT1"},
	}
	p, err := Create(filepath.Join(t.TempDir(), "x.db"), Config{StoreID: "s", SKUs: defs})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	mk := func(id int64, sc Scope, target int64) *adjEntry {
		isc, err := p.resolve(sc)
		if err != nil {
			t.Fatal(err)
		}
		return &adjEntry{adj: Adjustment{ID: id, Scope: sc, Target: target, Status: StatusActive}, sc: isc}
	}
	child := mk(1, Scope{Product: ProductSKU, SKU: "SKU1", Time: TimeDay, Day: 1}, 100)
	parent := mk(2, Scope{Product: ProductCategory, Category: "CAT1", Time: TimeDay, Day: 1}, 50)

	plan, conflicts := p.replay([]*adjEntry{child, parent})
	if len(conflicts) != 1 || conflicts[0] != parent {
		t.Fatalf("want parent in conflicts, got %v", conflicts)
	}
	if got := plan[p.skuIdx["SKU1"]*Days]; got != 100 {
		t.Fatalf("SKU1 day1 = %d, want 100 (child still applied)", got)
	}
}
