package planadjust

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"
	"time"
)

// 满规模：5000 SKU、60 品类、45.5 万格、500 条同时生效调整。
func buildFullScaleService(t *testing.T) (*Service, string, []SKUInfo) {
	t.Helper()
	var skus []SKUInfo
	for c := 0; c < 60; c++ {
		for i := 0; i < 84; i++ {
			if len(skus) >= 5000 {
				break
			}
			skus = append(skus, SKUInfo{SKU: fmt.Sprintf("sku%05d", len(skus)), Category: fmt.Sprintf("cat%02d", c)})
		}
	}
	path := filepath.Join(t.TempDir(), "perf.db")
	svc, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Init(time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), skus); err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewSource(99))
	f := make(map[string][]int64, len(skus))
	for _, info := range skus {
		vals := make([]int64, Days)
		for d := range vals {
			vals[d] = r.Int63n(101)
		}
		f[info.SKU] = vals
	}
	if err := svc.ReplaceForecast(f); err != nil {
		t.Fatal(err)
	}
	return svc, path, skus
}

// 生成与现有生效调整保持嵌套/不相交（不会作废他人）的范围。
func laminarScopes(r *rand.Rand, skus []SKUInfo) []Scope {
	var scopes []Scope
	scopes = append(scopes, Scope{ProductScope{Kind: ProductAll}, TimeScope{Kind: TimePeriod}})
	for c := 0; c < 60; c++ {
		scopes = append(scopes, Scope{
			ProductScope{Kind: ProductCategory, Category: fmt.Sprintf("cat%02d", c)},
			TimeScope{Kind: TimePeriod},
		})
	}
	// 每品类 2 个不重叠的周级范围。
	for c := 0; c < 60; c++ {
		for _, w := range []int{1 + r.Intn(6), 7 + r.Intn(7)} {
			scopes = append(scopes, Scope{
				ProductScope{Kind: ProductCategory, Category: fmt.Sprintf("cat%02d", c)},
				TimeScope{Kind: TimeWeek, Week: w},
			})
		}
	}
	// SKU 级：SKU×周 与 SKU×天。
	for len(scopes) < 500 {
		sku := skus[r.Intn(len(skus))].SKU
		if r.Intn(2) == 0 {
			scopes = append(scopes, Scope{
				ProductScope{Kind: ProductSKU, SKU: sku},
				TimeScope{Kind: TimeWeek, Week: 1 + r.Intn(Weeks)},
			})
		} else {
			scopes = append(scopes, Scope{
				ProductScope{Kind: ProductSKU, SKU: sku},
				TimeScope{Kind: TimeDay, Day: 1 + r.Intn(Days)},
			})
		}
	}
	return scopes
}

func TestFullScalePerformance(t *testing.T) {
	svc, path, skus := buildFullScaleService(t)
	r := rand.New(rand.NewSource(7))
	scopes := laminarScopes(r, skus)

	// 1. 提交 500 条不会互相作废的调整，每条必须在 2 秒内生效。
	var maxSubmit time.Duration
	submitted := 0
	for _, sc := range scopes {
		cur, err := svc.SumPlan(sc)
		if err != nil {
			t.Fatal(err)
		}
		target := cur * int64(95+r.Intn(11)) / 100 // ±5%
		start := time.Now()
		_, err = svc.Submit(sc, target)
		if err == ErrInfeasible {
			if _, err = svc.Submit(sc, cur); err != nil { // 退化为保持现状
				t.Fatalf("neutral submit failed: %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
		if d := time.Since(start); d > maxSubmit {
			maxSubmit = d
		}
		submitted++
	}
	if submitted != 500 {
		t.Fatalf("submitted %d, want 500", submitted)
	}
	t.Logf("max submit latency: %v", maxSubmit)
	if maxSubmit > 2*time.Second {
		t.Fatalf("submit took %v, exceeds 2s", maxSubmit)
	}
	active := 0
	for _, a := range svc.Adjustments() {
		if a.Status == StatusActive {
			active++
		}
	}
	t.Logf("active adjustments: %d", active)

	// 2. 任意范围合计查询 50 毫秒内，且不随调整条数变慢。
	var maxQuery time.Duration
	for i := 0; i < 2000; i++ {
		sc := laminarScopes(r, skus)[r.Intn(500)]
		start := time.Now()
		if _, err := svc.SumPlan(sc); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.SumForecast(sc); err != nil {
			t.Fatal(err)
		}
		if d := time.Since(start); d > maxQuery {
			maxQuery = d
		}
	}
	t.Logf("max query latency: %v", maxQuery)
	if maxQuery > 50*time.Millisecond {
		t.Fatalf("query took %v, exceeds 50ms", maxQuery)
	}

	// 3. 撤销引起的重算 30 秒内。
	var firstActive int64
	for _, a := range svc.Adjustments() {
		if a.Status == StatusActive {
			firstActive = a.ID
			break
		}
	}
	start := time.Now()
	if err := svc.Withdraw(firstActive); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Fatalf("withdraw recompute took %v, exceeds 30s", d)
	} else {
		t.Logf("withdraw recompute: %v", d)
	}

	// 4. 更换统计预测引起的重算 30 秒内。
	f := make(map[string][]int64, len(skus))
	for _, info := range skus {
		vals := make([]int64, Days)
		for d := range vals {
			vals[d] = r.Int63n(101)
		}
		f[info.SKU] = vals
	}
	start = time.Now()
	if err := svc.ReplaceForecast(f); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Fatalf("forecast replace took %v, exceeds 30s", d)
	} else {
		t.Logf("forecast replace: %v", d)
	}

	// 5. 满规模下不变量仍成立。
	for _, a := range svc.Adjustments() {
		if a.Status != StatusActive {
			continue
		}
		got, err := svc.SumPlan(a.Scope)
		if err != nil {
			t.Fatal(err)
		}
		if got != a.Target {
			t.Fatalf("active adj %d sum = %d, want %d", a.ID, got, a.Target)
		}
	}
	for _, v := range svc.eng.plan.values {
		if v < 0 {
			t.Fatal("negative cell at full scale")
		}
	}
	fresh := newEngine(svc.l, svc.eng.forecast.clone())
	for _, a := range svc.Adjustments() {
		if a.Status == StatusActive {
			fresh.acts = append(fresh.acts, a)
		}
	}
	fresh.replay()
	for i := range fresh.plan.values {
		if fresh.plan.values[i] != svc.eng.plan.values[i] {
			t.Fatalf("plan diverges from replay at cell %d", i)
		}
	}

	// 6. 重启后状态一致。
	wantCells := make([]int64, len(svc.eng.plan.values))
	copy(wantCells, svc.eng.plan.values)
	wantStatus := make(map[int64]Status)
	for _, a := range svc.Adjustments() {
		wantStatus[a.ID] = a.Status
	}
	svc.Close()
	start = time.Now()
	svc2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer svc2.Close()
	t.Logf("reopen: %v", time.Since(start))
	for i, v := range svc2.eng.plan.values {
		if v != wantCells[i] {
			t.Fatalf("cell %d changed across restart", i)
		}
	}
	for _, a := range svc2.Adjustments() {
		if wantStatus[a.ID] != a.Status {
			t.Fatalf("status of %d changed across restart", a.ID)
		}
	}
}
