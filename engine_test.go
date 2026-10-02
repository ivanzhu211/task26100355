package planadjust

import "testing"

// 小布局：3 个 SKU，2 个品类。
func engLayout() *layout {
	return testLayout()
}

// 全 1 的统计预测。
func onesForecast(l *layout) *grid {
	g := newGrid(l)
	for i := range g.values {
		g.setCell(i, 1)
	}
	return g
}

func mkAdj(seq int64, scope Scope, target int64) *Adjustment {
	return &Adjustment{ID: seq, Seq: seq, Scope: scope, Target: target, Status: StatusActive}
}

func allPeriod() Scope { return Scope{ps(ProductAll, "", ""), ts(TimePeriod, 0, 0)} }
func catPeriod(c string) Scope {
	return Scope{ps(ProductCategory, c, ""), ts(TimePeriod, 0, 0)}
}
func skuPeriod(s string) Scope { return Scope{ps(ProductSKU, "", s), ts(TimePeriod, 0, 0)} }
func allWeek(w int) Scope      { return Scope{ps(ProductAll, "", ""), ts(TimeWeek, w, 0)} }
func skuDay(s string, d int) Scope {
	return Scope{ps(ProductSKU, "", s), ts(TimeDay, 0, d)}
}

func planSum(e *engine, s Scope) int64 { return e.plan.sum(e.l, s) }

func TestDistributeExact(t *testing.T) {
	l := engLayout()
	e := newEngine(l, onesForecast(l))
	// 全店全期 273 格（3*91），目标 1000。
	if _, _, _, err := e.submit(mkAdj(1, allPeriod(), 1000)); err != nil {
		t.Fatal(err)
	}
	if got := planSum(e, allPeriod()); got != 1000 {
		t.Fatalf("sum = %d, want 1000", got)
	}
	// 每格必须是精确份额 1000/273≈3.66 的下或上取整：3 或 4。
	for i, v := range e.plan.values {
		if v != 3 && v != 4 {
			t.Fatalf("cell %d = %d, want 3 or 4", i, v)
		}
	}
}

func TestNestedAbsorption(t *testing.T) {
	l := engLayout()
	e := newEngine(l, onesForecast(l))
	// 全店全期 = 273（与预测一致），再定 catA 全期 = 200（原 182，+18 由 catB 消化）。
	if _, _, _, err := e.submit(mkAdj(1, allPeriod(), 273)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := e.submit(mkAdj(2, catPeriod("catA"), 200)); err != nil {
		t.Fatal(err)
	}
	// catA 合计 200；全店合计不变 273（catB 被反向消化 18）。
	if got := planSum(e, catPeriod("catA")); got != 200 {
		t.Fatalf("catA sum = %d, want 200", got)
	}
	if got := planSum(e, allPeriod()); got != 273 {
		t.Fatalf("total = %d, want 273", got)
	}
	if got := planSum(e, catPeriod("catB")); got != 73 {
		t.Fatalf("catB sum = %d, want 73", got)
	}
}

func TestFrozenChild(t *testing.T) {
	l := engLayout()
	e := newEngine(l, onesForecast(l))
	// 先定 SKU A1 全期 = 50，再定 catA 全期 = 200：A1 的格子原样不动。
	if _, _, _, err := e.submit(mkAdj(1, skuPeriod("A1"), 50)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := e.submit(mkAdj(2, catPeriod("catA"), 200)); err != nil {
		t.Fatal(err)
	}
	if got := planSum(e, skuPeriod("A1")); got != 50 {
		t.Fatalf("A1 sum = %d, want 50", got)
	}
	if got := planSum(e, catPeriod("catA")); got != 200 {
		t.Fatalf("catA sum = %d, want 200", got)
	}
	if got := planSum(e, skuPeriod("A2")); got != 150 {
		t.Fatalf("A2 sum = %d, want 150", got)
	}
}

func TestCrossVoids(t *testing.T) {
	l := engLayout()
	e := newEngine(l, onesForecast(l))
	// catA 第1周 与 SKU A1 全期 相交但互不包含 → 旧调整作废。
	if _, _, _, err := e.submit(mkAdj(1, Scope{ps(ProductCategory, "catA", ""), ts(TimeWeek, 1, 0)}, 100)); err != nil {
		t.Fatal(err)
	}
	voided, _, _, err := e.submit(mkAdj(2, skuPeriod("A1"), 200))
	if err != nil {
		t.Fatal(err)
	}
	if len(voided) != 1 || voided[0] != 1 {
		t.Fatalf("voided = %v, want [1]", voided)
	}
	if len(e.acts) != 1 || e.acts[0].ID != 2 {
		t.Fatalf("acts = %v, want only adj 2", e.acts)
	}
	if got := planSum(e, skuPeriod("A1")); got != 200 {
		t.Fatalf("A1 sum = %d, want 200", got)
	}
}

func TestSupersede(t *testing.T) {
	l := engLayout()
	e := newEngine(l, onesForecast(l))
	if _, _, _, err := e.submit(mkAdj(1, allWeek(2), 70)); err != nil {
		t.Fatal(err)
	}
	_, sup, _, err := e.submit(mkAdj(2, allWeek(2), 100))
	if err != nil {
		t.Fatal(err)
	}
	if len(sup) != 1 || sup[0] != 1 {
		t.Fatalf("superseded = %v, want [1]", sup)
	}
	if got := planSum(e, allWeek(2)); got != 100 {
		t.Fatalf("week2 sum = %d, want 100", got)
	}
}

func TestInfeasibleTargetBelowFrozen(t *testing.T) {
	l := engLayout()
	e := newEngine(l, onesForecast(l))
	// SKU A1 全期 = 50 后，catA 全期目标 40 < 50 → 拒绝。
	if _, _, _, err := e.submit(mkAdj(1, skuPeriod("A1"), 50)); err != nil {
		t.Fatal(err)
	}
	before := planSum(e, allPeriod())
	if _, _, _, err := e.submit(mkAdj(2, catPeriod("catA"), 40)); err != ErrInfeasible {
		t.Fatalf("err = %v, want ErrInfeasible", err)
	}
	if got := planSum(e, allPeriod()); got != before {
		t.Fatalf("plan changed after rejection: %d -> %d", before, got)
	}
	if len(e.acts) != 1 {
		t.Fatalf("acts len = %d, want 1", len(e.acts))
	}
}

func TestInfeasibleAbsorption(t *testing.T) {
	l := engLayout()
	// 预测全 0 的 catB：全店 = 182（catA 各格 2），catA 提到 400 时 catB 无法消化。
	g := newGrid(l)
	for r := 0; r < 2; r++ { // catA 两个 SKU
		for d := 0; d < Days; d++ {
			g.setCell(r*Days+d, 2)
		}
	}
	e := newEngine(l, g)
	if _, _, _, err := e.submit(mkAdj(1, allPeriod(), 364)); err != nil {
		t.Fatal(err)
	}
	// catA 现合计 364，全店 364。把 catA 提到 500：需从 catB 消化 136，但 catB 全为 0。
	if _, _, _, err := e.submit(mkAdj(2, catPeriod("catA"), 500)); err != ErrInfeasible {
		t.Fatalf("err = %v, want ErrInfeasible", err)
	}
}

func TestZeroCellsEqualSplit(t *testing.T) {
	l := engLayout()
	e := newEngine(l, newGrid(l)) // 预测全 0
	// 全 0 时平均分：10 分到 273 格不行？273 格分 10 → 前 10 格各 1。
	if _, _, _, err := e.submit(mkAdj(1, allPeriod(), 10)); err != nil {
		t.Fatal(err)
	}
	if got := planSum(e, allPeriod()); got != 10 {
		t.Fatalf("sum = %d, want 10", got)
	}
	ones := 0
	for _, v := range e.plan.values {
		if v == 1 {
			ones++
		} else if v != 0 {
			t.Fatalf("unexpected cell value %d", v)
		}
	}
	if ones != 10 {
		t.Fatalf("ones = %d, want 10", ones)
	}
}

func TestWithdrawReplay(t *testing.T) {
	l := engLayout()
	e := newEngine(l, onesForecast(l))
	e.submit(mkAdj(1, allPeriod(), 273))
	if _, _, _, err := e.submit(mkAdj(2, catPeriod("catA"), 200)); err != nil {
		t.Fatal(err)
	}
	// 撤销全店调整：catA 仍 200，catB 回到预测值 91。
	if _, err := e.withdraw(1); err != nil {
		t.Fatal(err)
	}
	if got := planSum(e, catPeriod("catA")); got != 200 {
		t.Fatalf("catA = %d, want 200", got)
	}
	if got := planSum(e, catPeriod("catB")); got != 91 {
		t.Fatalf("catB = %d, want 91", got)
	}
}

func TestConflictOnReplay(t *testing.T) {
	l := engLayout()
	e := newEngine(l, onesForecast(l))
	// SKU A1 全期 = 50；换预测后 A1 全 0，catA 目标 40 < 50 → catA 冲突。
	e.submit(mkAdj(1, skuPeriod("A1"), 50))
	e.submit(mkAdj(2, catPeriod("catA"), 200))
	// 新预测：A1 全 0，其余全 1。A1=50 仍可满足（全 0 平均分）。
	// 再构造：A1 被 SKU 级调整钉死 50，catA 调整目标 40 会在重放中冲突。
	e2 := newEngine(l, onesForecast(l))
	e2.submit(mkAdj(1, skuPeriod("A1"), 50))
	e2.submit(mkAdj(2, catPeriod("catA"), 200))
	// 直接把 catA 目标改成 40 重放（模拟旧调整重算冲突）。
	e2.acts[1].Target = 40
	conflicts := e2.replay()
	if len(conflicts) != 1 || conflicts[0] != 2 {
		t.Fatalf("conflicts = %v, want [2]", conflicts)
	}
	if len(e2.acts) != 1 || e2.acts[0].ID != 1 {
		t.Fatalf("acts = %v, want only adj 1", e2.acts)
	}
	if got := planSum(e2, skuPeriod("A1")); got != 50 {
		t.Fatalf("A1 = %d, want 50", got)
	}
}

// 嵌套调低时，包含者范围外的格子被反向加餐，包含者合计不变。
func TestEncloserBoostOnDecrease(t *testing.T) {
	l := engLayout()
	e := newEngine(l, onesForecast(l))
	// catA 全期 = 273（3 SKU × 91 天，与预测一致）。
	if _, _, _, err := e.submit(mkAdj(1, catPeriod("catA"), 273)); err != nil {
		t.Fatal(err)
	}
	// SKU A1 全期 = 10：A1 行从 91 降到 10，减少 81 由 catA 内其他格子消化。
	if _, _, _, err := e.submit(mkAdj(2, skuPeriod("A1"), 10)); err != nil {
		t.Fatal(err)
	}
	if got := planSum(e, catPeriod("catA")); got != 273 {
		t.Fatalf("catA sum = %d, want 273", got)
	}
	if got := planSum(e, skuPeriod("A1")); got != 10 {
		t.Fatalf("A1 sum = %d, want 10", got)
	}
	if got := planSum(e, skuPeriod("A2")) + planSum(e, skuPeriod("A3")); got != 263 {
		t.Fatalf("A2+A3 sum = %d, want 263", got)
	}
	for _, v := range e.plan.values {
		if v < 0 {
			t.Fatalf("negative cell %d", v)
		}
	}
}
