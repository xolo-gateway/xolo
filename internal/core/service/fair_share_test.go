package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/service"
	"github.com/pkg/errors"
)

type fakePlanUsageReader struct {
	planTokens int64
	planValue  int64
	userTokens int64
	userValue  int64

	otherActiveUsers int64
	countErr         error
	sumErr           error

	excludedUser model.UserID
}

func (f *fakePlanUsageReader) SumPlanUsageSince(_ context.Context, _ model.OrgID, _ model.ProviderID, _ time.Time) (int64, int64, error) {
	if f.sumErr != nil {
		return 0, 0, f.sumErr
	}
	return f.planTokens, f.planValue, nil
}

func (f *fakePlanUsageReader) SumUserPlanUsageSince(_ context.Context, _ model.UserID, _ model.OrgID, _ model.ProviderID, _ time.Time) (int64, int64, error) {
	return f.userTokens, f.userValue, nil
}

func (f *fakePlanUsageReader) CountActivePlanUsersSince(_ context.Context, _ model.OrgID, _ model.ProviderID, _ time.Time, excludeUserID model.UserID) (int64, error) {
	f.excludedUser = excludeUserID
	if f.countErr != nil {
		return 0, f.countErr
	}
	return f.otherActiveUsers, nil
}

func tokenAndValueConstraint(tokens, value int64) model.PlanConstraint {
	return model.PlanConstraint{
		Kind:        model.ConstraintRollingWindow,
		Label:       "5h",
		Duration:    model.PlanDuration(5 * time.Hour),
		TokenBudget: &tokens,
		ValueBudget: &value,
	}
}

func request(c model.PlanConstraint) service.FairShareRequest {
	return service.FairShareRequest{
		OrgID:       "org-1",
		ProviderID:  "prov-1",
		UserID:      "user-1",
		MemberCount: 20,
		Constraint:  c,
		Now:         time.Now(),
	}
}

func TestFairShareService_ResolvesBothBudgets(t *testing.T) {
	reader := &fakePlanUsageReader{planTokens: 300, planValue: 3_000, userTokens: 100, userValue: 1_000, otherActiveUsers: 2}
	svc := service.NewFairShareService(reader)

	got, err := svc.Resolve(context.Background(), request(tokenAndValueConstraint(1000, 10_000)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.TokenAllowance == nil || got.ValueAllowance == nil {
		t.Fatal("both allowances must be resolved when the constraint carries both budgets")
	}
	// 0.3×B/20 + 0.7×B/3 on each budget.
	if *got.TokenAllowance != 248 {
		t.Errorf("token allowance = %d, want 248", *got.TokenAllowance)
	}
	if *got.ValueAllowance != 2_483 {
		t.Errorf("value allowance = %d, want 2483", *got.ValueAllowance)
	}
	if got.ActiveUsers != 3 {
		t.Errorf("active users = %d, want 3 (2 others plus the caller)", got.ActiveUsers)
	}
	if got.CountDegraded {
		t.Error("count reported as degraded, want a clean read")
	}
	if reader.excludedUser != "user-1" {
		t.Errorf("excluded user = %q, want the caller", reader.excludedUser)
	}
}

func TestFairShareService_OnlyTheBudgetsTheConstraintCarries(t *testing.T) {
	reader := &fakePlanUsageReader{planTokens: 300, userTokens: 100, otherActiveUsers: 2}
	svc := service.NewFairShareService(reader)

	c := tokenAndValueConstraint(1000, 10_000)
	c.ValueBudget = nil

	got, err := svc.Resolve(context.Background(), request(c))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.TokenAllowance == nil {
		t.Fatal("token allowance must be resolved")
	}
	if got.ValueAllowance != nil {
		t.Errorf("value allowance = %d, want none", *got.ValueAllowance)
	}
	if got.ValueMode != "" {
		t.Errorf("value mode = %q, want empty", got.ValueMode)
	}
}

func TestFairShareService_CountFailureFallsBackToTheStaticShare(t *testing.T) {
	// Losing the count must narrow the allowance to budget/members, never widen
	// it, and must be reported so callers do not present it as a measurement.
	reader := &fakePlanUsageReader{planTokens: 300, userTokens: 10, otherActiveUsers: 2, countErr: errors.New("boom")}
	svc := service.NewFairShareService(reader)

	got, err := svc.Resolve(context.Background(), request(tokenAndValueConstraint(1000, 10_000)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.CountDegraded {
		t.Error("CountDegraded = false, want the fallback reported")
	}
	if got.ActiveUsers != 20 {
		t.Errorf("active users = %d, want the full membership", got.ActiveUsers)
	}
	if *got.TokenAllowance != 50 {
		t.Errorf("token allowance = %d, want the static 1000/20 share", *got.TokenAllowance)
	}
}

func TestFairShareService_SumFailureIsAnError(t *testing.T) {
	// Unlike the count, plan totals have no safe fallback: a missing total would
	// silently hand out a share the plan may not be able to honour.
	reader := &fakePlanUsageReader{sumErr: errors.New("boom")}
	svc := service.NewFairShareService(reader)

	if _, err := svc.Resolve(context.Background(), request(tokenAndValueConstraint(1000, 10_000))); err == nil {
		t.Fatal("expected an error when plan totals cannot be read")
	}
}

func TestFairShareService_ActiveCountIsClampedToTheMembership(t *testing.T) {
	// Members removed mid-window still carry usage records.
	reader := &fakePlanUsageReader{planTokens: 300, userTokens: 10, otherActiveUsers: 50}
	svc := service.NewFairShareService(reader)

	got, err := svc.Resolve(context.Background(), request(tokenAndValueConstraint(1000, 10_000)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ActiveUsers != 20 {
		t.Errorf("active users = %d, want it clamped to the 20 members", got.ActiveUsers)
	}
}

func TestFairShareService_ModeIsPerBudget(t *testing.T) {
	// The pacing factor depends on how full each budget is, so one constraint can
	// be throttled on tokens and still shared on value. Mode() keeps the most
	// restrictive of the two for a single-badge display.
	anchor := time.Now().Add(-15 * time.Minute)
	c := tokenAndValueConstraint(1000, 10_000)
	c.WindowAnchor = &anchor

	reader := &fakePlanUsageReader{planTokens: 900, planValue: 100, userTokens: 10, userValue: 10, otherActiveUsers: 1}
	svc := service.NewFairShareService(reader)

	got, err := svc.Resolve(context.Background(), request(c))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.TokenMode != model.FairShareModeThrottled {
		t.Errorf("token mode = %q, want throttled", got.TokenMode)
	}
	if got.ValueMode != model.FairShareModeShared {
		t.Errorf("value mode = %q, want shared", got.ValueMode)
	}
	if got.Mode() != model.FairShareModeThrottled {
		t.Errorf("Mode() = %q, want the most restrictive of the two", got.Mode())
	}
}

func TestFairShareService_PreReadPlanTotalsAreNotSummedAgain(t *testing.T) {
	// The enforcer reads the plan totals to check the plan-wide budget before
	// allocating; re-aggregating the same window would double the cost of the
	// proxy hot path, per constraint.
	reader := &countingPlanUsageReader{fakePlanUsageReader: fakePlanUsageReader{userTokens: 100, otherActiveUsers: 2}}
	svc := service.NewFairShareService(reader)

	req := request(tokenAndValueConstraint(1000, 10_000))
	req.PlanUsage = &service.PlanUsage{Tokens: 300, Value: 3_000}

	got, err := svc.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reader.planSums != 0 {
		t.Errorf("SumPlanUsageSince called %d times, want 0 when the totals are handed in", reader.planSums)
	}
	if got.PlanTokens != 300 || got.PlanValue != 3_000 {
		t.Errorf("plan usage = (%d, %d), want the values handed in", got.PlanTokens, got.PlanValue)
	}
	// The allocation must be the same as if it had read them itself.
	if *got.TokenAllowance != 248 {
		t.Errorf("token allowance = %d, want 248", *got.TokenAllowance)
	}
}

func TestFairShareService_NotApplicableWithoutMembershipOrUser(t *testing.T) {
	// The degradation invariant — every fallback narrows the allowance — only
	// holds with a membership to divide by. Without one, a failed count would
	// leave a single "active" user holding the whole commons, so the service
	// refuses rather than relying on its callers to filter.
	svc := service.NewFairShareService(&fakePlanUsageReader{})

	noMembers := request(tokenAndValueConstraint(1000, 10_000))
	noMembers.MemberCount = 0
	if _, err := svc.Resolve(context.Background(), noMembers); !errors.Is(err, service.ErrFairShareNotApplicable) {
		t.Errorf("error = %v, want ErrFairShareNotApplicable", err)
	}

	noUser := request(tokenAndValueConstraint(1000, 10_000))
	noUser.UserID = ""
	if _, err := svc.Resolve(context.Background(), noUser); !errors.Is(err, service.ErrFairShareNotApplicable) {
		t.Errorf("error = %v, want ErrFairShareNotApplicable", err)
	}
}

// countingPlanUsageReader counts the plan-wide aggregations it is asked for.
type countingPlanUsageReader struct {
	fakePlanUsageReader
	planSums int
}

func (f *countingPlanUsageReader) SumPlanUsageSince(ctx context.Context, orgID model.OrgID, providerID model.ProviderID, since time.Time) (int64, int64, error) {
	f.planSums++
	return f.fakePlanUsageReader.SumPlanUsageSince(ctx, orgID, providerID, since)
}
