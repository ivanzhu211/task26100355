package planadjust_test

import (
	"math/rand"
	"path/filepath"
	"testing"
	"time"

	pa "github.com/example/planadjust"
)

// 满规模：5000 SKU、60 品类、45.5 万格子、500 条同时生效的调整，
// 其中 53 条覆盖整个品类或整个计划期/整周。
func TestFullScalePerformance(t *testing.T) {
	rng := rand.New(rand.NewSource(9))
	c, defs := makeCatalog(rng, 5000, 60)
	dbPath := filepath.Join(t.TempDir(), "perf.db")
	p, err := pa.Create(dbPath, pa.Config{StoreID: "big", SKUs: defs})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	fc, _ := randForecast(rng, c, 100)
	if err := p.ReplaceForecast(fc); err != nil {
		t.Fatal(err)
	}

	mustSubmit := func(sc pa.Scope, target int64) {
		t.Helper()
		if _, err := p.Submit(sc, target); err != nil {
			t.Fatalf("submit %+v=%d: %v", sc, target, err)
		}
	}
	sum := func(sc pa.Scope) int64 {
		t.Helper()
		s, err := p.PlanSum(sc)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	// 1 条全期 + 13 条整周 + 39 条品类周，共 53 条大范围调整。
	scopePeriod := pa.Scope{Product: pa.ProductAll, Time: pa.TimePeriod}
	mustSubmit(scopePeriod, sum(scopePeriod)*21/20)
	for w := 1; w <= pa.Weeks; w++ {
		sc := pa.Scope{Product: pa.ProductAll, Time: pa.TimeWeek, Week: w}
		target := sum(sc) * 19 / 20
		if w == pa.Weeks {
			// 其余 12 周冻结后，最后一周没有可反向消化的格子，增减量必须为 0。
			target = sum(sc)
		}
		mustSubmit(sc, target)
	}
	for cat := 0; cat < 3; cat++ {
		for w := 1; w <= pa.Weeks; w++ {
			sc := pa.Scope{Product: pa.ProductCategory, Category: c.cats[cat], Time: pa.TimeWeek, Week: w}
			mustSubmit(sc, sum(sc)*21/20)
		}
	}
	// 447 条 SKU 级单日调整（格子互不重复，避免同范围取代），合计 500 条生效。
	used := map[[2]int]bool{}
	for i := 0; i < 447; i++ {
		var sc pa.Scope
		var key [2]int
		for {
			key = [2]int{rng.Intn(len(c.skus)), rng.Intn(pa.Days)}
			if !used[key] {
				used[key] = true
				break
			}
		}
		sc = pa.Scope{Product: pa.ProductSKU, SKU: c.skus[key[0]], Time: pa.TimeDay, Day: key[1] + 1}
		mustSubmit(sc, sum(sc)+int64(50+rng.Intn(50)))
	}
	if n := len(p.Adjustments()); n != 500 {
		t.Fatalf("active adjustments = %d, want 500", n)
	}

	// 查询：任意范围合计 < 50ms，不随调整条数变慢。
	var maxQuery time.Duration
	for i := 0; i < 500; i++ {
		sc := randScope(rng, c)
		start := time.Now()
		if _, err := p.PlanSum(sc); err != nil {
			t.Fatal(err)
		}
		if _, err := p.ForecastSum(sc); err != nil {
			t.Fatal(err)
		}
		if d := time.Since(start); d > maxQuery {
			maxQuery = d
		}
	}
	if maxQuery > 50*time.Millisecond {
		t.Fatalf("query took %v > 50ms", maxQuery)
	}
	t.Logf("max query: %v", maxQuery)

	// 提交一条不会让其他调整作废的调整（大范围）：< 2s。
	bigScope := pa.Scope{Product: pa.ProductCategory, Category: c.cats[10], Time: pa.TimeWeek, Week: 5}
	start := time.Now()
	mustSubmit(bigScope, sum(bigScope)*21/20)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("fast submit took %v > 2s", d)
	} else {
		t.Logf("fast submit (category-week): %v", d)
	}

	// 撤销引起的全量重算：< 30s。
	var undoID int64 = 1
	start = time.Now()
	if err := p.Undo(undoID); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Fatalf("recompute after undo took %v > 30s", d)
	} else {
		t.Logf("recompute after undo (499 active): %v", d)
	}

	// 更换统计预测引起的全量重算：< 30s。
	fc2, _ := randForecast(rng, c, 100)
	start = time.Now()
	if err := p.ReplaceForecast(fc2); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Fatalf("recompute after forecast change took %v > 30s", d)
	} else {
		t.Logf("recompute after forecast change (499 active): %v", d)
	}

	// 重算后所有生效调整的范围合计仍恰好等于目标。
	for _, a := range p.Adjustments() {
		if a.Status != pa.StatusActive {
			continue
		}
		if got := sum(a.Scope); got != a.Target {
			t.Fatalf("adj %d: sum %d != target %d after recompute", a.ID, got, a.Target)
		}
	}

	// 重启后状态一致。
	before := p.Adjustments()
	totalBefore := sum(scopePeriod)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p2, err := pa.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer p2.Close()
	after := p2.Adjustments()
	if len(before) != len(after) {
		t.Fatalf("adjustment count changed after restart")
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("adj %d changed after restart", before[i].ID)
		}
	}
	if got, _ := p2.PlanSum(scopePeriod); got != totalBefore {
		t.Fatalf("plan total changed after restart: %d != %d", got, totalBefore)
	}
}
