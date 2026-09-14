package model

import (
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad time %q: %v", s, err)
	}
	return v
}

func TestPlanConstraint_CurrentWindowStart_Sliding(t *testing.T) {
	c := PlanConstraint{Kind: ConstraintRollingWindow, Duration: PlanDuration(5 * time.Hour)}
	now := mustTime(t, "2026-07-01T12:30:00Z")

	if got := c.CurrentWindowStart(now); !got.Equal(now.Add(-5 * time.Hour)) {
		t.Fatalf("sliding window start = %s, want %s", got, now.Add(-5*time.Hour))
	}
	if c.IsAnchored() {
		t.Fatal("constraint without anchor must not be anchored")
	}
	if got := c.NextResetAt(now); !got.IsZero() {
		t.Fatalf("sliding window NextResetAt = %s, want zero", got)
	}
}

func TestPlanConstraint_CurrentWindowStart_Anchored(t *testing.T) {
	anchor := mustTime(t, "2026-07-01T05:00:00Z")
	c := PlanConstraint{
		Kind:         ConstraintRollingWindow,
		Duration:     PlanDuration(5 * time.Hour),
		WindowAnchor: &anchor,
	}

	cases := []struct {
		now       string
		wantStart string
		wantReset string
	}{
		// Same window as the anchor.
		{"2026-07-01T05:00:00Z", "2026-07-01T05:00:00Z", "2026-07-01T10:00:00Z"},
		{"2026-07-01T09:59:59Z", "2026-07-01T05:00:00Z", "2026-07-01T10:00:00Z"},
		// Next window after one full period.
		{"2026-07-01T10:00:00Z", "2026-07-01T10:00:00Z", "2026-07-01T15:00:00Z"},
		{"2026-07-01T12:30:00Z", "2026-07-01T10:00:00Z", "2026-07-01T15:00:00Z"},
		// Several periods later: anchor 05:00 + 4×5h = 01:00 next day (exact boundary).
		{"2026-07-02T01:00:00Z", "2026-07-02T01:00:00Z", "2026-07-02T06:00:00Z"},
		{"2026-07-02T03:30:00Z", "2026-07-02T01:00:00Z", "2026-07-02T06:00:00Z"},
		// Before the anchor (floor toward negative infinity).
		{"2026-07-01T04:59:59Z", "2026-07-01T00:00:00Z", "2026-07-01T05:00:00Z"},
	}

	if !c.IsAnchored() {
		t.Fatal("anchored constraint must report IsAnchored()")
	}

	for _, tc := range cases {
		now := mustTime(t, tc.now)
		if got := c.CurrentWindowStart(now); !got.Equal(mustTime(t, tc.wantStart)) {
			t.Errorf("now=%s: window start = %s, want %s", tc.now, got, tc.wantStart)
		}
		if got := c.NextResetAt(now); !got.Equal(mustTime(t, tc.wantReset)) {
			t.Errorf("now=%s: reset = %s, want %s", tc.now, got, tc.wantReset)
		}
	}
}

func TestComputeFairShare_AllMembersActiveMatchesStaticShare(t *testing.T) {
	// The regime the static budget/members cap was built for: when everyone
	// competes, the reserve plus the commons add back up to exactly 1/N.
	p := FairShareParams{
		Budget:         1000,
		TotalMembers:   10,
		ActiveUsers:    10,
		Elapsed:        -1,
		Reserve:        DefaultReserveRatio,
		Slack:          DefaultPaceSlack,
		HappyHourStart: DefaultHappyHourStart,
	}

	got := ComputeFairShare(p)
	if got.Allowance != 100 {
		t.Errorf("allowance = %d, want 100 (the static budget/members share)", got.Allowance)
	}
	if got.Mode != FairShareModeShared {
		t.Errorf("mode = %q, want shared", got.Mode)
	}
}

func TestComputeFairShare_QuietMembersFreeBudgetForActiveOnes(t *testing.T) {
	// 3 active users out of 20 members: each gets its 1.5 % floor plus a third of
	// the commons, instead of the 5 % the static cap allowed.
	p := FairShareParams{
		Budget:         1000,
		TotalMembers:   20,
		ActiveUsers:    3,
		Elapsed:        -1,
		Reserve:        DefaultReserveRatio,
		Slack:          DefaultPaceSlack,
		HappyHourStart: DefaultHappyHourStart,
	}

	got := ComputeFairShare(p)
	// 0.3×1000/20 + 0.7×1000/3 = 15 + 233.33
	if got.Allowance != 248 {
		t.Errorf("allowance = %d, want 248", got.Allowance)
	}
	if static := p.Budget / int64(p.TotalMembers); got.Allowance <= static {
		t.Errorf("allowance = %d, want more than the static share %d", got.Allowance, static)
	}
}

func TestComputeFairShare_QuietMemberKeepsAFloor(t *testing.T) {
	// A single user monopolising the plan may not take the whole budget: the
	// reserve stays out of their reach so the 19 others still have room.
	p := FairShareParams{
		Budget:         1000,
		TotalMembers:   20,
		ActiveUsers:    1,
		Elapsed:        -1,
		Reserve:        DefaultReserveRatio,
		Slack:          DefaultPaceSlack,
		HappyHourStart: DefaultHappyHourStart,
	}

	got := ComputeFairShare(p)
	if got.Allowance >= p.Budget {
		t.Errorf("allowance = %d, want strictly less than the budget %d", got.Allowance, p.Budget)
	}
	// 0.3×1000/20 + 0.7×1000 = 715
	if got.Allowance != 715 {
		t.Errorf("allowance = %d, want 715", got.Allowance)
	}
}

func TestComputeFairShare_PacingClosesTheCommons(t *testing.T) {
	base := FairShareParams{
		Budget:         1000,
		TotalMembers:   10,
		ActiveUsers:    2,
		Reserve:        DefaultReserveRatio,
		Slack:          DefaultPaceSlack,
		HappyHourStart: DefaultHappyHourStart,
	}

	// 10 % into the window, nothing consumed yet: the commons is wide open.
	early := base
	early.Elapsed, early.UsedTotal = 0.1, 0
	openAllowance := ComputeFairShare(early)
	if openAllowance.Mode != FairShareModeShared {
		t.Errorf("mode = %q, want shared while the plan trails the clock", openAllowance.Mode)
	}

	// Same instant, but the plan already burned 80 % of the budget: the commons
	// closes back toward the guaranteed floor.
	ahead := early
	ahead.UsedTotal = 800
	throttled := ComputeFairShare(ahead)
	if throttled.Mode != FairShareModeThrottled {
		t.Errorf("mode = %q, want throttled when the plan runs ahead of the clock", throttled.Mode)
	}
	if throttled.Allowance >= openAllowance.Allowance {
		t.Errorf("throttled allowance = %d, want less than the open one %d", throttled.Allowance, openAllowance.Allowance)
	}
	if floor := int64(base.Reserve * float64(base.Budget) / float64(base.TotalMembers)); throttled.Allowance < floor {
		t.Errorf("throttled allowance = %d, want at least the guaranteed floor %d", throttled.Allowance, floor)
	}
}

func TestComputeFairShare_PacingIgnoredOnSlidingWindow(t *testing.T) {
	// A sliding window never resets, so there is nothing to pace against.
	p := FairShareParams{
		Budget:         1000,
		TotalMembers:   10,
		ActiveUsers:    2,
		UsedTotal:      900,
		Elapsed:        -1,
		Reserve:        DefaultReserveRatio,
		Slack:          DefaultPaceSlack,
		HappyHourStart: DefaultHappyHourStart,
	}

	if got := ComputeFairShare(p); got.Mode != FairShareModeShared {
		t.Errorf("mode = %q, want shared on a sliding window", got.Mode)
	}
}

func happyHourParams() FairShareParams {
	return FairShareParams{
		Budget:           1000,
		TotalMembers:     20,
		ActiveUsers:      1,
		UsedTotal:        200,
		UserUsed:         200,
		Elapsed:          0.95,
		WindowDuration:   5 * time.Hour,
		Reserve:          DefaultReserveRatio,
		Slack:            DefaultPaceSlack,
		HappyHourStart:   DefaultHappyHourStart,
		HappyHourMaxLead: DefaultHappyHourMaxLead,
	}
}

func TestComputeFairShare_HappyHourOpensTheLeftover(t *testing.T) {
	// 15 minutes from the reset: the leftover would be destroyed, so the sole
	// active user may take all of it on top of what they already hold.
	p := happyHourParams()

	got := ComputeFairShare(p)
	if got.Mode != FairShareModeHappyHour {
		t.Errorf("mode = %q, want happy_hour", got.Mode)
	}
	if got.Allowance != p.Budget {
		t.Errorf("allowance = %d, want the whole budget %d", got.Allowance, p.Budget)
	}

	// HappyHourStart >= 1 disables it.
	p.HappyHourStart = 1
	if got := ComputeFairShare(p); got.Mode == FairShareModeHappyHour {
		t.Error("happy hour must be disabled when HappyHourStart >= 1")
	}
}

// sharedHappyHourParams puts four users in a window close to its reset, with
// little consumed: the leftover share is then wider than the nominal one, so the
// happy hour actually applies.
func sharedHappyHourParams() FairShareParams {
	p := happyHourParams()
	p.ActiveUsers = 4
	p.UsedTotal = 200
	p.UserUsed = 100
	return p
}

func TestComputeFairShare_HappyHourSplitsTheLeftoverBetweenActiveUsers(t *testing.T) {
	// With several users still working, the leftover is shared rather than handed
	// to whoever asks first.
	p := sharedHappyHourParams()

	got := ComputeFairShare(p)
	if got.Mode != FairShareModeHappyHour {
		t.Fatalf("mode = %q, want happy_hour", got.Mode)
	}
	// The other three consumed 100 between them; the remaining 900 is split four
	// ways, so this user may hold 225 in total.
	if got.Allowance != 225 {
		t.Errorf("allowance = %d, want 225", got.Allowance)
	}
	if got.Allowance >= p.Budget {
		t.Errorf("allowance = %d, want less than the whole budget while others are active", got.Allowance)
	}
}

func TestComputeFairShare_HappyHourNeverNarrowsTheShare(t *testing.T) {
	// The happy hour hands out what the reset would destroy; it must never be the
	// reason a request is refused. Once the other active users have consumed a
	// lot, an equal split of the leftover is narrower than the nominal share — so
	// the nominal share stands, and crossing the threshold cannot turn a grant
	// into a denial.
	p := happyHourParams()
	p.ActiveUsers = 4
	p.UsedTotal = 900
	p.UserUsed = 100 // the other three consumed 800 between them

	before := p
	before.Elapsed = 0.5 // same window, before the happy hour opens
	nominal := ComputeFairShare(before)

	got := ComputeFairShare(p)
	if got.Allowance < nominal.Allowance {
		t.Errorf("allowance = %d during the happy hour, want at least the nominal %d", got.Allowance, nominal.Allowance)
	}
	if got.Mode == FairShareModeHappyHour {
		t.Errorf("mode = %q, want the nominal mode when the leftover share is narrower", got.Mode)
	}
}

func TestComputeFairShare_HappyHourNeverAllocatesZero(t *testing.T) {
	// An integer split of a nearly exhausted leftover rounds to zero, and a zero
	// allowance refuses every user — including one who has consumed nothing.
	p := happyHourParams()
	p.ActiveUsers = 4
	p.UsedTotal = 999
	p.UserUsed = 0

	if got := ComputeFairShare(p); got.Allowance < 1 {
		t.Errorf("allowance = %d, want at least 1 so a request can still go through", got.Allowance)
	}
}

func TestComputeFairShare_HappyHourAllowanceDoesNotGrowAsItIsConsumed(t *testing.T) {
	// The check that guards a budget compares usage against the allowance, so an
	// allowance derived from that same usage can never fire. Consuming up to the
	// allowance and recomputing must not raise it.
	p := sharedHappyHourParams()

	first := ComputeFairShare(p).Allowance

	// The user consumes up to their allowance; nobody else moves.
	p.UsedTotal += first - p.UserUsed
	p.UserUsed = first

	if second := ComputeFairShare(p).Allowance; second > first {
		t.Errorf("allowance rose from %d to %d as it was consumed, want it stable", first, second)
	}

	// Iterating the grant must stay bounded by the share, not drift up to the
	// whole budget one request at a time.
	for range 20 {
		alloc := ComputeFairShare(p).Allowance
		if p.UserUsed >= alloc {
			break
		}
		p.UsedTotal += alloc - p.UserUsed
		p.UserUsed = alloc
	}
	if p.UserUsed > 250 {
		t.Errorf("user ended up holding %d of a 1000 budget shared with 3 others, want it bounded near their share", p.UserUsed)
	}
}

func TestComputeFairShare_HappyHourGivesTheWholeLeftoverToASoleUser(t *testing.T) {
	// The point of the happy hour: with nobody else active, what would be
	// destroyed at the reset goes to the one user who is there.
	p := happyHourParams()
	p.ActiveUsers = 1
	p.UsedTotal = 200
	p.UserUsed = 200

	if got := ComputeFairShare(p); got.Allowance != p.Budget {
		t.Errorf("allowance = %d, want the whole budget %d", got.Allowance, p.Budget)
	}
}

func TestComputeFairShare_ThrottledOnlyWhenTheShareActuallyNarrows(t *testing.T) {
	// A plan reserving its whole budget has no commons to close: reporting it as
	// throttled would put a "narrowed share" badge on an unchanged allowance.
	p := FairShareParams{
		Budget:         1000,
		TotalMembers:   10,
		ActiveUsers:    2,
		UsedTotal:      900,
		Elapsed:        0.1,
		WindowDuration: 5 * time.Hour,
		Reserve:        1,
		Slack:          DefaultPaceSlack,
		HappyHourStart: DefaultHappyHourStart,
	}

	got := ComputeFairShare(p)
	if got.Mode == FairShareModeThrottled {
		t.Errorf("mode = %q, want no throttling when the whole budget is reserved", got.Mode)
	}
	if got.Allowance != 100 {
		t.Errorf("allowance = %d, want the untouched guaranteed floor", got.Allowance)
	}
}

func TestComputeFairShare_HappyHourIsBoundedByAnAbsoluteLead(t *testing.T) {
	// The threshold is a fraction of the window, so on a weekly plan its last
	// tenth is nearly 17 hours — far too early to claim the leftover is about to
	// be destroyed. The absolute lead is what keeps it honest.
	p := happyHourParams()
	p.WindowDuration = 168 * time.Hour

	if got := ComputeFairShare(p); got.Mode == FairShareModeHappyHour {
		t.Errorf("mode = %q, want the happy hour held back 8h before a weekly reset", got.Mode)
	}

	// Within the last hour of that same weekly window, it opens.
	p.Elapsed = 1 - float64(30*time.Minute)/float64(168*time.Hour)
	if got := ComputeFairShare(p); got.Mode != FairShareModeHappyHour {
		t.Errorf("mode = %q, want happy_hour 30 minutes before the reset", got.Mode)
	}
}

func TestComputeFairShare_AllocationIsNotMonotonic(t *testing.T) {
	// Documented consequence of allocating against the users active at decision
	// time while consumption accumulates over the window: a user alone on the
	// plan may legitimately spend a wide share and be denied once others arrive,
	// even though the plan is only half consumed. The dashboard reports the
	// active-user count precisely so this can be accounted for.
	alone := FairShareParams{
		Budget:         1000,
		TotalMembers:   20,
		ActiveUsers:    1,
		Elapsed:        -1,
		Reserve:        DefaultReserveRatio,
		Slack:          DefaultPaceSlack,
		HappyHourStart: DefaultHappyHourStart,
	}
	granted := ComputeFairShare(alone).Allowance
	if granted < 500 {
		t.Fatalf("allowance alone = %d, want a wide share", granted)
	}

	crowded := alone
	crowded.ActiveUsers = 2
	crowded.UsedTotal = 500
	narrowed := ComputeFairShare(crowded).Allowance
	if narrowed >= 500 {
		t.Fatalf("allowance with two active = %d, want it narrowed below what was already spent", narrowed)
	}
}

func TestComputeFairShare_NonPositiveHappyHourStartIsIgnored(t *testing.T) {
	// A zero or negative threshold would put every window permanently in happy
	// hour, silently disabling the whole allocation.
	zero := 0.0
	c := PlanConstraint{Kind: ConstraintRollingWindow, Duration: PlanDuration(5 * time.Hour), HappyHourStart: &zero}

	p := c.FairShareParamsFor(FairShareParams{Budget: 1000, TotalMembers: 10, ActiveUsers: 1, Elapsed: 0.1})
	if p.HappyHourStart != DefaultHappyHourStart {
		t.Errorf("HappyHourStart = %v, want the default %v", p.HappyHourStart, DefaultHappyHourStart)
	}
	if got := ComputeFairShare(p); got.Mode == FairShareModeHappyHour {
		t.Error("a non-positive threshold must not put the window in permanent happy hour")
	}
}

func TestComputeFairShare_NeverAllocatesZero(t *testing.T) {
	// A huge org on a tiny budget: integer division would floor every share to 0
	// and lock the org out of a plan it pays for.
	p := FairShareParams{
		Budget:         10,
		TotalMembers:   10_000,
		ActiveUsers:    10_000,
		Elapsed:        -1,
		Reserve:        DefaultReserveRatio,
		Slack:          DefaultPaceSlack,
		HappyHourStart: DefaultHappyHourStart,
	}

	if got := ComputeFairShare(p); got.Allowance < 1 {
		t.Errorf("allowance = %d, want at least 1", got.Allowance)
	}
}

func TestComputeFairShare_MoreActiveThanMembers(t *testing.T) {
	// A stale member count must not make the commons look wider than the
	// competition for it.
	p := FairShareParams{
		Budget:         1000,
		TotalMembers:   2,
		ActiveUsers:    50,
		Elapsed:        -1,
		Reserve:        DefaultReserveRatio,
		Slack:          DefaultPaceSlack,
		HappyHourStart: DefaultHappyHourStart,
	}

	got := ComputeFairShare(p)
	// Active is clamped to the member count: 0.3×1000/2 + 0.7×1000/2 = 500.
	if got.Allowance != 500 {
		t.Errorf("allowance = %d, want 500", got.Allowance)
	}
}

func TestPlanConstraint_FairShareParamsFor_Defaults(t *testing.T) {
	c := PlanConstraint{Kind: ConstraintRollingWindow, Duration: PlanDuration(5 * time.Hour)}

	p := c.FairShareParamsFor(FairShareParams{Budget: 100})
	if p.Reserve != DefaultReserveRatio || p.Slack != DefaultPaceSlack || p.HappyHourStart != DefaultHappyHourStart {
		t.Errorf("defaults = (%v, %v, %v), want (%v, %v, %v)",
			p.Reserve, p.Slack, p.HappyHourStart, DefaultReserveRatio, DefaultPaceSlack, DefaultHappyHourStart)
	}
	if p.Budget != 100 {
		t.Errorf("Budget = %d, want the caller's value untouched", p.Budget)
	}

	reserve, slack, happy := 0.5, 0.0, 0.8
	c.ReserveRatio, c.PaceSlack, c.HappyHourStart = &reserve, &slack, &happy
	p = c.FairShareParamsFor(FairShareParams{})
	if p.Reserve != reserve || p.Slack != slack || p.HappyHourStart != happy {
		t.Errorf("overrides = (%v, %v, %v), want (%v, %v, %v)", p.Reserve, p.Slack, p.HappyHourStart, reserve, slack, happy)
	}
}

func TestPlanConstraint_ElapsedFraction(t *testing.T) {
	anchor := mustTime(t, "2026-07-01T05:00:00Z")
	sliding := PlanConstraint{Kind: ConstraintRollingWindow, Duration: PlanDuration(5 * time.Hour)}
	if got := sliding.ElapsedFraction(mustTime(t, "2026-07-01T06:00:00Z")); got >= 0 {
		t.Errorf("sliding window elapsed = %v, want negative", got)
	}

	anchored := sliding
	anchored.WindowAnchor = &anchor
	cases := []struct {
		now  string
		want float64
	}{
		{"2026-07-01T05:00:00Z", 0},
		{"2026-07-01T06:15:00Z", 0.25},
		{"2026-07-01T07:30:00Z", 0.5},
		{"2026-07-01T09:59:59Z", 0.999},
	}
	for _, tc := range cases {
		got := anchored.ElapsedFraction(mustTime(t, tc.now))
		if diff := got - tc.want; diff > 0.001 || diff < -0.001 {
			t.Errorf("now=%s: elapsed = %v, want ~%v", tc.now, got, tc.want)
		}
	}
}
