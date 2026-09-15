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
	// CountDegraded reports that the active-user count could not be read and
	// that the allocation split the commons across the whole membership. That is
	// the static budget/members share at best: the availability cap and pacing
	// still apply on top and can narrow it further, and the happy hour cannot
	// widen it, since (B − othersUsed)/N never exceeds B/N. Callers must say so
	// rather than present ActiveUsers as a measurement.
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

	// Bounded like the count it complements, but on the request's own context:
	// its answer is not shared, so a client that hangs up may take it along.
	probeCtx, cancel := context.WithTimeout(ctx, s.probeTimeout())
	defer cancel()
	present, err := s.usageReader.HasPlanUsageSince(probeCtx, req.UserID, req.OrgID, req.ProviderID, since)
	if err != nil || !present {
		return false
	}

	s.presence.remember(key, req.Now)

	return true
}

// countAll returns the plan-wide active-user count, from the cache when a recent
// read is available. A failure is cached like a value, so a database that cannot
// answer the count is spared the query on every request rather than asked again
// and again while it is struggling; concurrent reloads of one key are folded
// into a single query.
func (s *FairShareService) countAll(ctx context.Context, req FairShareRequest, since time.Time) (count int64, degraded bool) {
	key := activeUserKeyFor(req.OrgID, req.ProviderID, since, s.activeUsers.ttl)

	entry, fresh, ok := s.activeUsers.lookup(key, req.Now)
	if ok && fresh {
		return entry.count, entry.degraded
	}

	reload := func() (any, error) {
		// Another caller may have filled the entry while this one waited.
		if entry, ok := s.activeUsers.get(key, req.Now); ok {
			return entry, nil
		}
		// Detached from the request: the result lands in an entry shared by
		// everyone on the plan, and a client hanging up mid-query is not a
		// reason to mark the plan degraded for a whole TTL. A failure here is
		// then the database's own, and worth caching.
		countCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.countTimeout)
		defer cancel()
		n, err := s.usageReader.CountActivePlanUsersSince(countCtx, req.OrgID, req.ProviderID, since, "")
		entry := activeUserEntry{count: n, degraded: err != nil}
		s.activeUsers.put(key, entry, req.Now)
		return entry, nil
	}

	if ok {
		// Stale but present: serve it and refresh behind the request. The
		// denominator moves slowly, and holding a proxy request behind a COUNT
		// over a week of usage buys nothing a one-TTL-old value does not give.
		// DoChan folds concurrent refreshes into one; nobody waits on it.
		s.inflight.DoChan(key.flightKey(), reload)
		return entry.count, entry.degraded
	}

	// Nothing to serve yet: this is the first read of the window. Wait for it,
	// but not indefinitely — a proxy request is not the place to sit behind a
	// COUNT over a week of usage while the indexes are still being built. Past
	// the bound the share is computed on the whole membership, reported as
	// degraded, and the count carries on behind the request for the next one.
	// Nothing is cached on that path: the reload writes the entry itself.
	ch := s.inflight.DoChan(key.flightKey(), reload)
	select {
	case res := <-ch:
		entry = res.Val.(activeUserEntry)
		return entry.count, entry.degraded
	case <-time.After(s.firstReadWait):
		return 0, true
	}
}

// probeTimeout bounds one presence probe: a fraction of the count's own bound,
// since a point lookup that takes that long is a database in trouble.
func (s *FairShareService) probeTimeout() time.Duration {
	return max(s.countTimeout/4, 100*time.Millisecond)
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
	usageReader   PlanUsageReader
	activeUsers   *activeUserCache
	presence      *presenceCache
	countTimeout  time.Duration
	firstReadWait time.Duration
	// inflight serialises concurrent reloads of one count: when an entry
	// expires, every request on the plan would otherwise fire the same
	// COUNT(DISTINCT) at once.
	inflight singleflight.Group
}

// FairShareOptions tunes the service's reads of the usage store.
type FairShareOptions struct {
	// CacheTTL is how long an active-user count is reused. Non-positive reads
	// the count on every call.
	CacheTTL time.Duration
	// CountTimeout bounds one plan-wide count. The count runs detached from the
	// request, so this is what stops a stuck query from holding the reload.
	// Non-positive means DefaultActiveUserCountTimeout.
	CountTimeout time.Duration
	// FirstReadWait bounds how long a request waits for the first count of a
	// window, when there is no entry to serve stale. Non-positive means
	// DefaultFirstReadWait.
	FirstReadWait time.Duration
}

// DefaultFirstReadWait is how long the first request of a window waits for its
// count before falling back to the whole membership and letting the count
// finish behind it. Later requests are served from the entry, stale or fresh.
const DefaultFirstReadWait = time.Second

// DefaultActiveUserCountTimeout is the default bound on one plan-wide count.
// It assumes idx_usage_org_prov_plan is in place and the count is served from
// the index; a deployment where it is not, or whose usage table is very large,
// raises it through XOLO_PROXY_ACTIVE_USER_COUNT_TIMEOUT rather than living
// with a count that always times out and a share that always degrades.
const DefaultActiveUserCountTimeout = 10 * time.Second

func NewFairShareService(usageReader PlanUsageReader) *FairShareService {
	return NewFairShareServiceWithOptions(usageReader, FairShareOptions{CacheTTL: DefaultActiveUserCacheTTL})
}

// NewFairShareServiceWithCacheTTL builds a service whose active-user counts are
// reused for ttl. A non-positive ttl reads the count on every call.
func NewFairShareServiceWithCacheTTL(usageReader PlanUsageReader, ttl time.Duration) *FairShareService {
	return NewFairShareServiceWithOptions(usageReader, FairShareOptions{CacheTTL: ttl})
}

func NewFairShareServiceWithOptions(usageReader PlanUsageReader, opts FairShareOptions) *FairShareService {
	timeout := opts.CountTimeout
	if timeout <= 0 {
		timeout = DefaultActiveUserCountTimeout
	}
	firstRead := opts.FirstReadWait
	if firstRead <= 0 {
		firstRead = DefaultFirstReadWait
	}
	return &FairShareService{
		usageReader:   usageReader,
		activeUsers:   newActiveUserCache(opts.CacheTTL),
		presence:      newPresenceCache(opts.CacheTTL),
		countTimeout:  timeout,
		firstReadWait: firstRead,
	}
}

// Resolve computes the allocation for one user on one rolling-window constraint.
//
// Every degraded path narrows the allowance rather than widening it: losing the
// active-user count splits the commons across the whole membership, which is the
// static budget/members share at best and narrower under the availability cap or
// pacing, because an allocation handed out on missing data must not exceed what
// the plan can honour.
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
