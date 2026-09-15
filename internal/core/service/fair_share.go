package service

import (
	"context"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"golang.org/x/sync/singleflight"
)

// PlanUsageReader is the narrow slice of port.UsageStore the fair-share
// allocation reads. It is satisfied by port.UsageStore and by any fake.
type PlanUsageReader interface {
	SumPlanUsageSince(ctx context.Context, orgID model.OrgID, providerID model.ProviderID, since time.Time) (tokens int64, providerValue int64, err error)
	SumUserPlanUsageSince(ctx context.Context, userID model.UserID, orgID model.OrgID, providerID model.ProviderID, since time.Time) (tokens int64, providerValue int64, err error)
	CountActivePlanUsersSince(ctx context.Context, orgID model.OrgID, providerID model.ProviderID, since time.Time, excludeUserID model.UserID) (int64, error)
	HasPlanUsageSince(ctx context.Context, userID model.UserID, orgID model.OrgID, providerID model.ProviderID, since time.Time) (bool, error)
}

// FairShareRequest describes whose share of which constraint is being resolved.
type FairShareRequest struct {
	OrgID       model.OrgID
	ProviderID  model.ProviderID
	UserID      model.UserID
	MemberCount int
	Constraint  model.PlanConstraint
	Now         time.Time
	// PlanUsage, when set, is the plan-wide consumption of the window as the
	// caller already read it. Callers that check the plan-wide budget before
	// allocating pass it so the window is aggregated once per request instead of
	// twice — this runs on the proxy hot path, per constraint.
	PlanUsage *PlanUsage
}

// PlanUsage is the plan-wide consumption of a window.
type PlanUsage struct {
	Tokens int64
	Value  int64
}

// FairShareResult is everything a caller needs both to decide and to explain the
// decision: the allowances, how each was produced, and the state they came from.
type FairShareResult struct {
	// TokenAllowance and ValueAllowance are nil when the constraint carries no
	// budget of that kind.
	TokenAllowance *int64
	ValueAllowance *int64
	TokenMode      model.FairShareMode
	ValueMode      model.FairShareMode

	// PlanTokens and PlanValue are the plan-wide consumption of the window.
	PlanTokens int64
	PlanValue  int64
	// UserTokens and UserValue are the requesting user's consumption of the window.
	UserTokens int64
	UserValue  int64

	// ActiveUsers is how many members are competing for the budget, the caller
	// included. It never exceeds MemberCount.
	ActiveUsers int
	// CountDegraded reports that the active-user count could not be read and that
	// the allocation fell back to "everybody is active", i.e. the static share.
	// Callers must say so rather than present ActiveUsers as a measurement.
	CountDegraded bool
}

// countActive returns how many users compete for the budget in the window, the
// caller included. degraded reports that the count could not be read, now or
// within the TTL.
//
// The plan-wide count is the expensive part, a DISTINCT over the whole window,
// and it is what the cache holds — one entry per plan and window, shared by
// every user on it. Whether the caller is already among the counted is a point
// lookup in the same index, asked on every call: a user whose first request has
// not been recorded yet is competing all the same, and inferring their presence
// from a zero usage sum was wrong, since a request can be recorded with no
// billable token. A failed lookup counts the caller in, which only narrows.
func (s *FairShareService) countActive(ctx context.Context, req FairShareRequest, since time.Time) (count int64, degraded bool) {
	total, degraded := s.countAll(ctx, req, since)
	if degraded {
		return 0, true
	}

	if !s.isPresent(ctx, req, since) {
		total++
	}

	return total, false
}

// isPresent reports whether the caller already consumed in the window. A
// positive answer is kept for the TTL: presence never turns false again within
// a window, so there is no reason to ask the store on every request of a user
// it has already seen. Absence is asked again every time, since it can end
// with this very request. A failed lookup reads as absent, which counts the
// caller in and only narrows.
func (s *FairShareService) isPresent(ctx context.Context, req FairShareRequest, since time.Time) bool {
	key := presenceKeyFor(req.OrgID, req.ProviderID, since, req.UserID, s.presence.ttl)
	if s.presence.has(key, req.Now) {
		return true
	}

	present, err := s.usageReader.HasPlanUsageSince(ctx, req.UserID, req.OrgID, req.ProviderID, since)
	if err != nil || !present {
		return false
	}

	s.presence.remember(key, req.Now)

	return true
}

// activeUserCountTimeout bounds the detached count: without the request's
// cancellation, this is what stops a stuck query from holding the singleflight.
const activeUserCountTimeout = 10 * time.Second

// countAll returns the plan-wide active-user count, from the cache when a recent
// read is available. A failure is cached like a value, so a database that cannot
// answer the count is spared the query on every request rather than asked again
// and again while it is struggling; concurrent reloads of one key are folded
// into a single query.
func (s *FairShareService) countAll(ctx context.Context, req FairShareRequest, since time.Time) (count int64, degraded bool) {
	key := activeUserKeyFor(req.OrgID, req.ProviderID, since, s.activeUsers.ttl)

	if entry, ok := s.activeUsers.get(key, req.Now); ok {
		return entry.count, entry.degraded
	}

	v, _, _ := s.inflight.Do(key.flightKey(), func() (any, error) {
		// Another caller may have filled the entry while this one waited.
		if entry, ok := s.activeUsers.get(key, req.Now); ok {
			return entry, nil
		}
		// Detached from the request: the result lands in an entry shared by
		// everyone on the plan, and a client hanging up mid-query is not a
		// reason to mark the plan degraded for a whole TTL. A failure here is
		// then the database's own, and worth caching.
		countCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), activeUserCountTimeout)
		defer cancel()
		n, err := s.usageReader.CountActivePlanUsersSince(countCtx, req.OrgID, req.ProviderID, since, "")
		entry := activeUserEntry{count: n, degraded: err != nil}
		s.activeUsers.put(key, entry, req.Now)
		return entry, nil
	})
	entry := v.(activeUserEntry)

	return entry.count, entry.degraded
}

// ErrFairShareNotApplicable is returned when there is no membership to divide a
// budget between, or no user to allocate for.
var ErrFairShareNotApplicable = errors.New("fair share: not applicable without a member count and a user")

// FairShareService resolves the per-user share of a subscription plan budget.
//
// It exists so that the enforcer and the screens that display the allocation
// decide from one implementation: a dashboard showing a denominator the enforcer
// does not apply is worse than showing nothing, and two copies of this wiring
// drift the moment one of them is touched.
type FairShareService struct {
	usageReader PlanUsageReader
	activeUsers *activeUserCache
	presence    *presenceCache
	// inflight serialises concurrent reloads of one count: when an entry
	// expires, every request on the plan would otherwise fire the same
	// COUNT(DISTINCT) at once.
	inflight singleflight.Group
}

func NewFairShareService(usageReader PlanUsageReader) *FairShareService {
	return NewFairShareServiceWithCacheTTL(usageReader, DefaultActiveUserCacheTTL)
}

// NewFairShareServiceWithCacheTTL builds a service whose active-user counts are
// reused for ttl. A non-positive ttl reads the count on every call.
func NewFairShareServiceWithCacheTTL(usageReader PlanUsageReader, ttl time.Duration) *FairShareService {
	return &FairShareService{
		usageReader: usageReader,
		activeUsers: newActiveUserCache(ttl),
		presence:    newPresenceCache(ttl),
	}
}

// Resolve computes the allocation for one user on one rolling-window constraint.
//
// Every degraded path narrows the allowance rather than widening it: losing the
// active-user count falls back to the static budget/members share, because an
// allocation handed out on missing data must not exceed what the plan can honour.
func (s *FairShareService) Resolve(ctx context.Context, req FairShareRequest) (*FairShareResult, error) {
	if req.MemberCount <= 0 || req.UserID == "" {
		// The allocation divides a budget between members: without a membership to
		// divide by, or a user to allocate for, there is nothing to resolve. Saying
		// so is what keeps the degradation invariant below a property of this
		// service rather than of its callers' discipline.
		return nil, errors.WithStack(ErrFairShareNotApplicable)
	}

	c := req.Constraint
	since := c.CurrentWindowStart(req.Now)

	var planTokens, planValue int64
	if req.PlanUsage != nil {
		planTokens, planValue = req.PlanUsage.Tokens, req.PlanUsage.Value
	} else {
		var err error
		planTokens, planValue, err = s.usageReader.SumPlanUsageSince(ctx, req.OrgID, req.ProviderID, since)
		if err != nil {
			return nil, errors.WithStack(err)
		}
	}

	userTokens, userValue, err := s.usageReader.SumUserPlanUsageSince(ctx, req.UserID, req.OrgID, req.ProviderID, since)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	res := &FairShareResult{
		PlanTokens: planTokens,
		PlanValue:  planValue,
		UserTokens: userTokens,
		UserValue:  userValue,
	}

	active, degraded := s.countActive(ctx, req, since)
	if degraded {
		res.CountDegraded = true
		active = int64(req.MemberCount)
	}
	if active > int64(req.MemberCount) {
		active = int64(req.MemberCount)
	}
	res.ActiveUsers = int(active)

	base := c.FairShareParamsFor(model.FairShareParams{
		TotalMembers: req.MemberCount,
		ActiveUsers:  int(active),
		Elapsed:      c.ElapsedFraction(req.Now),
	})

	if c.TokenBudget != nil {
		p := base
		p.Budget, p.UsedTotal, p.UserUsed = *c.TokenBudget, planTokens, userTokens
		alloc := model.ComputeFairShare(p)
		res.TokenAllowance = &alloc.Allowance
		res.TokenMode = alloc.Mode
	}

	if c.ValueBudget != nil {
		p := base
		p.Budget, p.UsedTotal, p.UserUsed = *c.ValueBudget, planValue, userValue
		alloc := model.ComputeFairShare(p)
		res.ValueAllowance = &alloc.Allowance
		res.ValueMode = alloc.Mode
	}

	return res, nil
}
