package planadjust

import (
	"math/rand"
	"path/filepath"
	"testing"
	"time"
)

var testSKUs = []SKUInfo{
	{SKU: "A1", Category: "catA"}, {SKU: "A2", Category: "catA"}, {SKU: "A3", Category: "catA"},
	{SKU: "B1", Category: "catB"}, {SKU: "B2", Category: "catB"},
	{SKU: "C1", Category: "catC"}, {SKU: "C2", Category: "catC"}, {SKU: "C3", Category: "catC"},
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	svc, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Init(time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), testSKUs); err != nil { // 周一
		t.Fatal(err)
	}
	return svc
}

func randForecast(r *rand.Rand, skus []SKUInfo, maxVal int64) map[string][]int64 {
	f := make(map[string][]int64, len(skus))
	for _, info := range skus {
		vals := make([]int64, Days)
		for d := range vals {
			vals[d] = r.Int63n(maxVal + 1)
		}
		f[info.SKU] = vals
	}
	return f
}

func randScope(r *rand.Rand) Scope {
	var p ProductScope
	switch r.Intn(3) {
	case 0:
		p = ProductScope{Kind: ProductAll}
	case 1:
		p = ProductScope{Kind: ProductCategory, Category: []string{"catA", "catB", "catC"}[r.Intn(3)]}
	default:
		p = ProductScope{Kind: ProductSKU, SKU: testSKUs[r.Intn(len(testSKUs))].SKU}
	}
	var ts TimeScope
	switch r.Intn(3) {
	case 0:
		ts = TimeScope{Kind: TimePeriod}
	case 1:
		ts = TimeScope{Kind: TimeWeek, Week: 1 + r.Intn(Weeks)}
	default:
		ts = TimeScope{Kind: TimeDay, Day: 1 + r.Intn(Days)}
	}
	return Scope{Product: p, Time: ts}
}

// snapshot 抓取计划值与全部调整状态。
func snapshot(t *testing.T, svc *Service) (cells map[[2]string]int64, statuses map[int64]Status) {
	t.Helper()
	cells = make(map[[2]string]int64)
	for _, info := range testSKUs {
		for d := 1; d <= Days; d++ {
			v, err := svc.PlanCell(info.SKU, d)
			if err != nil {
				t.Fatal(err)
			}
			cells[[2]string{info.SKU, string(rune(d))}] = v
		}
	}
	statuses = make(map[int64]Status)
	for _, a := range svc.Adjustments() {
		statuses[a.ID] = a.Status
	}
	return cells, statuses
}

// checkInvariants 校验验收要求的三条不变量。
func checkInvariants(t *testing.T, svc *Service, ctx string) {
	t.Helper()
	// 1. 所有生效调整的范围合计恰好等于目标值。
	for _, a := range svc.Adjustments() {
		if a.Status != StatusActive {
			continue
		}
		got, err := svc.SumPlan(a.Scope)
		if err != nil {
			t.Fatalf("%s: SumPlan: %v", ctx, err)
		}
		if got != a.Target {
			t.Fatalf("%s: active adj %d scope sum = %d, want %d", ctx, a.ID, got, a.Target)
		}
	}
	// 2. 每格都是非负整数。
	for _, info := range testSKUs {
		for d := 1; d <= Days; d++ {
			v, _ := svc.PlanCell(info.SKU, d)
			if v < 0 {
				t.Fatalf("%s: negative cell %s day %d = %d", ctx, info.SKU, d, v)
			}
		}
	}
	// 3. 计划值与从当前统计预测出发、按顺序重放生效调整的结果逐格一致。
	fresh := newEngine(svc.l, svc.eng.forecast.clone())
	for _, a := range svc.Adjustments() {
		if a.Status == StatusActive {
			fresh.acts = append(fresh.acts, a)
		}
	}
	fresh.replay()
	for i := range fresh.plan.values {
		if fresh.plan.values[i] != svc.eng.plan.values[i] {
			t.Fatalf("%s: plan cell %d = %d, replay says %d", ctx, i, svc.eng.plan.values[i], fresh.plan.values[i])
		}
	}
	// 4. 抽查若干范围的合计与逐格手算一致。
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 20; i++ {
		sc := randScope(r)
		var manual int64
		rows, _ := svc.l.skuRows(sc.Product)
		if rows == nil {
			rows = make([]int, len(testSKUs))
			for j := range rows {
				rows[j] = j
			}
		}
		for _, row := range rows {
			for _, d := range daysOf(sc.Time) {
				manual += svc.eng.plan.get(row, d)
			}
		}
		got, err := svc.SumPlan(sc)
		if err != nil {
			t.Fatalf("%s: SumPlan: %v", ctx, err)
		}
		if got != manual {
			t.Fatalf("%s: SumPlan(%+v) = %d, manual = %d", ctx, sc, got, manual)
		}
	}
}

func TestLongOperationSequence(t *testing.T) {
	svc := newTestService(t)
	defer svc.Close()
	r := rand.New(rand.NewSource(42))

	if err := svc.ReplaceForecast(randForecast(r, testSKUs, 20)); err != nil {
		t.Fatal(err)
	}
	checkInvariants(t, svc, "init")

	for op := 0; op < 600; op++ {
		ctx := "op " + itoa(op)
		switch r.Intn(100) {
		case 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14: // 15% 更换统计预测
			if err := svc.ReplaceForecast(randForecast(r, testSKUs, 20)); err != nil {
				t.Fatalf("%s: ReplaceForecast: %v", ctx, err)
			}
		case 15, 16, 17, 18, 19, 20, 21, 22, 23, 24: // 10% 撤销
			var active []int64
			for _, a := range svc.Adjustments() {
				if a.Status == StatusActive {
					active = append(active, a.ID)
				}
			}
			if len(active) > 0 {
				if err := svc.Withdraw(active[r.Intn(len(active))]); err != nil {
					t.Fatalf("%s: Withdraw: %v", ctx, err)
				}
			}
		default: // 75% 提交调整
			sc := randScope(r)
			var target int64
			switch r.Intn(10) {
			case 0:
				target = 0 // 容易触发不可满足/清零
			case 1:
				target = 5000 // 大目标
			default:
				target = r.Int63n(400)
			}
			beforeCells, beforeStatuses := snapshot(t, svc)
			_, err := svc.Submit(sc, target)
			if err == ErrInfeasible {
				// 拒绝不变性：计划值与所有调整状态逐项相同。
				afterCells, afterStatuses := snapshot(t, svc)
				for k, v := range beforeCells {
					if afterCells[k] != v {
						t.Fatalf("%s: rejected submit changed cell %v", ctx, k)
					}
				}
				for id, st := range beforeStatuses {
					if afterStatuses[id] != st {
						t.Fatalf("%s: rejected submit changed status of %d: %s -> %s", ctx, id, st, afterStatuses[id])
					}
				}
				if len(afterStatuses) != len(beforeStatuses) {
					t.Fatalf("%s: rejected submit added an adjustment", ctx)
				}
			} else if err != nil {
				t.Fatalf("%s: Submit: %v", ctx, err)
			}
		}
		checkInvariants(t, svc, ctx)
	}
}

func TestPersistenceAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	svc, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Init(time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), testSKUs); err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewSource(7))
	svc.ReplaceForecast(randForecast(r, testSKUs, 15))
	for i := 0; i < 60; i++ {
		svc.Submit(randScope(r), r.Int63n(300))
		if i%7 == 0 {
			svc.ReplaceForecast(randForecast(r, testSKUs, 15))
		}
	}
	beforeCells, beforeStatuses := snapshot(t, svc)
	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}

	svc2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer svc2.Close()
	afterCells, afterStatuses := snapshot(t, svc2)
	for k, v := range beforeCells {
		if afterCells[k] != v {
			t.Fatalf("cell %v changed across restart: %d -> %d", k, v, afterCells[k])
		}
	}
	if len(afterStatuses) != len(beforeStatuses) {
		t.Fatalf("adjustment count changed across restart")
	}
	for id, st := range beforeStatuses {
		if afterStatuses[id] != st {
			t.Fatalf("status of %d changed across restart: %s -> %s", id, st, afterStatuses[id])
		}
	}
	checkInvariants(t, svc2, "after restart")
}

func TestRejectCases(t *testing.T) {
	svc := newTestService(t)
	defer svc.Close()
	f := make(map[string][]int64)
	for _, info := range testSKUs {
		vals := make([]int64, Days)
		for d := range vals {
			vals[d] = 1
		}
		f[info.SKU] = vals
	}
	if err := svc.ReplaceForecast(f); err != nil {
		t.Fatal(err)
	}
	// 全店全期 = 728。
	if _, err := svc.Submit(Scope{ProductScope{Kind: ProductAll}, TimeScope{Kind: TimePeriod}}, 728); err != nil {
		t.Fatal(err)
	}
	// SKU A1 全期 = 91（钉死）。
	if _, err := svc.Submit(Scope{ProductScope{Kind: ProductSKU, SKU: "A1"}, TimeScope{Kind: TimePeriod}}, 91); err != nil {
		t.Fatal(err)
	}
	// catA 全期目标 50 < A1 的 91 → 拒绝。
	if _, err := svc.Submit(Scope{ProductScope{Kind: ProductCategory, Category: "catA"}, TimeScope{Kind: TimePeriod}}, 50); err != ErrInfeasible {
		t.Fatalf("want ErrInfeasible, got %v", err)
	}
	// 全店第 1 周 = 70（钉死 7*8=56 格中的 70），随后 catA 第 1 周目标 200：
	// catA 第 1 周可动格子 21 格现值约 21，要涨到 200，需全店第 1 周消化 +179，
	// 但可消化格子（catB/c 第 1 周）合计 35 < 179 → 拒绝。
	if _, err := svc.Submit(Scope{ProductScope{Kind: ProductAll}, TimeScope{Kind: TimeWeek, Week: 1}}, 70); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Submit(Scope{ProductScope{Kind: ProductCategory, Category: "catA"}, TimeScope{Kind: TimeWeek, Week: 1}}, 200); err != ErrInfeasible {
		t.Fatalf("want ErrInfeasible for absorption, got %v", err)
	}
	// 负目标 → ErrInvalid。
	if _, err := svc.Submit(Scope{ProductScope{Kind: ProductAll}, TimeScope{Kind: TimePeriod}}, -5); err == nil {
		t.Fatal("negative target should fail")
	}
	// 非法范围 → ErrInvalid。
	if _, err := svc.Submit(Scope{ProductScope{Kind: ProductSKU, SKU: "ZZ"}, TimeScope{Kind: TimePeriod}}, 5); err == nil {
		t.Fatal("unknown SKU should fail")
	}
	checkInvariants(t, svc, "reject cases")
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [8]byte
	p := len(buf)
	for i > 0 {
		p--
		buf[p] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[p:])
}
