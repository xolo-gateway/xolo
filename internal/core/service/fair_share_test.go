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
	// Floor (0.3×B/20) plus a third of the commons, itself capped by what the two
	// other active users left once the 17 absent members' floors are set aside.
	if *got.TokenAllowance != 191 {
		t.Errorf("token allowance = %d, want 191", *got.TokenAllowance)
	}
	if *got.ValueAllowance != 1_916 {
		t.Errorf("value allowance = %d, want 1916", *got.ValueAllowance)
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
	// Split across the whole membership, as the static cap did — the last unit of
	// the 1000/20 share going to what the plan has already consumed.
	if *got.TokenAllowance != 49 {
		t.Errorf("token allowance = %d, want the share split across every member", *got.TokenAllowance)
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
	// be throttled on tokens and still shared on value — which is why the screens
	// badge each gauge from its own mode.
	anchor := time.Now().Add(-15 * time.Minute)
	c := tokenAndValueConstraint(1000, 10_000)
	c.WindowAnchor = &anchor

	// The caller is the one who burned the tokens, so the availability cap stays
	// loose on that budget and pacing is what narrows it.
	reader := &fakePlanUsageReader{planTokens: 900, planValue: 100, userTokens: 890, userValue: 10, otherActiveUsers: 1}
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
	if *got.TokenAllowance != 191 {
		t.Errorf("token allowance = %d, want 191", *got.TokenAllowance)
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

// countingCountReader counts how many times the active-user count is read.
type countingCountReader struct {
	fakePlanUsageReader
	counts int
}

func (f *countingCountReader) CountActivePlanUsersSince(ctx context.Context, orgID model.OrgID, providerID model.ProviderID, since time.Time, excludeUserID model.UserID) (int64, error) {
	f.counts++
	return f.fakePlanUsageReader.CountActivePlanUsersSince(ctx, orgID, providerID, since, excludeUserID)
}

func TestFairShareService_ActiveUserCountIsReusedWithinItsTTL(t *testing.T) {
	// The count walks the whole window in the database and moves slowly; a user
	// firing requests in a row must not pay for it every time.
	reader := &countingCountReader{fakePlanUsageReader: fakePlanUsageReader{planTokens: 300, userTokens: 100, otherActiveUsers: 2}}
	svc := service.NewFairShareService(reader)

	req := request(tokenAndValueConstraint(1000, 10_000))
	first, err := svc.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, err := svc.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if reader.counts != 1 {
		t.Errorf("count read %d times, want 1 within the TTL", reader.counts)
	}
	if first.ActiveUsers != second.ActiveUsers || *first.TokenAllowance != *second.TokenAllowance {
		t.Errorf("cached resolve differs: %d/%d vs %d/%d",
			first.ActiveUsers, *first.TokenAllowance, second.ActiveUsers, *second.TokenAllowance)
	}
}

func TestFairShareService_ActiveUserCountIsNotSharedAcrossKeys(t *testing.T) {
	// The count is taken without the caller and over one window, so neither may
	// be borrowed from another user's or another window's entry.
	reader := &countingCountReader{fakePlanUsageReader: fakePlanUsageReader{planTokens: 300, userTokens: 100, otherActiveUsers: 2}}
	svc := service.NewFairShareService(reader)

	base := request(tokenAndValueConstraint(1000, 10_000))
	if _, err := svc.Resolve(context.Background(), base); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	otherUser := base
	otherUser.UserID = "user-2"
	if _, err := svc.Resolve(context.Background(), otherUser); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	otherWindow := base
	otherWindow.Now = base.Now.Add(6 * time.Hour) // a later sliding window
	if _, err := svc.Resolve(context.Background(), otherWindow); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if reader.counts != 3 {
		t.Errorf("count read %d times, want one read per user and per window", reader.counts)
	}
}

func TestFairShareService_CacheCanBeDisabled(t *testing.T) {
	reader := &countingCountReader{fakePlanUsageReader: fakePlanUsageReader{planTokens: 300, userTokens: 100, otherActiveUsers: 2}}
	svc := service.NewFairShareServiceWithCacheTTL(reader, 0)

	req := request(tokenAndValueConstraint(1000, 10_000))
	for range 3 {
		if _, err := svc.Resolve(context.Background(), req); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if reader.counts != 3 {
		t.Errorf("count read %d times, want every call to reach the store", reader.counts)
	}
}

func TestFairShareService_ExpiredCacheEntryIsReRead(t *testing.T) {
	reader := &countingCountReader{fakePlanUsageReader: fakePlanUsageReader{planTokens: 300, userTokens: 100, otherActiveUsers: 2}}
	svc := service.NewFairShareServiceWithCacheTTL(reader, 30*time.Second)

	// An anchored window, so the window start — part of the cache key — does not
	// move with the clock: without it the entry would be missed because the key
	// changed, and the test would pass whether the TTL worked or not.
	c := tokenAndValueConstraint(1000, 10_000)
	anchor := time.Now().Add(-time.Hour)
	c.WindowAnchor = &anchor

	req := request(c)
	if _, err := svc.Resolve(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The cache ages against the request's own clock, so the passage of time is
	// expressed rather than waited for.
	later := req
	later.Now = req.Now.Add(31 * time.Second)
	if _, err := svc.Resolve(context.Background(), later); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if reader.counts != 2 {
		t.Errorf("count read %d times, want the expired entry to be read again", reader.counts)
	}
}

func TestFairShareService_SlidingWindowCountIsReusedAcrossNearbyInstants(t *testing.T) {
	// The default constraint has no anchor, so its window start is now−duration:
	// a different instant on every request. The cache must still serve requests
	// made within one TTL of each other, or it is write-only on the common case.
	reader := &countingCountReader{fakePlanUsageReader: fakePlanUsageReader{planTokens: 300, userTokens: 100, otherActiveUsers: 2}}
	svc := service.NewFairShareService(reader)

	c := tokenAndValueConstraint(1000, 10_000)
	if c.WindowAnchor != nil {
		t.Fatal("this test needs a sliding window")
	}

	// Aligned on the TTL so the three instants fall in the same bucket regardless
	// of the wall clock the test happens to run at.
	start := time.Now().Truncate(service.DefaultActiveUserCacheTTL).Add(time.Second)
	for _, offset := range []time.Duration{0, 750 * time.Millisecond, 3 * time.Second} {
		req := request(c)
		req.Now = start.Add(offset)
		if _, err := svc.Resolve(context.Background(), req); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if reader.counts != 1 {
		t.Errorf("count read %d times, want 1 for requests a few seconds apart on a sliding window", reader.counts)
	}
}

func TestFairShareService_CountFailureIsCachedAsAFallback(t *testing.T) {
	// A database that cannot answer the count must not be asked again on every
	// request while it struggles: the failure is remembered for the TTL, and the
	// shares computed meanwhile still say they are degraded.
	reader := &countingCountReader{fakePlanUsageReader: fakePlanUsageReader{planTokens: 300, userTokens: 100, countErr: errors.New("boom")}}
	svc := service.NewFairShareService(reader)

	c := tokenAndValueConstraint(1000, 10_000)
	anchor := time.Now().Add(-time.Hour)
	c.WindowAnchor = &anchor
	req := request(c)

	for i := range 3 {
		got, err := svc.Resolve(context.Background(), req)
		if err != nil {
			t.Fatalf("resolve %d: unexpected error: %v", i, err)
		}
		if !got.CountDegraded {
			t.Errorf("resolve %d: CountDegraded = false, want the fallback reported while the failure is cached", i)
		}
		if got.ActiveUsers != 20 {
			t.Errorf("resolve %d: active users = %d, want the whole membership", i, got.ActiveUsers)
		}
	}
	if reader.counts != 1 {
		t.Errorf("count attempted %d times, want 1 while the failure is cached", reader.counts)
	}

	// Once the entry expires the store is asked again, and a recovered count
	// replaces the fallback.
	reader.countErr = nil
	later := req
	later.Now = req.Now.Add(31 * time.Second)
	got, err := svc.Resolve(context.Background(), later)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.CountDegraded {
		t.Error("CountDegraded = true after the store recovered, want a real count")
	}
	if reader.counts != 2 {
		t.Errorf("count attempted %d times, want a retry once the cached failure expired", reader.counts)
	}
}
