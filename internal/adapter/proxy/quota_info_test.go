package proxy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

type fakeQuotaResolver struct {
	quota *model.EffectiveQuota
	err   error
}

func (f fakeQuotaResolver) ResolveEffectiveQuota(context.Context, model.UserID, model.OrgID) (*model.EffectiveQuota, error) {
	return f.quota, f.err
}

// fakeUsage answers SumCostSince with the amount registered for the period start.
type fakeUsage struct {
	port.UsageStore
	spent map[time.Time]int64
	calls int
	err   error
}

func (f *fakeUsage) SumCostSince(_ context.Context, _ model.UserID, _ model.OrgID, since time.Time) (int64, error) {
	f.calls++
	return f.spent[since], f.err
}

func i64(v int64) *int64 { return &v }

func TestQuotaInfoResolver_Resolve(t *testing.T) {
	now := time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)
	usage := &fakeUsage{spent: map[time.Time]int64{
		startOfDay(now):   25,
		startOfMonth(now): 800,
	}}
	r := NewQuotaInfoResolver(fakeQuotaResolver{quota: &model.EffectiveQuota{
		DailyBudget:   i64(100),
		MonthlyBudget: i64(1000),
	}}, usage)
	r.now = func() time.Time { return now }

	info := r.Resolve(context.Background(), "u", "o")
	if info == nil {
		t.Fatal("expected quota info")
	}
	if info.DailyTotal != 100 || info.DailyRemaining != 75 {
		t.Errorf("daily: got %v/%v", info.DailyRemaining, info.DailyTotal)
	}
	if info.MonthlyTotal != 1000 || info.MonthlyRemaining != 200 {
		t.Errorf("monthly: got %v/%v", info.MonthlyRemaining, info.MonthlyTotal)
	}
	if info.YearlyTotal != 0 || info.YearlyRemaining != 0 {
		t.Errorf("yearly must stay unlimited: %v/%v", info.YearlyRemaining, info.YearlyTotal)
	}
	if usage.calls != 2 {
		t.Errorf("only budgeted periods must be summed, got %d calls", usage.calls)
	}
}

func TestQuotaInfoResolver_OverspentClampsToZero(t *testing.T) {
	now := time.Now()
	usage := &fakeUsage{spent: map[time.Time]int64{startOfDay(now): 500}}
	r := NewQuotaInfoResolver(fakeQuotaResolver{quota: &model.EffectiveQuota{DailyBudget: i64(100)}}, usage)
	info := r.Resolve(context.Background(), "u", "o")
	if info == nil || info.DailyRemaining != 0 {
		t.Fatalf("expected remaining clamped to 0, got %+v", info)
	}
}

func TestQuotaInfoResolver_NilCases(t *testing.T) {
	usage := &fakeUsage{}
	if got := NewQuotaInfoResolver(fakeQuotaResolver{quota: &model.EffectiveQuota{}}, usage).Resolve(context.Background(), "u", "o"); got != nil {
		t.Errorf("no budget must yield nil, got %+v", got)
	}
	if got := NewQuotaInfoResolver(fakeQuotaResolver{err: errors.New("boom")}, usage).Resolve(context.Background(), "u", "o"); got != nil {
		t.Errorf("resolver error must yield nil, got %+v", got)
	}
	failing := &fakeUsage{err: errors.New("db down")}
	if got := NewQuotaInfoResolver(fakeQuotaResolver{quota: &model.EffectiveQuota{DailyBudget: i64(1)}}, failing).Resolve(context.Background(), "u", "o"); got != nil {
		t.Errorf("usage error must yield nil, got %+v", got)
	}
}
