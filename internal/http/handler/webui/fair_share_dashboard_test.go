package webui

import (
	"context"
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/service"
	orgcomponent "github.com/xolo-gateway/xolo/internal/http/handler/webui/org/component"
	"github.com/pkg/errors"
)

// stubPlanUsageReader implements service.PlanUsageReader for the wiring tests.
type stubPlanUsageReader struct {
	planTokens       int64
	userTokens       int64
	otherActiveUsers int64
	sumErr           error
}

func (s *stubPlanUsageReader) SumPlanUsageSince(context.Context, model.OrgID, model.ProviderID, time.Time) (int64, int64, error) {
	if s.sumErr != nil {
		return 0, 0, s.sumErr
	}
	return s.planTokens, 0, nil
}

func (s *stubPlanUsageReader) SumUserPlanUsageSince(context.Context, model.UserID, model.OrgID, model.ProviderID, time.Time) (int64, int64, error) {
	return s.userTokens, 0, nil
}

func (s *stubPlanUsageReader) CountActivePlanUsersSince(context.Context, model.OrgID, model.ProviderID, time.Time, model.UserID) (int64, error) {
	return s.otherActiveUsers, nil
}

func (s *stubPlanUsageReader) HasPlanUsageSince(context.Context, model.UserID, model.OrgID, model.ProviderID, time.Time) (bool, error) {
	return false, nil
}

func planConstraint(budget int64) model.PlanConstraint {
	return model.PlanConstraint{
		Kind:        model.ConstraintRollingWindow,
		Label:       "5h",
		Duration:    model.PlanDuration(5 * time.Hour),
		TokenBudget: &budget,
	}
}

func TestApplyFairShare_ReplacesTheDenominatorAndReportsTheBasis(t *testing.T) {
	// The screen must show the denominator the enforcer decides against: a gauge
	// promising room the enforcer refuses is worse than no gauge at all.
	reader := &stubPlanUsageReader{planTokens: 300, userTokens: 100, otherActiveUsers: 2}
	c := planConstraint(1000)
	cu := orgcomponent.SubscriptionConstraintUsage{Constraint: fairShareConstraint(c, 20)}

	if !applyFairShare(context.Background(), service.NewFairShareService(reader), &cu, c, "org-1", "prov-1", "user-1", 20, time.Now(), nil) {
		t.Fatal("applyFairShare reported failure, want success")
	}

	// 0.3×1000/20 + 0.7×1000/3, not the static 1000/20.
	if cu.Constraint.TokenBudget == nil || *cu.Constraint.TokenBudget != 191 {
		t.Errorf("token budget = %d, want the 191 allowance", *cu.Constraint.TokenBudget)
	}
	if cu.TokensUsed != 100 {
		t.Errorf("tokens used = %d, want the total read by the allocation", cu.TokensUsed)
	}
	if cu.ActiveUsers != 3 || cu.MemberCount != 20 {
		t.Errorf("basis = %d of %d, want 3 of 20", cu.ActiveUsers, cu.MemberCount)
	}
	// The two other users consumed 200, so the cap trimmed the commons, but 191
	// is still above the static 50 the old rule granted: nothing to badge.
	if cu.TokenMode != model.FairShareModeShared {
		t.Errorf("token mode = %q, want shared while the share beats the static one", cu.TokenMode)
	}
	if cu.ShareDegraded {
		t.Error("share reported as degraded, want a clean read")
	}
}

func TestApplyFairShare_KeepsTheStaticShareWhenTheAllocationFails(t *testing.T) {
	// On failure the screen keeps the conservative static denominator and posts
	// no badge, rather than showing a figure the enforcer does not apply.
	reader := &stubPlanUsageReader{sumErr: errors.New("boom")}
	c := planConstraint(1000)
	cu := orgcomponent.SubscriptionConstraintUsage{Constraint: fairShareConstraint(c, 20)}

	if applyFairShare(context.Background(), service.NewFairShareService(reader), &cu, c, "org-1", "prov-1", "user-1", 20, time.Now(), nil) {
		t.Fatal("applyFairShare reported success, want failure")
	}
	if cu.Constraint.TokenBudget == nil || *cu.Constraint.TokenBudget != 50 {
		t.Errorf("token budget = %v, want the static 1000/20 share left in place", cu.Constraint.TokenBudget)
	}
	if cu.TokenMode != "" || cu.ActiveUsers != 0 {
		t.Errorf("badge basis = (%q, %d), want none when the allocation failed", cu.TokenMode, cu.ActiveUsers)
	}
}

func TestApplyFairShare_SkippedWithoutTheBasisToAllocateOn(t *testing.T) {
	reader := &stubPlanUsageReader{planTokens: 300, userTokens: 100, otherActiveUsers: 2}
	svc := service.NewFairShareService(reader)
	c := planConstraint(1000)

	cases := []struct {
		name        string
		userID      model.UserID
		memberCount int64
		service     *service.FairShareService
		constraint  model.PlanConstraint
	}{
		{"no service", "user-1", 20, nil, c},
		{"no members", "user-1", 0, svc, c},
		{"no user", "", 20, svc, c},
		{"no budget", "user-1", 20, svc, model.PlanConstraint{Kind: model.ConstraintRollingWindow, Duration: c.Duration}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cu := orgcomponent.SubscriptionConstraintUsage{Constraint: tc.constraint}
			if applyFairShare(context.Background(), tc.service, &cu, tc.constraint, "org-1", "prov-1", tc.userID, tc.memberCount, time.Now(), nil) {
				t.Error("applyFairShare reported success, want it skipped")
			}
			if cu.ActiveUsers != 0 || cu.TokenMode != "" {
				t.Errorf("view model touched: active=%d mode=%q", cu.ActiveUsers, cu.TokenMode)
			}
		})
	}
}

func TestApplyFairShare_UsesThePreReadPlanTotals(t *testing.T) {
	// The page reads the plan-wide totals once per constraint and hands them in,
	// as the proxy hot path does; the allocation must not sum the window again.
	reader := &countingReader{stubPlanUsageReader: stubPlanUsageReader{userTokens: 100, otherActiveUsers: 2}}
	c := planConstraint(1000)
	cu := orgcomponent.SubscriptionConstraintUsage{Constraint: c}

	ok := applyFairShare(context.Background(), service.NewFairShareService(reader), &cu, c,
		"org-1", "prov-1", "user-1", 20, time.Now(), &service.PlanUsage{Tokens: 300})
	if !ok {
		t.Fatal("applyFairShare reported failure, want success")
	}
	if reader.planSums != 0 {
		t.Errorf("SumPlanUsageSince called %d times, want 0 when the totals are handed in", reader.planSums)
	}
	if cu.Constraint.TokenBudget == nil || *cu.Constraint.TokenBudget != 191 {
		t.Errorf("token budget = %d, want the 191 allowance", *cu.Constraint.TokenBudget)
	}
}

type countingReader struct {
	stubPlanUsageReader
	planSums int
}

func (c *countingReader) SumPlanUsageSince(ctx context.Context, orgID model.OrgID, providerID model.ProviderID, since time.Time) (int64, int64, error) {
	c.planSums++
	return c.stubPlanUsageReader.SumPlanUsageSince(ctx, orgID, providerID, since)
}
