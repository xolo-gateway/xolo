package service_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

type fakePlanUsageReader struct {
	planTokens int64
	planValue  int64
	userTokens int64
	userValue  int64

	// otherActiveUsers is the plan-wide count as the store reports it; the
	// caller is present in it when callerPresent is true.
	otherActiveUsers int64
	callerPresent    bool
	countErr         error
	sumErr           error

	excludedUser  model.UserID
	presenceCalls atomic.Int64 // read concurrently by every caller of a plan
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

func (f *fakePlanUsageReader) HasPlanUsageSince(_ context.Context, _ model.UserID, _ model.OrgID, _ model.ProviderID, _ time.Time) (bool, error) {
	f.presenceCalls.Add(1)
	return f.callerPresent, nil
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
		t.Errorf("active users = %d, want 3 (2 counted plus the caller, absent from the count)", got.ActiveUsers)
	}
	if got.CountDegraded {
		t.Error("count reported as degraded, want a clean read")
	}
	// The count is taken plan-wide so every user on the plan can share it; the
	// caller's presence is a separate lookup.
	if reader.excludedUser != "" {
		t.Errorf("excluded user = %q, want the plan-wide count", reader.excludedUser)
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

func TestFairShareService_ActiveUserCountIsSharedByThePlanAndKeyedByWindow(t *testing.T) {
	// One count per plan and window, shared by every user on it; a new window
	// reads again.
	reader := &countingCountReader{fakePlanUsageReader: fakePlanUsageReader{planTokens: 300, userTokens: 100, otherActiveUsers: 2}}
	svc := service.NewFairShareService(reader)

	base := request(tokenAndValueConstraint(1000, 10_000))
	if _, err := svc.Resolve(context.Background(), base); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Another user on the same plan and window shares the count: that is the
	// point of taking it plan-wide.
	otherUser := base
	otherUser.UserID = "user-2"
	if _, err := svc.Resolve(context.Background(), otherUser); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reader.counts != 1 {
		t.Errorf("count read %d times for two users on one plan, want 1", reader.counts)
	}

	otherWindow := base
	otherWindow.Now = base.Now.Add(6 * time.Hour) // a later sliding window
	if _, err := svc.Resolve(context.Background(), otherWindow); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reader.counts != 2 {
		t.Errorf("count read %d times, want a new read for a new window", reader.counts)
	}
}

func TestFairShareService_CallerAlreadyCountedIsNotAddedTwice(t *testing.T) {
	// The plan-wide count already includes a user who consumed in the window;
	// adding them back would overstate the competition and narrow every share.
	reader := &fakePlanUsageReader{planTokens: 300, userTokens: 100, otherActiveUsers: 3, callerPresent: true}
	svc := service.NewFairShareService(reader)

	got, err := svc.Resolve(context.Background(), request(tokenAndValueConstraint(1000, 10_000)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ActiveUsers != 3 {
		t.Errorf("active users = %d, want the plan-wide 3 with the caller already in it", got.ActiveUsers)
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

func TestFairShareService_CountIsSharedBetweenConstraintsOfTheSameWindow(t *testing.T) {
	// The count depends on the window alone, never on the budget or the label,
	// so two constraints over the same window share one cache entry. This pins
	// that invariance: should the count ever become sensitive to the constraint,
	// the key must grow with it and this test will say so.
	reader := &countingCountReader{fakePlanUsageReader: fakePlanUsageReader{planTokens: 300, userTokens: 100, otherActiveUsers: 2}}
	svc := service.NewFairShareService(reader)

	anchor := time.Now().Add(-time.Hour)
	tokens := tokenAndValueConstraint(1000, 10_000)
	tokens.Label, tokens.WindowAnchor = "tokens", &anchor
	value := tokenAndValueConstraint(50_000, 500_000)
	value.Label, value.WindowAnchor = "value", &anchor

	now := time.Now()
	for _, c := range []model.PlanConstraint{tokens, value} {
		req := request(c)
		req.Now = now
		if _, err := svc.Resolve(context.Background(), req); err != nil {
			t.Fatalf("resolve %s: %v", c.Label, err)
		}
	}

	if reader.counts != 1 {
		t.Errorf("count read %d times for two constraints over one window, want 1", reader.counts)
	}
}

func TestFairShareService_ConcurrentReloadsFireOneCount(t *testing.T) {
	// When an entry expires, every request on the plan arrives at the same
	// time; without single-flight each would run the same COUNT(DISTINCT).
	reader := &blockingCountReader{
		fakePlanUsageReader: fakePlanUsageReader{planTokens: 300, userTokens: 100, otherActiveUsers: 2},
		release:             make(chan struct{}),
		started:             make(chan struct{}),
	}
	svc := service.NewFairShareService(reader)

	c := tokenAndValueConstraint(1000, 10_000)
	anchor := time.Now().Add(-time.Hour)
	c.WindowAnchor = &anchor
	req := request(c)

	const callers = 8
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.Resolve(context.Background(), req); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	// Let the callers pile up on the count before releasing it.
	reader.waitForFirst()
	time.Sleep(20 * time.Millisecond)
	close(reader.release)
	wg.Wait()

	if got := reader.counts.Load(); got != 1 {
		t.Errorf("count read %d times by %d concurrent callers, want 1", got, callers)
	}
}

// blockingCountReader holds the first count until released, so concurrent
// callers can be observed piling up on it. started is closed exactly once, by
// the first count, and is the only thing the test goroutine reads: a channel
// close is a synchronisation point, a plain field assignment is not.
type blockingCountReader struct {
	fakePlanUsageReader
	release chan struct{}
	started chan struct{}
	first   sync.Once
	counts  atomic.Int64
}

func (b *blockingCountReader) waitForFirst() {
	<-b.started
}

func (b *blockingCountReader) CountActivePlanUsersSince(ctx context.Context, orgID model.OrgID, providerID model.ProviderID, since time.Time, excludeUserID model.UserID) (int64, error) {
	b.counts.Add(1)
	b.first.Do(func() { close(b.started) })
	<-b.release
	return b.fakePlanUsageReader.CountActivePlanUsersSince(ctx, orgID, providerID, since, excludeUserID)
}

func TestFairShareService_PresenceIsRememberedOncePositive(t *testing.T) {
	// A user seen in a window stays seen for the rest of it: presence never
	// turns false again, so a positive answer is kept for the TTL instead of
	// being asked of the store on every request.
	reader := &fakePlanUsageReader{planTokens: 300, userTokens: 100, otherActiveUsers: 3, callerPresent: true}
	svc := service.NewFairShareService(reader)

	c := tokenAndValueConstraint(1000, 10_000)
	anchor := time.Now().Add(-time.Hour)
	c.WindowAnchor = &anchor
	req := request(c)

	for range 3 {
		if _, err := svc.Resolve(context.Background(), req); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if got := reader.presenceCalls.Load(); got != 1 {
		t.Errorf("presence asked %d times, want 1 once it answered present", got)
	}
}

func TestFairShareService_AbsenceIsNotRemembered(t *testing.T) {
	// The other way round, absence can end at any moment — with this very
	// request — so it is asked again each time.
	reader := &fakePlanUsageReader{planTokens: 300, userTokens: 0, otherActiveUsers: 3, callerPresent: false}
	svc := service.NewFairShareService(reader)

	c := tokenAndValueConstraint(1000, 10_000)
	anchor := time.Now().Add(-time.Hour)
	c.WindowAnchor = &anchor
	req := request(c)

	for range 2 {
		if _, err := svc.Resolve(context.Background(), req); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if got := reader.presenceCalls.Load(); got != 2 {
		t.Errorf("presence asked %d times, want it asked again while absent", got)
	}
}

func TestFairShareService_CallerCancellationDoesNotDegradeThePlan(t *testing.T) {
	// The count runs under the winning request's context. A client that hangs
	// up mid-query must not write "degraded" into an entry shared by everyone
	// on the plan for a whole TTL: the count is detached from the request.
	reader := &ctxCheckingReader{fakePlanUsageReader: fakePlanUsageReader{planTokens: 300, userTokens: 100, otherActiveUsers: 2}}
	svc := service.NewFairShareService(reader)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the client is already gone

	got, err := svc.Resolve(ctx, request(tokenAndValueConstraint(1000, 10_000)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.CountDegraded {
		t.Error("CountDegraded = true after a client cancellation, want the count to have run detached")
	}
	if got.ActiveUsers != 3 {
		t.Errorf("active users = %d, want the real count", got.ActiveUsers)
	}
}

// ctxCheckingReader fails the count when the context it receives is already
// done, as a real database driver would.
type ctxCheckingReader struct {
	fakePlanUsageReader
}

func (r *ctxCheckingReader) CountActivePlanUsersSince(ctx context.Context, orgID model.OrgID, providerID model.ProviderID, since time.Time, excludeUserID model.UserID) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return r.fakePlanUsageReader.CountActivePlanUsersSince(ctx, orgID, providerID, since, excludeUserID)
}
