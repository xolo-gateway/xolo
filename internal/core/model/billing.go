package model

import (
	"encoding/json"
	"time"
)

type BillingMode string

const (
	BillingModePayg         BillingMode = "payg"
	BillingModeSubscription BillingMode = "subscription"
)

type PlanConstraintKind string

const (
	ConstraintRollingWindow PlanConstraintKind = "rolling_window"
	ConstraintConcurrency   PlanConstraintKind = "concurrency"
)

// PlanDuration wraps time.Duration with human-readable JSON marshaling ("5h", "168h", "30m", etc.).
type PlanDuration time.Duration

func (d PlanDuration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *PlanDuration) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = PlanDuration(dur)
	return nil
}

func (d PlanDuration) Duration() time.Duration { return time.Duration(d) }

// PlanConstraint describes a single limit within a subscription plan.
// Fields used depend on Kind:
//   - rolling_window: Duration (e.g. "5h", "168h"), TokenBudget (optional), ValueBudget (optional, microcents in provider currency)
//   - concurrency:    MaxConcurrent
type PlanConstraint struct {
	Kind          PlanConstraintKind `json:"kind"`
	Label         string             `json:"label"`
	Duration      PlanDuration       `json:"duration,omitempty"`
	TokenBudget   *int64             `json:"token_budget,omitempty"`
	ValueBudget   *int64             `json:"value_budget,omitempty"` // microcents in provider currency
	MaxConcurrent *int               `json:"max_concurrent,omitempty"`
	// ReserveRatio, PaceSlack and HappyHourStart tune the per-user fair-share
	// allocator (see ComputeFairShare); nil means the package default.
	ReserveRatio   *float64 `json:"reserve_ratio,omitempty"`
	PaceSlack      *float64 `json:"pace_slack,omitempty"`
	HappyHourStart *float64 `json:"happy_hour_start,omitempty"`
	// WindowAnchor aligns a rolling_window constraint on a fixed (tumbling) schedule.
	// It records any instant at which a window opened; combined with Duration it lets us
	// compute the current window boundaries so they match the upstream provider's real
	// reset schedule (e.g. MiniMax "Resets in 4h29"). When nil, the window behaves as a
	// continuous sliding window (now - Duration).
	WindowAnchor *time.Time `json:"window_anchor,omitempty"`
}

// IsAnchored reports whether the constraint uses a fixed (tumbling) window aligned on a
// manual anchor, as opposed to a continuous sliding window.
func (c PlanConstraint) IsAnchored() bool {
	return c.WindowAnchor != nil && c.Duration.Duration() > 0
}

// CurrentWindowStart returns the start instant of the tumbling window containing `now`,
// aligned on WindowAnchor. When no anchor is set it falls back to a sliding window
// (now - Duration). Returns the zero time when Duration is not positive.
func (c PlanConstraint) CurrentWindowStart(now time.Time) time.Time {
	dur := c.Duration.Duration()
	if dur <= 0 {
		return time.Time{}
	}
	if c.WindowAnchor == nil {
		return now.Add(-dur)
	}
	anchor := *c.WindowAnchor
	elapsed := now.Sub(anchor)
	n := elapsed / dur
	if elapsed < 0 && elapsed%dur != 0 {
		n-- // floor toward negative infinity
	}
	return anchor.Add(n * dur)
}

// NextResetAt returns the instant at which the current tumbling window resets. It is only
// meaningful for anchored windows; sliding windows never reset atomically, so it returns
// the zero time when no anchor is set.
func (c PlanConstraint) NextResetAt(now time.Time) time.Time {
	if !c.IsAnchored() {
		return time.Time{}
	}
	ws := c.CurrentWindowStart(now)
	if ws.IsZero() {
		return time.Time{}
	}
	return ws.Add(c.Duration.Duration())
}

// SubscriptionPlan describes the limits attached to a subscription-billed provider.
type SubscriptionPlan struct {
	Label       string           `json:"label"`
	Constraints []PlanConstraint `json:"constraints"`
}

// ── Fair-share allocation ───────────────────────────────────────────────────

// Default tuning of the fair-share allocator, used when a constraint leaves the
// corresponding field nil.
const (
	DefaultReserveRatio   = 0.3
	DefaultPaceSlack      = 0.15
	DefaultHappyHourStart = 0.9
)

// FairShareMode explains which rule produced a user's allowance. It is surfaced
// in denial messages and logs so a fluctuating allowance stays explainable.
type FairShareMode string

const (
	// FairShareModeShared is the nominal regime: guaranteed floor plus a full
	// share of the commons, split between the users active in the window.
	FairShareModeShared FairShareMode = "shared"
	// FairShareModeThrottled means the plan is consuming its budget faster than
	// the window elapses, so the commons is being closed back toward the floor.
	FairShareModeThrottled FairShareMode = "throttled"
	// FairShareModeHappyHour means the window is about to reset: whatever is left
	// would be destroyed, so the whole budget is opened to whoever shows up.
	FairShareModeHappyHour FairShareMode = "happy_hour"
)

// FairShareParams describes the state of a plan constraint at decision time.
type FairShareParams struct {
	// Budget is the plan-wide budget for the window (tokens or microcents).
	Budget int64
	// TotalMembers is the number of members of the org, active or not. It sizes
	// the guaranteed floor, so that a member who shows up late still has room.
	TotalMembers int
	// ActiveUsers is the number of distinct users who consumed in the current
	// window. It sizes the shared part: the fewer users compete, the larger the
	// share of each.
	ActiveUsers int
	// UsedTotal is the plan-wide consumption in the current window.
	UsedTotal int64
	// Elapsed is the fraction of the window already elapsed, in [0,1]. It is
	// negative for a sliding window, which never resets and therefore has
	// neither pacing nor happy hour.
	Elapsed float64
	// Reserve is the fraction of the budget kept out of the commons and split
	// between all members as a guaranteed floor.
	Reserve float64
	// Slack is how far ahead of the elapsed fraction the plan may burn before
	// the commons starts closing.
	Slack float64
	// HappyHourStart is the elapsed fraction past which the leftover budget is
	// opened to everyone. A value >= 1 disables the happy hour.
	HappyHourStart float64
}

// FairShareAllocation is the outcome of the allocator for a single user.
type FairShareAllocation struct {
	// Allowance is how much of the budget this user may consume in the window.
	Allowance int64
	Mode      FairShareMode
}

// ComputeFairShare returns how much of a plan budget a single user may consume
// in the current window.
//
// The allocation is the sum of two parts:
//
//	allowance = reserve×B/members  +  (1−reserve)×B/active × pacingFactor
//
// The first term is a floor every member holds whether they use it or not, so a
// quiet user is never squeezed out by a heavy one. The second is the commons,
// split only between the users who actually show up — this is what recovers the
// budget that the static budget/members cap used to leave on the table.
//
// The pacing factor keeps the commons from being drained in the first minutes of
// a window: it stays at 1 as long as plan-wide consumption trails the elapsed
// fraction of the window (plus some slack), then decreases linearly to 0 as the
// budget fills up, collapsing the allowance back onto the guaranteed floor.
//
// Past HappyHourStart the remaining budget would be destroyed by the upcoming
// reset, so it is handed out in full rather than protected for members who did
// not come. Both pacing and happy hour require a window that actually resets;
// for a sliding window (Elapsed < 0) only the two-part split applies.
func ComputeFairShare(p FairShareParams) FairShareAllocation {
	if p.Budget <= 0 {
		return FairShareAllocation{Allowance: 0, Mode: FairShareModeShared}
	}

	anchored := p.Elapsed >= 0
	if anchored && p.HappyHourStart < 1 && p.Elapsed >= p.HappyHourStart {
		return FairShareAllocation{Allowance: p.Budget, Mode: FairShareModeHappyHour}
	}

	reserve := clampUnit(p.Reserve)
	members := max(p.TotalMembers, 1)
	active := max(p.ActiveUsers, 1)
	// A user counted as active is necessarily a member; a stale member count
	// must not make the commons look narrower than the competition for it.
	active = min(active, members)

	budget := float64(p.Budget)
	guaranteed := reserve * budget / float64(members)
	commons := (1 - reserve) * budget / float64(active)

	mode := FairShareModeShared
	if anchored {
		if factor := pacingFactor(float64(p.UsedTotal)/budget, p.Elapsed, p.Slack); factor < 1 {
			commons *= factor
			mode = FairShareModeThrottled
		}
	}

	allowance := int64(guaranteed + commons)
	if allowance < 1 {
		// Never hand out a zero allowance: a budgeted plan must always let a
		// request through, otherwise the org locks itself out of its own plan.
		allowance = 1
	}

	return FairShareAllocation{Allowance: allowance, Mode: mode}
}

// pacingFactor returns how much of the commons stays open, given how far plan
// consumption (pace) runs ahead of the elapsed fraction of the window. It is 1
// while the plan burns no faster than the clock, then decreases linearly to 0 as
// the budget is exhausted.
func pacingFactor(pace, elapsed, slack float64) float64 {
	budgetLine := clampUnit(elapsed) + max(slack, 0)
	if budgetLine >= 1 || pace <= budgetLine {
		return 1
	}
	return clampUnit((1 - pace) / (1 - budgetLine))
}

func clampUnit(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// FairShareParamsFor fills the tuning fields of p from the constraint, applying
// the defaults for every field the plan leaves unset.
func (c PlanConstraint) FairShareParamsFor(p FairShareParams) FairShareParams {
	p.Reserve = DefaultReserveRatio
	if c.ReserveRatio != nil {
		p.Reserve = *c.ReserveRatio
	}
	p.Slack = DefaultPaceSlack
	if c.PaceSlack != nil {
		p.Slack = *c.PaceSlack
	}
	p.HappyHourStart = DefaultHappyHourStart
	if c.HappyHourStart != nil {
		p.HappyHourStart = *c.HappyHourStart
	}
	return p
}

// ElapsedFraction returns how much of the current window has elapsed, in [0,1].
// It returns -1 for a sliding window, which has no reset to pace against.
func (c PlanConstraint) ElapsedFraction(now time.Time) float64 {
	if !c.IsAnchored() {
		return -1
	}
	dur := c.Duration.Duration()
	start := c.CurrentWindowStart(now)
	if start.IsZero() || dur <= 0 {
		return -1
	}
	return clampUnit(float64(now.Sub(start)) / float64(dur))
}
