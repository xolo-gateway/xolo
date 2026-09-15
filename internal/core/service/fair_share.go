package service

import (
	"context"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/pkg/errors"
)

// PlanUsageReader is the narrow slice of port.UsageStore the fair-share
// allocation reads. It is satisfied by port.UsageStore and by any fake.
type PlanUsageReader interface {
	SumPlanUsageSince(ctx context.Context, orgID model.OrgID, providerID model.ProviderID, since time.Time) (tokens int64, providerValue int64, err error)
	SumUserPlanUsageSince(ctx context.Context, userID model.UserID, orgID model.OrgID, providerID model.ProviderID, since time.Time) (tokens int64, providerValue int64, err error)
	CountActivePlanUsersSince(ctx context.Context, orgID model.OrgID, providerID model.ProviderID, since time.Time, excludeUserID model.UserID) (int64, error)
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

// countOthers returns how many users other than the caller consumed in the
// window, from the cache when a recent count is available.
func (s *FairShareService) countOthers(ctx context.Context, req FairShareRequest, since time.Time) (int64, error) {
	key := activeUserKey{
		orgID:      req.OrgID,
		providerID: req.ProviderID,
		since:      since,
		excluded:   req.UserID,
	}

	if count, ok := s.activeUsers.get(key, req.Now); ok {
		return count, nil
	}

	count, err := s.usageReader.CountActivePlanUsersSince(ctx, req.OrgID, req.ProviderID, since, req.UserID)
	if err != nil {
		return 0, errors.WithStack(err)
	}

	s.activeUsers.put(key, count, req.Now)

	return count, nil
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

	// The count excludes the caller, who is then added back: they are competing
	// for the budget whether or not their first request has been recorded yet.
	others, err := s.countOthers(ctx, req, since)
	if err != nil {
		res.CountDegraded = true
		others = int64(req.MemberCount)
	}
	active := int(others) + 1
	if active > req.MemberCount {
		active = req.MemberCount
	}
	res.ActiveUsers = active

	base := c.FairShareParamsFor(model.FairShareParams{
		TotalMembers: req.MemberCount,
		ActiveUsers:  active,
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
