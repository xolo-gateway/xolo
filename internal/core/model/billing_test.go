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

func TestComputeFairShare_HappyHourOpensTheLeftover(t *testing.T) {
	// Past the happy-hour threshold the remaining budget would be destroyed by
	// the reset, so it is handed out in full.
	p := FairShareParams{
		Budget:         1000,
		TotalMembers:   20,
		ActiveUsers:    1,
		UsedTotal:      200,
		Elapsed:        0.95,
		Reserve:        DefaultReserveRatio,
		Slack:          DefaultPaceSlack,
		HappyHourStart: DefaultHappyHourStart,
	}

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
