package planadjust_test

import (
	"path/filepath"
	"testing"

	pa "github.com/example/planadjust"
)

func newTestPlanner(t *testing.T) (*pa.Planner, catalog) {
	t.Helper()
	c := catalog{
		skus:    []string{"SKU1", "SKU2", "SKU3"},
		cats:    []string{"CAT1", "CAT2"},
		skuCat:  []int{0, 0, 1},
		catSKUs: [][]int{{0, 1}, {2}},
	}
	defs := []pa.SKUDef{
		{SKU: "SKU1", Category: "CAT1"},
		{SKU: "SKU2", Category: "CAT1"},
		{SKU: "SKU3", Category: "CAT2"},
	}
	p, err := pa.Create(filepath.Join(t.TempDir(), "edge.db"), pa.Config{StoreID: "s", SKUs: defs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p, c
}

func setForecast(t *testing.T, p *pa.Planner, c catalog, day1 []int64) {
	t.Helper()
	fc := map[string][pa.Days]int64{}
	for i, sku := range c.skus {
		var days [pa.Days]int64
		days[0] = day1[i]
		fc[sku] = days
	}
	if err := p.ReplaceForecast(fc); err != nil {
		t.Fatal(err)
	}
}

func mustSum(t *testing.T, p *pa.Planner, sc pa.Scope, want int64) {
	t.Helper()
	got, err := p.PlanSum(sc)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("PlanSum(%+v)=%d want %d", sc, got, want)
	}
}

func mustVal(t *testing.T, p *pa.Planner, sku string, day int, want int64) {
	t.Helper()
	got, err := p.PlanValue(sku, day)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("PlanValue(%s,%d)=%d want %d", sku, day, got, want)
	}
}

func mustStatus(t *testing.T, p *pa.Planner, id int64, want pa.Status) {
	t.Helper()
	for _, a := range p.Adjustments() {
		if a.ID == id {
			if a.Status != want {
				t.Fatalf("adj %d status %v want %v", id, a.Status, want)
			}
			return
		}
	}
	t.Fatalf("adj %d not found", id)
}

var (
	scopeAllDay1  = pa.Scope{Product: pa.ProductAll, Time: pa.TimeDay, Day: 1}
	scopeCat1Day1 = pa.Scope{Product: pa.ProductCategory, Category: "CAT1", Time: pa.TimeDay, Day: 1}
	scopeSKU1Day1 = pa.Scope{Product: pa.ProductSKU, SKU: "SKU1", Time: pa.TimeDay, Day: 1}
	scopeSKU3Day1 = pa.Scope{Product: pa.ProductSKU, SKU: "SKU3", Time: pa.TimeDay, Day: 1}
	scopeAllAll   = pa.Scope{Product: pa.ProductAll, Time: pa.TimePeriod}
)

// 嵌套包含：被包含的调整格子原样不动；包含者的合计通过反向消化保持不变。
func TestNestedAbsorption(t *testing.T) {
	p, c := newTestPlanner(t)
	setForecast(t, p, c, []int64{10, 20, 30})

	if _, err := p.Submit(scopeSKU1Day1, 50); err != nil {
		t.Fatal(err)
	}
	mustVal(t, p, "SKU1", 1, 50)

	// CAT1 day1 = 120：SKU1 冻结在 50，SKU2 拿 70。
	if _, err := p.Submit(scopeCat1Day1, 120); err != nil {
		t.Fatal(err)
	}
	mustVal(t, p, "SKU1", 1, 50)
	mustVal(t, p, "SKU2", 1, 70)
	mustSum(t, p, scopeCat1Day1, 120)

	// 全店 day1 = 300：CAT1 冻结在 120，SKU3 拿 180。
	if _, err := p.Submit(scopeAllDay1, 300); err != nil {
		t.Fatal(err)
	}
	mustVal(t, p, "SKU3", 1, 180)
	mustSum(t, p, scopeAllDay1, 300)

	// SKU3 想改成 100：全店合计不能变、CAT1 不能动，没有可消化的格子 → 拒绝。
	if _, err := p.Submit(scopeSKU3Day1, 100); err != pa.ErrInfeasible {
		t.Fatalf("want ErrInfeasible, got %v", err)
	}
	mustVal(t, p, "SKU3", 1, 180)
	mustSum(t, p, scopeAllDay1, 300)

	// SKU3 改成 180：增减量为 0，可行。
	if _, err := p.Submit(scopeSKU3Day1, 180); err != nil {
		t.Fatal(err)
	}
	mustSum(t, p, scopeAllDay1, 300)
}

// 目标小于原样不动部分合计 → 拒绝，且一切不变。
func TestRejectTargetBelowFrozen(t *testing.T) {
	p, c := newTestPlanner(t)
	setForecast(t, p, c, []int64{10, 20, 30})
	if _, err := p.Submit(scopeSKU1Day1, 100); err != nil {
		t.Fatal(err)
	}
	before := p.Adjustments()
	if _, err := p.Submit(scopeCat1Day1, 50); err != pa.ErrInfeasible {
		t.Fatalf("want ErrInfeasible, got %v", err)
	}
	after := p.Adjustments()
	if len(before) != len(after) || before[0] != after[0] {
		t.Fatalf("rejected submit changed adjustments")
	}
	mustVal(t, p, "SKU1", 1, 100)
	mustVal(t, p, "SKU2", 1, 20)
}

// 同范围取代、相交作废。
func TestReplaceAndVoid(t *testing.T) {
	p, c := newTestPlanner(t)
	setForecast(t, p, c, []int64{10, 20, 30})

	id1, _ := p.Submit(scopeCat1Day1, 100)
	mustSum(t, p, scopeCat1Day1, 100)
	// 同范围：取代。
	id2, _ := p.Submit(scopeCat1Day1, 200)
	mustStatus(t, p, id1, pa.StatusReplaced)
	mustSum(t, p, scopeCat1Day1, 200)

	// 相交但互不包含：(CAT1,day1) 与 (SKU1,period) → 旧的作废。
	id3, _ := p.Submit(pa.Scope{Product: pa.ProductSKU, SKU: "SKU1", Time: pa.TimePeriod}, 91)
	mustStatus(t, p, id2, pa.StatusVoided)
	mustStatus(t, p, id3, pa.StatusActive)
	mustSum(t, p, pa.Scope{Product: pa.ProductSKU, SKU: "SKU1", Time: pa.TimePeriod}, 91)
}

// 同范围取代时若新调整自身不可满足，则拒绝且旧调整不被取代。
func TestReplaceRejectedKeepsOld(t *testing.T) {
	p, c := newTestPlanner(t)
	setForecast(t, p, c, []int64{0, 0, 0})

	idA, _ := p.Submit(scopeCat1Day1, 1000) // SKU1=500, SKU2=500（平均分）
	idB, _ := p.Submit(scopeSKU1Day1, 900)  // SKU2 消化 −400 → 100
	mustVal(t, p, "SKU2", 1, 100)

	// 同范围取代 A，但新目标 50 < 被包含的 B 的 900 → 新调整不可满足，拒绝。
	if _, err := p.Submit(scopeCat1Day1, 50); err != pa.ErrInfeasible {
		t.Fatalf("want ErrInfeasible, got %v", err)
	}
	mustStatus(t, p, idA, pa.StatusActive)
	mustStatus(t, p, idB, pa.StatusActive)
	mustSum(t, p, scopeCat1Day1, 1000)
	mustVal(t, p, "SKU1", 1, 900)
	mustVal(t, p, "SKU2", 1, 100)
}

// 新调整不可满足时，本该被它作废的交叉调整也不作废。
func TestRejectedSubmitDoesNotVoid(t *testing.T) {
	p, c := newTestPlanner(t)
	setForecast(t, p, c, []int64{0, 0, 5000})

	idZ, _ := p.Submit(scopeAllAll, 2000)   // 只有 SKU3 day1 非零 → 2000 全给它
	idA, _ := p.Submit(scopeCat1Day1, 1000) // SKU1=SKU2=500；SKU3 day1 消化 −1000 → 1000
	idB, _ := p.Submit(scopeSKU1Day1, 900)  // SKU2 消化 −400 → 100
	mustVal(t, p, "SKU3", 1, 1000)

	// (SKU2, week1) 与 (CAT1, day1) 相交互不包含，本该作废 A；
	// 但目标 5000 需要最近包含者 Z 消化 +5000，Z 里可动的只有 SKU3 day1 的 1000 → 拒绝。
	cross := pa.Scope{Product: pa.ProductSKU, SKU: "SKU2", Time: pa.TimeWeek, Week: 1}
	if _, err := p.Submit(cross, 5000); err != pa.ErrInfeasible {
		t.Fatalf("want ErrInfeasible, got %v", err)
	}
	mustStatus(t, p, idZ, pa.StatusActive)
	mustStatus(t, p, idA, pa.StatusActive)
	mustStatus(t, p, idB, pa.StatusActive)
	mustSum(t, p, scopeCat1Day1, 1000)
	mustVal(t, p, "SKU1", 1, 900)
	mustVal(t, p, "SKU2", 1, 100)
	mustVal(t, p, "SKU3", 1, 1000)
}

// 全 0 时平均分摊，合计分毫不差。
func TestAverageSplitOnZeros(t *testing.T) {
	p, c := newTestPlanner(t)
	setForecast(t, p, c, []int64{0, 0, 0})
	if _, err := p.Submit(scopeAllAll, 1000); err != nil {
		t.Fatal(err)
	}
	mustSum(t, p, scopeAllAll, 1000)
	cells := int64(len(c.skus) * pa.Days) // 273
	base, extra := 1000/cells, 1000%cells
	var cntBase, cntCeil int64
	for _, sku := range c.skus {
		for d := 1; d <= pa.Days; d++ {
			v, _ := p.PlanValue(sku, d)
			switch v {
			case base:
				cntBase++
			case base + 1:
				cntCeil++
			default:
				t.Fatalf("cell %s day %d = %d, not floor/ceil of average", sku, d, v)
			}
		}
	}
	if cntCeil != extra || cntBase != cells-extra {
		t.Fatalf("ceil cells %d want %d", cntCeil, extra)
	}
}

// 撤销后按剩余生效调整重放。
func TestUndoReplays(t *testing.T) {
	p, c := newTestPlanner(t)
	setForecast(t, p, c, []int64{10, 20, 30})
	idA, _ := p.Submit(scopeSKU1Day1, 50)
	idB, _ := p.Submit(scopeCat1Day1, 120)
	mustVal(t, p, "SKU2", 1, 70)
	if err := p.Undo(idB); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, p, idB, pa.StatusUndone)
	mustStatus(t, p, idA, pa.StatusActive)
	// B 撤销后：SKU1 仍 50（A 生效），SKU2 回到预测 20。
	mustVal(t, p, "SKU1", 1, 50)
	mustVal(t, p, "SKU2", 1, 20)
	if err := p.Undo(idB); err != pa.ErrNotActive {
		t.Fatalf("want ErrNotActive, got %v", err)
	}
}
