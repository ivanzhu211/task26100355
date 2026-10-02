package planadjust_test

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"

	pa "github.com/example/planadjust"
)

type catalog struct {
	skus    []string
	cats    []string
	skuCat  []int
	catSKUs [][]int
}

func makeCatalog(rng *rand.Rand, nSKU, nCat int) (catalog, []pa.SKUDef) {
	c := catalog{skuCat: make([]int, nSKU), catSKUs: make([][]int, nCat)}
	defs := make([]pa.SKUDef, 0, nSKU)
	for i := 0; i < nCat; i++ {
		c.cats = append(c.cats, fmt.Sprintf("CAT%d", i))
	}
	for i := 0; i < nSKU; i++ {
		sku := fmt.Sprintf("SKU%04d", i)
		cat := rng.Intn(nCat)
		c.skus = append(c.skus, sku)
		c.skuCat[i] = cat
		c.catSKUs[cat] = append(c.catSKUs[cat], i)
		defs = append(defs, pa.SKUDef{SKU: sku, Category: c.cats[cat]})
	}
	return c, defs
}

func (c catalog) toRef(sc pa.Scope) refScope {
	r := refScope{skus: map[int]bool{}}
	switch sc.Product {
	case pa.ProductAll:
		for i := range c.skus {
			r.skus[i] = true
		}
	case pa.ProductCategory:
		for _, k := range c.catSKUs[indexOf(c.cats, sc.Category)] {
			r.skus[k] = true
		}
	case pa.ProductSKU:
		r.skus[indexOf(c.skus, sc.SKU)] = true
	}
	switch sc.Time {
	case pa.TimePeriod:
		r.d0, r.d1 = 0, pa.Days
	case pa.TimeWeek:
		r.d0, r.d1 = (sc.Week-1)*pa.DaysPerWeek, sc.Week*pa.DaysPerWeek
	case pa.TimeDay:
		r.d0, r.d1 = sc.Day-1, sc.Day
	}
	return r
}

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}

func randScope(rng *rand.Rand, c catalog) pa.Scope {
	var sc pa.Scope
	switch rng.Intn(10) {
	case 0, 1:
		sc.Product = pa.ProductAll
	case 2, 3, 4:
		sc.Product = pa.ProductCategory
		sc.Category = c.cats[rng.Intn(len(c.cats))]
	default:
		sc.Product = pa.ProductSKU
		sc.SKU = c.skus[rng.Intn(len(c.skus))]
	}
	switch rng.Intn(10) {
	case 0, 1:
		sc.Time = pa.TimePeriod
	case 2, 3, 4:
		sc.Time = pa.TimeWeek
		sc.Week = 1 + rng.Intn(pa.Weeks)
	default:
		sc.Time = pa.TimeDay
		sc.Day = 1 + rng.Intn(pa.Days)
	}
	return sc
}

func randForecast(rng *rand.Rand, c catalog, maxVal int) (map[string][pa.Days]int64, []int64) {
	m := make(map[string][pa.Days]int64, len(c.skus))
	flat := make([]int64, len(c.skus)*pa.Days)
	for i, sku := range c.skus {
		var days [pa.Days]int64
		for d := range days {
			days[d] = int64(rng.Intn(maxVal + 1))
			flat[i*pa.Days+d] = days[d]
		}
		m[sku] = days
	}
	return m, flat
}

func sumScopeFlat(grid []int64, sc refScope) int64 {
	var s int64
	for _, cell := range sc.cells() {
		s += grid[cell]
	}
	return s
}

// checkConsistency 校验：计划值与参考逐格一致；生效调整范围合计等于目标；
// 所有调整状态与参考一致；每格非负。
func checkConsistency(t *testing.T, p *pa.Planner, rs *refState, c catalog, step string) {
	t.Helper()
	adjs := p.Adjustments()
	if len(adjs) != len(rs.all) {
		t.Fatalf("%s: adjustment count %d != ref %d", step, len(adjs), len(rs.all))
	}
	for i, a := range adjs {
		if a.Status != rs.all[i].status {
			t.Fatalf("%s: adj %d status %v != ref %v", step, a.ID, a.Status, rs.all[i].status)
		}
		if a.Status == pa.StatusActive {
			got, err := p.PlanSum(a.Scope)
			if err != nil {
				t.Fatalf("%s: PlanSum: %v", step, err)
			}
			if got != a.Target {
				t.Fatalf("%s: active adj %d scope sum %d != target %d", step, a.ID, got, a.Target)
			}
		}
	}
	for k, sku := range c.skus {
		for d := 1; d <= pa.Days; d++ {
			got, err := p.PlanValue(sku, d)
			if err != nil {
				t.Fatalf("%s: PlanValue: %v", step, err)
			}
			want := rs.grid[k*pa.Days+d-1]
			if got != want {
				t.Fatalf("%s: plan[%s][%d]=%d want %d", step, sku, d, got, want)
			}
			if got < 0 {
				t.Fatalf("%s: negative cell", step)
			}
		}
	}
}

func TestBehaviorSequence(t *testing.T) {
	for _, seed := range []int64{42, 1, 2, 3, 99, 12345} {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			runBehaviorSequence(t, seed)
		})
	}
}

func runBehaviorSequence(t *testing.T, seed int64) {
	rng := rand.New(rand.NewSource(seed))
	c, defs := makeCatalog(rng, 20, 3)
	dir := t.TempDir()
	p, err := pa.Create(filepath.Join(dir, "test.db"), pa.Config{StoreID: "s1", SKUs: defs})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	fc, flat := randForecast(rng, c, 50)
	if err := p.ReplaceForecast(fc); err != nil {
		t.Fatal(err)
	}
	rs := &refState{nSKU: len(c.skus), forecast: flat, grid: append([]int64{}, flat...)}
	checkConsistency(t, p, rs, c, "init")

	for step := 0; step < 400; step++ {
		label := fmt.Sprintf("step %d", step)
		op := rng.Intn(100)
		switch {
		case op < 65: // 提交调整
			sc := randScope(rng, c)
			rsScope := c.toRef(sc)
			var target int64
			switch rng.Intn(10) {
			case 0, 1:
				target = 0
			case 2, 3, 4: // 在当前合计附近微调
				cur := sumScopeFlat(rs.grid, rsScope)
				target = max(0, cur+int64(rng.Intn(201))-100)
			case 5: // 故意不可满足：包住一条生效调整但目标比它小
				if act := rs.active(); len(act) > 0 {
					victim := act[rng.Intn(len(act))]
					big := pa.Scope{Product: pa.ProductAll, Time: pa.TimePeriod}
					if !c.toRef(big).equal(victim.sc) {
						sc, rsScope = big, c.toRef(big)
						target = max(0, victim.target-1)
						break
					}
				}
				target = int64(rng.Intn(2000))
			default:
				target = int64(rng.Intn(2000))
			}
			// 快照，用于拒绝后逐项比对
			beforeAdjs := p.Adjustments()
			beforeSum, _ := p.PlanSum(pa.Scope{Product: pa.ProductAll, Time: pa.TimePeriod})
			id, err := p.Submit(sc, target)
			accepted := rs.refSubmit(rsScope, target)
			if accepted != (err == nil) {
				t.Fatalf("%s: planner accepted=%v (err=%v), ref accepted=%v, scope=%+v target=%d",
					label, err == nil, err, accepted, sc, target)
			}
			if err == nil && id != int64(len(rs.all)) {
				t.Fatalf("%s: id %d != ref %d", label, id, len(rs.all))
			}
			if !accepted {
				afterAdjs := p.Adjustments()
				if len(beforeAdjs) != len(afterAdjs) {
					t.Fatalf("%s: rejected submit changed adjustment count", label)
				}
				for i := range beforeAdjs {
					if beforeAdjs[i] != afterAdjs[i] {
						t.Fatalf("%s: rejected submit changed adj %d", label, beforeAdjs[i].ID)
					}
				}
				afterSum, _ := p.PlanSum(pa.Scope{Product: pa.ProductAll, Time: pa.TimePeriod})
				if beforeSum != afterSum {
					t.Fatalf("%s: rejected submit changed plan", label)
				}
			}
		case op < 80: // 撤销
			var id int64 = -1
			if len(rs.all) > 0 {
				id = rs.all[rng.Intn(len(rs.all))].id
			}
			err := p.Undo(id)
			ok := rs.refUndo(id)
			if ok != (err == nil) {
				t.Fatalf("%s: undo(%d) planner err=%v ref ok=%v", label, id, err, ok)
			}
		default: // 更换统计预测
			fc, flat := randForecast(rng, c, 50)
			if err := p.ReplaceForecast(fc); err != nil {
				t.Fatalf("%s: ReplaceForecast: %v", label, err)
			}
			rs.refReplaceForecast(flat)
		}
		checkConsistency(t, p, rs, c, label)
	}
}

func TestPersistenceAcrossRestart(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	c, defs := makeCatalog(rng, 30, 4)
	path := filepath.Join(t.TempDir(), "restart.db")
	p, err := pa.Create(path, pa.Config{StoreID: "s1", SKUs: defs})
	if err != nil {
		t.Fatal(err)
	}
	fc, flat := randForecast(rng, c, 30)
	if err := p.ReplaceForecast(fc); err != nil {
		t.Fatal(err)
	}
	rs := &refState{nSKU: len(c.skus), forecast: flat, grid: append([]int64{}, flat...)}
	for i := 0; i < 120; i++ {
		sc := randScope(rng, c)
		target := int64(rng.Intn(3000))
		if _, err := p.Submit(sc, target); err == nil {
			if !rs.refSubmit(c.toRef(sc), target) {
				t.Fatalf("step %d: planner accepted but ref rejected", i)
			}
		} else if rs.refSubmit(c.toRef(sc), target) {
			t.Fatalf("step %d: planner rejected but ref accepted", i)
		}
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p2, err := pa.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p2.Close()
	checkConsistency(t, p2, rs, c, "after restart")
}

func TestErrors(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	_, defs := makeCatalog(rng, 5, 2)
	p, err := pa.Create(filepath.Join(t.TempDir(), "err.db"), pa.Config{StoreID: "s1", SKUs: defs})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := p.Submit(pa.Scope{Product: pa.ProductAll, Time: pa.TimePeriod}, -1); err != pa.ErrInvalidTarget {
		t.Fatalf("want ErrInvalidTarget, got %v", err)
	}
	if _, err := p.Submit(pa.Scope{Product: pa.ProductSKU, SKU: "NOPE", Time: pa.TimePeriod}, 1); err != pa.ErrInvalidScope {
		t.Fatalf("want ErrInvalidScope, got %v", err)
	}
	if _, err := p.Submit(pa.Scope{Product: pa.ProductAll, Time: pa.TimeWeek, Week: 14}, 1); err != pa.ErrInvalidScope {
		t.Fatalf("want ErrInvalidScope, got %v", err)
	}
	if err := p.Undo(999); err != pa.ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if _, err := pa.Create(filepath.Join(t.TempDir(), "empty.db"), pa.Config{}); err == nil {
		t.Fatalf("want error for empty SKU list")
	}
	// 重复 Create 同一数据库 → ErrExists；Open 正常。
	path := filepath.Join(t.TempDir(), "dup.db")
	p1, err := pa.Create(path, pa.Config{StoreID: "s", SKUs: defs})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pa.Create(path, pa.Config{StoreID: "s", SKUs: defs}); err != pa.ErrExists {
		t.Fatalf("want ErrExists, got %v", err)
	}
	p1.Close()
	p2, err := pa.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p2.Close()
}
