package proxy

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/pkg/errors"
)

// fairShareUsageStore embeds the port.UsageStore interface (nil) and only
// implements the reads the rolling-window evaluator performs.
type fairShareUsageStore struct {
	port.UsageStore

	orgTokens int64
	orgValue  int64

	userTokens int64
	userValue  int64

	// otherActiveUsers is what the store reports once the caller is excluded.
	otherActiveUsers int64
	countErr         error

	countCalls   int
	excludedUser model.UserID
}

func (f *fairShareUsageStore) SumPlanUsageSince(_ context.Context, _ model.OrgID, _ model.ProviderID, _ time.Time) (int64, int64, error) {
	return f.orgTokens, f.orgValue, nil
}

func (f *fairShareUsageStore) SumUserPlanUsageSince(_ context.Context, _ model.UserID, _ model.OrgID, _ model.ProviderID, _ time.Time) (int64, int64, error) {
	return f.userTokens, f.userValue, nil
}

func (f *fairShareUsageStore) CountActivePlanUsersSince(_ context.Context, _ model.OrgID, _ model.ProviderID, _ time.Time, excludeUserID model.UserID) (int64, error) {
	f.countCalls++
	f.excludedUser = excludeUserID
	if f.countErr != nil {
		return 0, f.countErr
	}
	return f.otherActiveUsers, nil
}

func tokenConstraint(budget int64) model.PlanConstraint {
	return model.PlanConstraint{
		Kind:        model.ConstraintRollingWindow,
		Label:       "5h",
		Duration:    model.PlanDuration(5 * time.Hour),
		TokenBudget: &budget,
	}
}

func fairShareScope() planScope {
	return planScope{
		OrgID:       "org-1",
		ProviderID:  "prov-1",
		UserID:      "user-1",
		MemberCount: 20,
	}
}

func TestRollingWindow_QuietMembersWidenTheShare(t *testing.T) {
	// 3 users active out of 20 members. The static budget/members cap stopped a
	// user at 50 tokens; the shared allocation lets them keep going.
	store := &fairShareUsageStore{orgTokens: 300, userTokens: 100, otherActiveUsers: 2}
	ev := newRollingWindowEvaluator(store)

	_, denial, err := ev.Acquire(context.Background(), fairShareScope(), tokenConstraint(1000))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if denial != nil {
		t.Fatalf("request denied, want granted: %s", denial.Message)
	}
	if store.countCalls != 1 {
		t.Errorf("CountActivePlanUsersSince called %d times, want 1", store.countCalls)
	}
}

func TestRollingWindow_ShareStillBoundedByTheReserve(t *testing.T) {
	// Sole active user: they get the commons but not the reserve held for the
	// 19 members who have not shown up.
	store := &fairShareUsageStore{orgTokens: 900, userTokens: 900, otherActiveUsers: 0}
	ev := newRollingWindowEvaluator(store)

	_, denial, err := ev.Acquire(context.Background(), fairShareScope(), tokenConstraint(1000))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if denial == nil {
		t.Fatal("request granted, want denied by the reserve")
	}
	if !strings.Contains(denial.Message, "fair-share quota exceeded") {
		t.Errorf("message = %q, want a fair-share denial", denial.Message)
	}
	// The denial explains the allocation instead of just stating a number.
	if !strings.Contains(denial.Message, "members active") {
		t.Errorf("message = %q, want it to report how many members are active", denial.Message)
	}
}

func TestRollingWindow_CountFailureFallsBackToStaticShare(t *testing.T) {
	// The count is only an optimisation; losing it must narrow the allowance back
	// to budget/members, never widen it.
	store := &fairShareUsageStore{
		orgTokens:  300,
		userTokens: 60, // above the static 1000/20 = 50 share
		countErr:   errors.New("boom"),
	}
	ev := newRollingWindowEvaluator(store)

	_, denial, err := ev.Acquire(context.Background(), fairShareScope(), tokenConstraint(1000))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if denial == nil {
		t.Fatal("request granted, want the static share to still apply on count failure")
	}
	// The message must not present the fallback as a measurement: "20 of 20
	// members active" would send support looking for nineteen colleagues.
	if !strings.Contains(denial.Message, "active-user count unavailable") {
		t.Errorf("message = %q, want it to name the degraded count", denial.Message)
	}
	if strings.Contains(denial.Message, "members active") {
		t.Errorf("message = %q, want no active-member figure when the count failed", denial.Message)
	}
}

func TestRollingWindow_CallerIsExcludedFromTheCountThenAddedBack(t *testing.T) {
	// The caller competes for the budget whether or not their first request has
	// been recorded yet, so they are excluded from the count and added back —
	// counting them from a zero usage sum would miscount a user whose requests
	// were recorded with no billable token.
	store := &fairShareUsageStore{orgTokens: 300, userTokens: 0, otherActiveUsers: 2}
	ev := newRollingWindowEvaluator(store)

	_, denial, err := ev.Acquire(context.Background(), fairShareScope(), tokenConstraint(1000))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if denial != nil {
		t.Fatalf("first request of the window denied: %s", denial.Message)
	}
	if store.excludedUser != "user-1" {
		t.Errorf("excluded user = %q, want the caller", store.excludedUser)
	}
}

func TestRollingWindow_DenialReportsTheAllocationBasis(t *testing.T) {
	// 2 other active users + the caller, out of 20 members: the message must
	// report 3 of 20, never a count that exceeds the membership.
	store := &fairShareUsageStore{orgTokens: 900, userTokens: 300, otherActiveUsers: 2}
	ev := newRollingWindowEvaluator(store)

	_, denial, err := ev.Acquire(context.Background(), fairShareScope(), tokenConstraint(1000))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if denial == nil {
		t.Fatal("request granted, want denied")
	}
	if !strings.Contains(denial.Message, "3 of 20 members active") {
		t.Errorf("message = %q, want it to report 3 of 20 members active", denial.Message)
	}
}

func TestRollingWindow_ActiveCountNeverExceedsTheMembership(t *testing.T) {
	// A member removed mid-window still has usage records, so the raw count can
	// reach the membership; adding the caller back must not report 21 of 20.
	store := &fairShareUsageStore{orgTokens: 900, userTokens: 300, otherActiveUsers: 20}
	ev := newRollingWindowEvaluator(store)

	_, denial, err := ev.Acquire(context.Background(), fairShareScope(), tokenConstraint(1000))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if denial == nil {
		t.Fatal("request granted, want denied")
	}
	if !strings.Contains(denial.Message, "20 of 20 members active") {
		t.Errorf("message = %q, want the active count clamped to the membership", denial.Message)
	}
}

func TestRollingWindow_OrgBudgetStillCapsEverything(t *testing.T) {
	// Whatever the per-user allocation says, the plan-wide budget is the hard
	// limit and is checked first.
	store := &fairShareUsageStore{orgTokens: 1000, userTokens: 10, otherActiveUsers: 4}
	ev := newRollingWindowEvaluator(store)

	_, denial, err := ev.Acquire(context.Background(), fairShareScope(), tokenConstraint(1000))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if denial == nil {
		t.Fatal("request granted, want denied by the plan-wide budget")
	}
	if !strings.Contains(denial.Message, "plan quota exceeded") {
		t.Errorf("message = %q, want a plan-wide denial", denial.Message)
	}
	if store.countCalls != 0 {
		t.Errorf("CountActivePlanUsersSince called %d times, want 0 before the plan budget is cleared", store.countCalls)
	}
}

func TestRollingWindow_NoUserContextSkipsFairShare(t *testing.T) {
	store := &fairShareUsageStore{orgTokens: 300, otherActiveUsers: 2}
	ev := newRollingWindowEvaluator(store)

	scope := fairShareScope()
	scope.UserID = ""

	_, denial, err := ev.Acquire(context.Background(), scope, tokenConstraint(1000))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if denial != nil {
		t.Fatalf("request denied without a user context: %s", denial.Message)
	}
	if store.countCalls != 0 {
		t.Errorf("CountActivePlanUsersSince called %d times, want 0 without a user context", store.countCalls)
	}
}

func TestRollingWindow_HappyHourOpensTheLeftoverBeforeReset(t *testing.T) {
	// 95 % into a 5h anchored window with most of the budget still unspent: the
	// leftover would be destroyed at reset, so a heavy user is let through.
	anchor := time.Now().Add(-4*time.Hour - 45*time.Minute)
	c := tokenConstraint(1000)
	c.WindowAnchor = &anchor

	store := &fairShareUsageStore{orgTokens: 300, userTokens: 290, otherActiveUsers: 0}
	ev := newRollingWindowEvaluator(store)

	_, denial, err := ev.Acquire(context.Background(), fairShareScope(), c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if denial != nil {
		t.Fatalf("request denied during happy hour: %s", denial.Message)
	}

	// Same state early in the window: the user is past their share and is denied.
	earlyAnchor := time.Now().Add(-15 * time.Minute)
	c.WindowAnchor = &earlyAnchor
	store.orgTokens = 900 // plan running well ahead of the clock
	_, denial, err = ev.Acquire(context.Background(), fairShareScope(), c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if denial == nil {
		t.Fatal("request granted early in the window, want throttled")
	}
	if !strings.Contains(denial.Message, string(model.FairShareModeThrottled)) {
		t.Errorf("message = %q, want it to name the throttled mode", denial.Message)
	}
}
