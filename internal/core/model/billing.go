package model

import (
	"encoding/json"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
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

// PlanConstraintScope tells what a rolling_window constraint counts.
type PlanConstraintScope string

const (
	// PlanScopeProvider counts every model of the provider against one budget.
	// It is the scope of a constraint that sets none.
	PlanScopeProvider PlanConstraintScope = "provider"
	// PlanScopeModel gives each upstream model its own budget and its own
	// window, as plans that meter every model separately do.
	PlanScopeModel PlanConstraintScope = "model"
	// PlanScopeShared counts every model of the provider against one budget,
	// each at its own rate: a model whose own budget (ModelBudgets) is twice
	// the constraint's counts for half of what it consumes. Plans that publish
	// a usage limit per model but meter a single window work this way (OpenCode
	// Go): the per-model limit is what the whole window holds when spent on
	// that model alone.
	PlanScopeShared PlanConstraintScope = "shared"
)

// PlanBudget is the budget of one upstream model on a per-model or shared
// constraint. A nil field leaves that dimension unlimited for the model.
type PlanBudget struct {
	TokenBudget *int64 `json:"token_budget,omitempty"`
	ValueBudget *int64 `json:"value_budget,omitempty"` // microcents in provider currency
}

// PlanConstraint describes a single limit within a subscription plan.
// Fields used depend on Kind:
//   - rolling_window: Duration (e.g. "5h", "168h"), TokenBudget (optional), ValueBudget (optional, microcents in provider currency),
//     Scope and ModelBudgets (optional, per-model metering)
//   - concurrency:    MaxConcurrent
type PlanConstraint struct {
	Kind          PlanConstraintKind `json:"kind"`
	Label         string             `json:"label"`
	Duration      PlanDuration       `json:"duration,omitempty"`
	TokenBudget   *int64             `json:"token_budget,omitempty"`
	ValueBudget   *int64             `json:"value_budget,omitempty"` // microcents in provider currency
	MaxConcurrent *int               `json:"max_concurrent,omitempty"`
	// Scope is what a rolling_window constraint counts: the whole provider
	// (empty or PlanScopeProvider), each upstream model on its own
	// (PlanScopeModel), or the whole provider with each model weighted by its
	// own budget (PlanScopeShared). Concurrency constraints are always
	// provider-wide.
	Scope PlanConstraintScope `json:"scope,omitempty"`
	// ModelBudgets overrides, on a per-model or shared constraint, the budgets
	// of the upstream models named by their real model name. An override
	// replaces TokenBudget and ValueBudget as a whole: a model listed with no
	// budget is unlimited, which is how a plan's free models are described.
	// Models not listed take the constraint's own budgets. On a shared
	// constraint they only set the rate at which each model fills the single
	// budget of the constraint (see Weigh).
	ModelBudgets map[string]PlanBudget `json:"model_budgets,omitempty"`
	// ReserveRatio, PaceSlack, HappyHourStart and HappyHourMaxLead tune the
	// per-user fair-share allocator (see ComputeFairShare); nil means the package
	// default.
	ReserveRatio   *float64 `json:"reserve_ratio,omitempty"`
	PaceSlack      *float64 `json:"pace_slack,omitempty"`
	HappyHourStart *float64 `json:"happy_hour_start,omitempty"`
	// HappyHourMaxLead caps how long before the reset the happy hour may open.
	// HappyHourStart is a fraction of the window, so on a long window it would
	// otherwise open for hours: a tenth of a 168h window is nearly 17 hours, over
	// which "the leftover is about to be destroyed" stops being true. Nil means
	// DefaultHappyHourLead of the window.
	HappyHourMaxLead *PlanDuration `json:"happy_hour_max_lead,omitempty"`
	// ShareThreshold is the fraction of the budget the plan may fill before
	// the users' shares apply: below it any user may consume, the plan having
	// room for everyone. Nil or zero applies the shares from the first request.
	ShareThreshold *float64 `json:"share_threshold,omitempty"`
	// WindowAnchor aligns a rolling_window constraint on a fixed (tumbling) schedule.
	// It records any instant at which a window opened; combined with Duration it lets us
	// compute the current window boundaries so they match the upstream provider's real
	// reset schedule (e.g. MiniMax "Resets in 4h29"). When nil, the window behaves as a
	// continuous sliding window (now - Duration).
	WindowAnchor *time.Time `json:"window_anchor,omitempty"`
}

// PerModel reports whether the constraint meters each upstream model on its own.
func (c PlanConstraint) PerModel() bool {
	return c.Kind == ConstraintRollingWindow && c.Scope == PlanScopeModel
}

// Shared reports whether the constraint counts every model against one budget,
// each weighted by its own budget.
func (c PlanConstraint) Shared() bool {
	return c.Kind == ConstraintRollingWindow && c.Scope == PlanScopeShared
}

// Weigh converts the consumption of one upstream model into the units of a
// shared constraint's budgets: tokens and value are scaled by the ratio of the
// constraint's budget to the model's own, so that spending a model's whole
// budget fills the constraint's. A model listed without a budget of a kind
// weighs nothing on it. A model not listed, a dimension the constraint does
// not limit, and any constraint that is not shared keep the figures as they
// are.
func (c PlanConstraint) Weigh(realModel string, tokens, value int64) (int64, int64) {
	b, ok := c.ModelBudgets[realModel]
	if !c.Shared() || !ok {
		return tokens, value
	}
	return weighUsage(tokens, c.TokenBudget, b.TokenBudget), weighUsage(value, c.ValueBudget, b.ValueBudget)
}

// WeighUnknown converts the consumption of a model whose upstream model is
// unknown, deleted since or not listed, into the units of a shared
// constraint's budgets. It takes the highest rate among the listed models, and
// at least the constraint's own: the window then closes early rather than
// late, whichever model it was.
func (c PlanConstraint) WeighUnknown(tokens, value int64) (int64, int64) {
	if !c.Shared() {
		return tokens, value
	}
	return weighUnknown(tokens, c.TokenBudget, c.ModelBudgets, func(b PlanBudget) *int64 { return b.TokenBudget }),
		weighUnknown(value, c.ValueBudget, c.ModelBudgets, func(b PlanBudget) *int64 { return b.ValueBudget })
}

func weighUnknown(used int64, budget *int64, budgets map[string]PlanBudget, of func(PlanBudget) *int64) int64 {
	if budget == nil {
		return used
	}
	// The smallest model budget weighs the most; the constraint's own budget
	// bounds it, so the rate is never below 1.
	smallest := *budget
	for _, b := range budgets {
		if mb := of(b); mb != nil && *mb > 0 && *mb < smallest {
			smallest = *mb
		}
	}
	return weighUsage(used, budget, &smallest)
}

func weighUsage(used int64, budget, modelBudget *int64) int64 {
	switch {
	case budget == nil:
		return used
	case modelBudget == nil:
		return 0
	case *modelBudget <= 0:
		// Validation refuses a zero model budget on a shared constraint: it
		// would weigh infinitely. Counting the usage as is keeps a stored plan
		// that slipped through from dividing by zero.
		return used
	}
	return int64(math.Round(float64(used) * float64(*budget) / float64(*modelBudget)))
}

// ForModel returns the constraint as it applies to one upstream model: its
// budgets are the model's override when there is one. A provider-wide
// constraint is returned unchanged.
func (c PlanConstraint) ForModel(realModel string) PlanConstraint {
	if !c.PerModel() {
		return c
	}
	if b, ok := c.ModelBudgets[realModel]; ok {
		c.TokenBudget, c.ValueBudget = b.TokenBudget, b.ValueBudget
	}
	c.ModelBudgets = nil
	return c
}

// HasSharedConstraint reports whether one of the plan's constraints weighs
// each model by its own budget, which needs the provider's models to resolve.
func (p *SubscriptionPlan) HasSharedConstraint() bool {
	if p == nil {
		return false
	}
	return slices.ContainsFunc(p.Constraints, PlanConstraint.Shared)
}

// HasPerModelConstraint reports whether one of the plan's constraints meters
// each upstream model on its own.
func (p *SubscriptionPlan) HasPerModelConstraint() bool {
	if p == nil {
		return false
	}
	for _, c := range p.Constraints {
		if c.PerModel() {
			return true
		}
	}
	return false
}

// PlanUsageScope identifies the subscription-covered usage a plan constraint
// counts: the consumption of one provider by one organization, narrowed to a
// set of models when the constraint is per model.
type PlanUsageScope struct {
	OrgID      OrgID
	ProviderID ProviderID
	// ModelIDs narrows the count to these models; empty counts the whole
	// provider. A per-model constraint lists every model of the provider that
	// targets the same upstream model, since the provider meters them as one.
	ModelIDs []LLMModelID
	// ByKey narrows the count to the usage of one API key of the provider,
	// KeyID, when the provider spreads its plan over several keys. Without it
	// the count covers every key.
	ByKey bool
	KeyID ProviderKeyID
	// KeyWindows counts the usage of these keys from their own window start
	// instead of the one the count is asked from: keys whose subscription
	// renews on another day (see KeyWindows). The other keys keep the asked
	// start.
	KeyWindows map[ProviderKeyID]time.Time
}

// Since is the earliest instant the scope counts usage from, for a count asked
// from since: a key window may start before it.
func (s PlanUsageScope) Since(since time.Time) time.Time {
	for _, start := range s.KeyWindows {
		if start.Before(since) {
			since = start
		}
	}
	return since
}

// UpstreamModelIDs returns the ids of the models, among models, that m's
// provider serves under the same upstream model as m, m always included. The
// provider meters them as one whatever proxy name they are published under.
func UpstreamModelIDs(models []LLMModel, m LLMModel) []LLMModelID {
	ids := []LLMModelID{m.ID()}
	for _, other := range models {
		if other.ID() != m.ID() && other.ProviderID() == m.ProviderID() && other.RealModel() == m.RealModel() {
			ids = append(ids, other.ID())
		}
	}
	return ids
}

// Key identifies the scope in cache keys. The model ids are sorted, so two
// scopes naming the same models in another order share a key.
func (s PlanUsageScope) Key() string {
	ids := make([]string, len(s.ModelIDs))
	for i, id := range s.ModelIDs {
		ids[i] = string(id)
	}
	slices.Sort(ids)
	key := string(s.OrgID) + "|" + string(s.ProviderID) + "|" + strings.Join(ids, ",")
	if s.ByKey {
		key += "|key:" + string(s.KeyID)
	}
	for _, k := range slices.Sorted(maps.Keys(s.KeyWindows)) {
		key += "|" + string(k) + "@" + strconv.FormatInt(s.KeyWindows[k].UnixNano(), 10)
	}
	return key
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

	// The default happy-hour lead is a fraction of the window, bounded on both
	// sides. A fixed lead does not fit every window: an hour is a third of a 5h
	// plan's last stretch but leaves a weekly plan's absent-member floors to be
	// destroyed unless someone shows up in the final hour of seven days. A bare
	// fraction does not either — a tenth of a week is 17 hours during which
	// "about to be destroyed" is simply not true. Hence a ratio, clamped.
	DefaultHappyHourLeadRatio = 0.05
	MinHappyHourLead          = 30 * time.Minute
	MaxHappyHourLead          = 4 * time.Hour
)

// DefaultHappyHourLead returns the lead used when a constraint sets none:
// DefaultHappyHourLeadRatio of the window, clamped to [MinHappyHourLead,
// MaxHappyHourLead]. A 5h window opens 30 minutes before its reset, a weekly
// one 4 hours before.
func DefaultHappyHourLead(window time.Duration) time.Duration {
	lead := time.Duration(float64(window) * DefaultHappyHourLeadRatio)
	if lead < MinHappyHourLead {
		return MinHappyHourLead
	}
	if lead > MaxHappyHourLead {
		return MaxHappyHourLead
	}
	return lead
}

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
	// would be destroyed, so it is shared out between the users who showed up.
	FairShareModeHappyHour FairShareMode = "happy_hour"
	// FairShareModeCapped means the share was cut to what the plan can still hand
	// out, and below the static budget/members share: the other active users
	// have already consumed most of it, or the absent members' floors hold what
	// is left. A cap that leaves the share above the static one is not reported;
	// the user still has more than the old cap gave them. On a sliding window,
	// where there is no pacing, this is the only reason a share ever narrows.
	FairShareModeCapped FairShareMode = "capped"
	// FairShareModeOpen means the plan is still under its share threshold:
	// with room for everyone, the user may consume up to that threshold
	// whatever their share.
	FairShareModeOpen FairShareMode = "open"
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
	// UserUsed is what the user being allocated already consumed in the window.
	// Only the happy hour and the share threshold read it, to hand out room on
	// top of what each user already holds.
	UserUsed int64
	// WindowDuration is the length of the window, used to turn the happy-hour
	// fraction into a real duration. Zero disables the happy hour.
	WindowDuration time.Duration
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
	// shared out. A value >= 1 disables the happy hour.
	HappyHourStart float64
	// HappyHourMaxLead caps how long before the reset the happy hour may open,
	// regardless of the fraction.
	HappyHourMaxLead time.Duration
	// ShareThreshold is the fraction of the budget under which the plan is
	// open to anyone (see PlanConstraint.ShareThreshold). Zero disables it.
	ShareThreshold float64
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
// quiet user is never squeezed out by a heavy one. It is a reservation, not just
// a share: the commons is capped by what the plan can still hand out once the
// absent members' floors are set aside, so shares granted in sequence cannot add
// up past the budget. The second term is that commons, split only between the
// users who actually show up — this is what recovers the budget that the static
// budget/members cap used to leave on the table.
//
// The pacing factor keeps the commons from being drained in the first minutes of
// a window: it stays at 1 as long as plan-wide consumption trails the elapsed
// fraction of the window (plus some slack), then decreases linearly to 0 as the
// budget fills up, collapsing the allowance back onto the guaranteed floor.
//
// While the plan as a whole stays under its share threshold, the shares do not
// apply at all: any user may consume until the plan reaches it, the plan
// having room for everyone. Past it, the allocation below takes over.
//
// In the last moments of a window the leftover budget would be destroyed by the
// upcoming reset, so it is shared out between the active users on top of what
// they already hold, rather than kept for members who did not come. Both pacing
// and happy hour require a window that actually resets; for a sliding window
// (Elapsed < 0) only the two-part split applies.
//
// The allocation is not monotonic: it is decided against the number of users
// active at that instant, while consumption accumulates over the whole window.
// A user alone on the plan may consume the wide share they were granted and then
// be denied for the rest of the window once others show up and the share narrows.
// This is deliberate — the alternative is to keep honouring a share the plan can
// no longer afford — but it means a user can watch their own gauge fill without
// consuming anything, which is why the dashboard reports the active-user count.
func ComputeFairShare(p FairShareParams) FairShareAllocation {
	if p.Budget <= 0 {
		return FairShareAllocation{Allowance: 0, Mode: FairShareModeShared}
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

	anchored := p.Elapsed >= 0
	mode := FairShareModeShared
	if anchored && commons > 0 {
		// Only a pacing factor that actually narrows the allowance is reported as
		// throttling: a plan reserving its whole budget has no commons to close,
		// and a "Part réduite" badge on an unchanged share is the kind of
		// unexplainable signal the modes exist to avoid.
		if factor := pacingFactor(float64(p.UsedTotal)/budget, p.Elapsed, p.Slack); factor < 1 {
			commons *= factor
			mode = FairShareModeThrottled
		}
	}

	// Hard cap on what is left to hand out. Without it the guaranteed floor is a
	// share, not a reservation: shares granted one after another add up past the
	// budget, and the quiet member the floor exists for finds the plan empty.
	if allocatable := p.allocatableCommons(guaranteed, members, active); allocatable < commons {
		commons = allocatable
		// The cap binds on almost every plan where few members are active: the
		// absent members' floors take a large slice of the budget, so a modest
		// consumption by the others is enough to trim the commons. Reporting that
		// as a warning would badge the nominal state of the very orgs this
		// allocation is for. It is only worth explaining once the share drops
		// below what the static budget/members cap used to grant, since above
		// that line the user still has more than before.
		// Pacing keeps precedence when both rules bite: "your share reopens
		// when consumption catches up with the clock" is the one explanation
		// the user can act on, and the screen shows a single note per gauge.
		if mode == FairShareModeShared && guaranteed+commons < budget/float64(members) {
			mode = FairShareModeCapped
		}
	}

	allowance := int64(guaranteed + commons)
	if allowance < 1 {
		// Never hand out a zero allowance: a budgeted plan must always let a
		// request through, otherwise the org locks itself out of its own plan.
		allowance = 1
	}

	// The happy hour only ever opens: it is taken when it widens the share, and
	// ignored when it would narrow it. Handing out a leftover that is about to be
	// destroyed must never be the reason a request is refused — and since it is
	// the nominal allowance that carries the floor above, deferring to it also
	// keeps an exhausted window from allocating zero to everyone.
	result := FairShareAllocation{Allowance: allowance, Mode: mode}
	if anchored && p.inHappyHour() {
		if hh := p.happyHourAllowance(int64(active)); hh > result.Allowance {
			result = FairShareAllocation{Allowance: hh, Mode: FairShareModeHappyHour}
		}
	}

	// Under the share threshold the plan has room for everyone: the shares
	// only take over once it fills past it. Like the happy hour this only ever
	// widens an allowance.
	if open := p.openAllowance(); open > result.Allowance {
		result = FairShareAllocation{Allowance: open, Mode: FairShareModeOpen}
	}
	return result
}

// openAllowance lets the user consume until the plan reaches its share
// threshold: what they already used, plus the room left under the threshold.
// The room is the plan's, not the user's: once the recorded usage reaches the
// threshold, no user is let through it any more. Requests are decided on the
// usage already recorded, so the ones in flight when it is crossed may take
// the plan past it by what they consume; the shares then apply to everyone.
// Zero when the threshold is unset or reached.
func (p FairShareParams) openAllowance() int64 {
	if p.ShareThreshold <= 0 || p.Budget <= 0 {
		return 0
	}
	room := int64(math.Round(clampUnit(p.ShareThreshold)*float64(p.Budget))) - p.UsedTotal
	if room <= 0 {
		return 0
	}
	return max(p.UserUsed, 0) + room
}

// inHappyHour reports whether the window is close enough to its reset for the
// leftover budget to be shared out. It takes both a fraction of the window and
// an absolute lead, so that a long window does not open for hours.
func (p FairShareParams) inHappyHour() bool {
	// A non-positive threshold is an unset one, never "open from the first
	// second": read as a real threshold it would disable the allocation for the
	// whole window. FairShareParamsFor already resolves it, but ComputeFairShare
	// is exported, so the invariant belongs here too.
	if p.HappyHourStart <= 0 || p.HappyHourStart >= 1 || p.WindowDuration <= 0 {
		return false
	}
	if p.Elapsed < p.HappyHourStart {
		return false
	}
	lead := p.HappyHourMaxLead
	if lead <= 0 {
		lead = DefaultHappyHourLead(p.WindowDuration)
	}
	remaining := time.Duration((1 - clampUnit(p.Elapsed)) * float64(p.WindowDuration))
	return remaining <= lead
}

// happyHourAllowance gives each active user an equal share of what the others
// have left. With a single active user this hands them the whole budget; with
// several it stops the first one to notice from taking it all.
//
// The share is computed from what the OTHER users consumed, never from the
// caller's own total: an allowance of the form "what I already used, plus a
// share of the rest" grows as it is consumed, so the check that compares usage
// to it can never fire.
// active is the clamped competitor count, passed in rather than read back from
// the params: both branches of one allocation must divide by the same number.
func (p FairShareParams) happyHourAllowance(active int64) int64 {
	if active < 1 {
		active = 1
	}
	othersUsed := p.UsedTotal - p.UserUsed
	if othersUsed < 0 {
		othersUsed = 0
	}
	available := p.Budget - othersUsed
	if available < 0 {
		available = 0
	}
	return available / active
}

// allocatableCommons is each active user's share of what the plan can still
// hand out: the budget, less what the other users already consumed, less the
// floors held for the members who have not shown up. Those floors are what makes
// the guarantee opposable — a member arriving late in the window still finds
// their share, instead of a plan drained by whoever came first.
func (p FairShareParams) allocatableCommons(guaranteed float64, members, active int) float64 {
	othersUsed := float64(p.UsedTotal - p.UserUsed)
	if othersUsed < 0 {
		othersUsed = 0
	}

	// The caller's own floor is granted separately, so it comes off the pot too:
	// leaving it in would let each arrival take their floor plus a slice of it,
	// and the sequence of allocations would creep past the budget.
	absentFloors := float64(members-active) * guaranteed
	available := float64(p.Budget) - othersUsed - absentFloors - guaranteed
	if available <= 0 {
		return 0
	}

	return available / float64(active)
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
	// A non-positive threshold would put every window permanently in happy hour,
	// disabling the allocation altogether; read it as "unset" instead.
	p.HappyHourStart = DefaultHappyHourStart
	if c.HappyHourStart != nil && *c.HappyHourStart > 0 {
		p.HappyHourStart = *c.HappyHourStart
	}
	if c.ShareThreshold != nil {
		p.ShareThreshold = *c.ShareThreshold
	}
	p.WindowDuration = c.Duration.Duration()
	p.HappyHourMaxLead = DefaultHappyHourLead(p.WindowDuration)
	if c.HappyHourMaxLead != nil && c.HappyHourMaxLead.Duration() > 0 {
		p.HappyHourMaxLead = c.HappyHourMaxLead.Duration()
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
