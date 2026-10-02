package planadjust

import (
	"path/filepath"
	"testing"
	"time"
)

func TestNotInitialized(t *testing.T) {
	svc, err := Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if _, err := svc.Submit(Scope{ProductScope{Kind: ProductAll}, TimeScope{Kind: TimePeriod}}, 1); err != ErrNotInitialized {
		t.Fatalf("submit before init: %v", err)
	}
	if _, err := svc.SumPlan(Scope{ProductScope{Kind: ProductAll}, TimeScope{Kind: TimePeriod}}); err != ErrNotInitialized {
		t.Fatalf("SumPlan before init: %v", err)
	}
	if err := svc.ReplaceForecast(map[string][]int64{}); err != ErrNotInitialized {
		t.Fatalf("ReplaceForecast before init: %v", err)
	}
}

func TestDoubleInit(t *testing.T) {
	svc := newTestService(t)
	defer svc.Close()
	if err := svc.Init(time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), testSKUs); err == nil {
		t.Fatal("double init should fail")
	}
	// 非周一
	svc2, _ := Open(filepath.Join(t.TempDir(), "y.db"))
	defer svc2.Close()
	if err := svc2.Init(time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC), testSKUs); err == nil {
		t.Fatal("non-Monday start should fail")
	}
}

func TestWithdrawNonActive(t *testing.T) {
	svc := newTestService(t)
	defer svc.Close()
	if err := svc.Withdraw(999); err != ErrNotFound {
		t.Fatalf("withdraw missing: %v", err)
	}
	adj, err := svc.Submit(Scope{ProductScope{Kind: ProductAll}, TimeScope{Kind: TimePeriod}}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Withdraw(adj.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Withdraw(adj.ID); err == nil {
		t.Fatal("double withdraw should fail")
	}
	if got := svc.Adjustments()[0].Status; got != StatusWithdrawn {
		t.Fatalf("status = %s, want withdrawn", got)
	}
}

func TestSupersedeSameScope(t *testing.T) {
	svc := newTestService(t)
	defer svc.Close()
	f := make(map[string][]int64)
	for _, info := range testSKUs {
		vals := make([]int64, Days)
		for d := range vals {
			vals[d] = 2
		}
		f[info.SKU] = vals
	}
	svc.ReplaceForecast(f)
	sc := Scope{ProductScope{Kind: ProductCategory, Category: "catA"}, TimeScope{Kind: TimeWeek, Week: 3}}
	a1, err := svc.Submit(sc, 100)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := svc.Submit(sc, 200)
	if err != nil {
		t.Fatal(err)
	}
	if svc.find(a1.ID).Status != StatusSuperseded {
		t.Fatalf("old adjustment should be superseded, got %s", svc.find(a1.ID).Status)
	}
	if svc.find(a2.ID).Status != StatusActive {
		t.Fatalf("new adjustment should be active")
	}
	if got, _ := svc.SumPlan(sc); got != 200 {
		t.Fatalf("scope sum = %d, want 200", got)
	}
	checkInvariants(t, svc, "supersede")
}
