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

func (f fakeQuotaResolver) ResolveEffectiveQuota(context.Context, model.UserID, model.OrgID) (*model.EffectiveQuota, model.Quota, error) {
	return f.quota, nil, f.err
}

func (f fakeQuotaResolver) ResolveEffectiveQuotaForApplication(context.Context, model.ApplicationID, model.OrgID) (*model.EffectiveQuota, model.Quota, error) {
	return f.quota, nil, f.err
}

func (f fakeQuotaResolver) ResolveTeamQuotas(context.Context, model.TeamID, model.UserID) (model.Quota, *model.EffectiveQuota, error) {
	return nil, nil, f.err
}

// fakeUsage answers SumQuotaCostSince with the amount registered for the period start.
type fakeUsage struct {
	port.UsageStore
	spent map[time.Time]int64
	calls int
	err   error
}

func (f *fakeUsage) SumQuotaCostSince(_ context.Context, _ model.QuotaScope, _ string, _ model.OrgID, since time.Time) (int64, error) {
	f.calls++
	return f.spent[since], f.err
}

func i64(v int64) *int64 { return &v }

func TestQuotaInfoResolver_Resolve(t *testing.T) {
	now := time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)
	usage := &fakeUsage{spent: map[time.Time]int64{
		model.StartOfDay(now):   25,
		model.StartOfMonth(now): 800,
	}}
	r := NewQuotaInfoResolver(fakeQuotaResolver{quota: &model.EffectiveQuota{
		DailyBudget:   i64(100),
		MonthlyBudget: i64(1000),
	}}, usage)
	r.now = func() time.Time { return now }

	info := r.Resolve(context.Background(), "u", "", "o", "")
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
	usage := &fakeUsage{spent: map[time.Time]int64{model.StartOfDay(now): 500}}
	r := NewQuotaInfoResolver(fakeQuotaResolver{quota: &model.EffectiveQuota{DailyBudget: i64(100)}}, usage)
	info := r.Resolve(context.Background(), "u", "", "o", "")
	if info == nil || info.DailyRemaining != 0 {
		t.Fatalf("expected remaining clamped to 0, got %+v", info)
	}
}

func TestQuotaInfoResolver_NilCases(t *testing.T) {
	usage := &fakeUsage{}
	if got := NewQuotaInfoResolver(fakeQuotaResolver{quota: &model.EffectiveQuota{}}, usage).Resolve(context.Background(), "u", "", "o", ""); got != nil {
		t.Errorf("no budget must yield nil, got %+v", got)
	}
	if got := NewQuotaInfoResolver(fakeQuotaResolver{err: errors.New("boom")}, usage).Resolve(context.Background(), "u", "", "o", ""); got != nil {
		t.Errorf("resolver error must yield nil, got %+v", got)
	}
	failing := &fakeUsage{err: errors.New("db down")}
	if got := NewQuotaInfoResolver(fakeQuotaResolver{quota: &model.EffectiveQuota{DailyBudget: i64(1)}}, failing).Resolve(context.Background(), "u", "", "o", ""); got != nil {
		t.Errorf("usage error must yield nil, got %+v", got)
	}
}

// appPathQuotaResolver gives distinct budgets to the user and application paths
// and records the member asked for the team levels.
type appPathQuotaResolver struct {
	member *model.UserID
}

func (f appPathQuotaResolver) ResolveEffectiveQuota(context.Context, model.UserID, model.OrgID) (*model.EffectiveQuota, model.Quota, error) {
	return &model.EffectiveQuota{DailyBudget: i64(10)}, nil, nil
}

func (f appPathQuotaResolver) ResolveEffectiveQuotaForApplication(context.Context, model.ApplicationID, model.OrgID) (*model.EffectiveQuota, model.Quota, error) {
	return &model.EffectiveQuota{DailyBudget: i64(500)}, nil, nil
}

func (f appPathQuotaResolver) ResolveTeamQuotas(_ context.Context, _ model.TeamID, userID model.UserID) (model.Quota, *model.EffectiveQuota, error) {
	*f.member = userID
	return nil, nil, nil
}

// An application request reports the application budget the enforcer checks,
// not the one of its shadow user, and has no member budget in its team.
func TestQuotaInfoResolver_Application(t *testing.T) {
	usage := &fakeUsage{spent: map[time.Time]int64{}}
	member := model.UserID("unset")
	r := NewQuotaInfoResolver(appPathQuotaResolver{member: &member}, usage)

	info := r.Resolve(context.Background(), "shadow", "app", "o", "team")
	if info == nil || info.DailyTotal != 500 {
		t.Fatalf("info = %+v, want the application daily budget 500", info)
	}
	if member != "" {
		t.Fatalf("team levels resolved for member %q, want none for an application", member)
	}

	info = r.Resolve(context.Background(), "u", "", "o", "team")
	if info == nil || info.DailyTotal != 10 {
		t.Fatalf("info = %+v, want the user daily budget 10", info)
	}
	if member != "u" {
		t.Fatalf("team levels resolved for member %q, want u", member)
	}
}
