package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	"github.com/xolo-gateway/xolo/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

// planScope identifies the org+provider+user context for a subscription plan constraint.
type planScope struct {
	OrgID       model.OrgID
	ProviderID  model.ProviderID
	UserID      model.UserID // empty if no user context
	MemberCount int          // 0 disables per-user fair-share checks
	// Currency is the provider's, in which value budgets and provider costs are
	// expressed. Denial messages format amounts with it.
	Currency string
}

// planDenial describes why a constraint blocked a request.
type planDenial struct {
	Message    string
	RetryAfter time.Duration
}

// planReservation is returned by a successful Acquire; must be Released after the request.
type planReservation interface {
	Release(ctx context.Context)
}

type noopReservation struct{}

func (noopReservation) Release(_ context.Context) {}

// concurrencyReservation decrements the org-level in-flight counter on release.
type concurrencyReservation struct {
	state      *SubscriptionStateDetailed
	orgID      model.OrgID
	providerID model.ProviderID
}

func (r *concurrencyReservation) Release(_ context.Context) {
	r.state.DecrementInFlight(r.orgID, r.providerID)
}

// userConcurrencyReservation decrements the per-user in-flight counter on release.
type userConcurrencyReservation struct {
	state      *SubscriptionStateDetailed
	orgID      model.OrgID
	providerID model.ProviderID
	userID     model.UserID
}

func (r *userConcurrencyReservation) Release(_ context.Context) {
	r.state.DecrementUserInFlight(r.orgID, r.providerID, r.userID)
}

// compositeReservation releases multiple reservations in order.
type compositeReservation []planReservation

func (c compositeReservation) Release(ctx context.Context) {
	for _, r := range c {
		r.Release(ctx)
	}
}

// constraintEvaluator evaluates a single plan constraint and either grants or denies access.
type constraintEvaluator interface {
	Kind() model.PlanConstraintKind
	// Acquire checks the constraint; on success it returns a reservation (may be no-op).
	// On failure it returns a denial with the reason.
	Acquire(ctx context.Context, scope planScope, c model.PlanConstraint) (planReservation, *planDenial, error)
}

// rollingWindowEvaluator enforces time-based rolling budgets (token count and/or value).
type rollingWindowEvaluator struct {
	usageStore port.UsageStore
	fairShare  *service.FairShareService
}

// fairShare is the process-wide allocator, shared with the dashboard so both
// decide and display from the same active-user cache.
func newRollingWindowEvaluator(usageStore port.UsageStore, fairShare *service.FairShareService) *rollingWindowEvaluator {
	if fairShare == nil {
		// A nil allocator would only be noticed on the first subscription
		// request in production, as a panic on the hot path. Build a private
		// one instead: same formula, its own cache, and a share the dashboard
		// may disagree with for one TTL — a degradation, not an outage.
		fairShare = service.NewFairShareService(usageStore)
	}
	return &rollingWindowEvaluator{
		usageStore: usageStore,
		fairShare:  fairShare,
	}
}

func (e *rollingWindowEvaluator) Kind() model.PlanConstraintKind {
	return model.ConstraintRollingWindow
}

func (e *rollingWindowEvaluator) Acquire(ctx context.Context, scope planScope, c model.PlanConstraint) (planReservation, *planDenial, error) {
	dur := c.Duration.Duration()
	if dur <= 0 || (c.TokenBudget == nil && c.ValueBudget == nil) {
		return noopReservation{}, nil, nil
	}

	// Window start is aligned on the constraint's anchor (fixed/tumbling window matching the
	// upstream provider's reset schedule) or falls back to a sliding window when unset.
	now := time.Now()
	since := c.CurrentWindowStart(now)
	tokens, providerValue, err := e.usageStore.SumPlanUsageSince(ctx, scope.OrgID, scope.ProviderID, since)
	if err != nil {
		return nil, nil, err
	}

	if c.TokenBudget != nil && tokens >= *c.TokenBudget {
		return nil, &planDenial{
			Message: fmt.Sprintf("plan quota exceeded [%s]: %d / %d tokens used in the last %s",
				c.Label, tokens, *c.TokenBudget, formatDuration(dur)),
		}, nil
	}

	if c.ValueBudget != nil && providerValue >= *c.ValueBudget {
		return nil, &planDenial{
			Message: fmt.Sprintf("plan quota exceeded [%s]: value budget of %s reached in the last %s",
				c.Label, formatMicrocents(providerValue, scope.Currency), formatDuration(dur)),
		}, nil
	}

	// Per-user fair-share check.
	if scope.UserID != "" && scope.MemberCount > 0 {
		// The plan-wide totals were just read for the check above; handing them to
		// the allocator keeps this to one aggregation of the window per request.
		share, err := e.fairShare.Resolve(ctx, service.FairShareRequest{
			OrgID:       scope.OrgID,
			ProviderID:  scope.ProviderID,
			UserID:      scope.UserID,
			MemberCount: scope.MemberCount,
			Constraint:  c,
			Now:         now,
			PlanUsage:   &service.PlanUsage{Tokens: tokens, Value: providerValue},
		})
		if err != nil {
			return nil, nil, err
		}
		if share.CountDegraded {
			// Not a Warn: the failure is cached for a TTL and every request in
			// that TTL lands here, so a struggling database would flood the log.
			// The counter is what to alert on; the line is for a debugger.
			metrics.FairShareDegradedShares.With(prometheus.Labels{metrics.LabelOrg: string(scope.OrgID)}).Inc()
			slog.DebugContext(ctx, "rolling window: active-user count unavailable, share computed on the whole membership",
				slog.String("org", string(scope.OrgID)), slog.String("provider", string(scope.ProviderID)))
		}

		if share.TokenAllowance != nil && share.UserTokens >= *share.TokenAllowance {
			return nil, &planDenial{
				Message: fmt.Sprintf("fair-share quota exceeded [%s]: %d / %d tokens used in the last %s (%s)",
					c.Label, share.UserTokens, *share.TokenAllowance, formatDuration(dur),
					shareBasis(share, share.TokenMode, scope.MemberCount)),
			}, nil
		}

		if share.ValueAllowance != nil && share.UserValue >= *share.ValueAllowance {
			return nil, &planDenial{
				Message: fmt.Sprintf("fair-share quota exceeded [%s]: value budget of %s / %s reached in the last %s (%s)",
					c.Label, formatMicrocents(share.UserValue, scope.Currency), formatMicrocents(*share.ValueAllowance, scope.Currency),
					formatDuration(dur), shareBasis(share, share.ValueMode, scope.MemberCount)),
			}, nil
		}
	}

	return noopReservation{}, nil, nil
}

// shareBasis spells out what the allowance was computed from, so a share that
// moves between two requests can be accounted for rather than guessed at. A
// degraded count is named as such: reporting every member as active would read
// as a measurement instead of a fallback.
func shareBasis(share *service.FairShareResult, mode model.FairShareMode, memberCount int) string {
	if share.CountDegraded {
		// The count is what the share is split by; every other rule still applies
		// on top of it, so the allocation is the whole-membership share at best,
		// narrower under pacing and wider during the happy hour. Naming the mode
		// keeps the message from contradicting the allocation it explains.
		return fmt.Sprintf("active-user count unavailable, %s allocation split across every member", mode)
	}
	return fmt.Sprintf("%s allocation, %d of %d members active", mode, share.ActiveUsers, memberCount)
}

// concurrencyEvaluator enforces a maximum number of simultaneous in-flight requests.
type concurrencyEvaluator struct {
	state *SubscriptionStateDetailed
}

func (e *concurrencyEvaluator) Kind() model.PlanConstraintKind { return model.ConstraintConcurrency }

func (e *concurrencyEvaluator) Acquire(_ context.Context, scope planScope, c model.PlanConstraint) (planReservation, *planDenial, error) {
	if c.MaxConcurrent == nil || *c.MaxConcurrent <= 0 {
		return noopReservation{}, nil, nil
	}

	// Increment first, then check — this prevents thundering herd but may momentarily
	// exceed the limit by 1. We decrement immediately if the limit is breached.
	current := e.state.IncrementInFlight(scope.OrgID, scope.ProviderID)
	if current > *c.MaxConcurrent {
		e.state.DecrementInFlight(scope.OrgID, scope.ProviderID)
		return nil, &planDenial{
			Message: fmt.Sprintf("concurrency limit reached [%s]: %d / %d concurrent requests",
				c.Label, current-1, *c.MaxConcurrent),
		}, nil
	}
	orgRes := &concurrencyReservation{
		state:      e.state,
		orgID:      scope.OrgID,
		providerID: scope.ProviderID,
	}

	// Per-user fair-share concurrency check.
	if scope.UserID != "" && scope.MemberCount > 0 {
		n := scope.MemberCount
		fairShare := max(*c.MaxConcurrent/n, 1)
		userCurrent := e.state.IncrementUserInFlight(scope.OrgID, scope.ProviderID, scope.UserID)
		if userCurrent > fairShare {
			e.state.DecrementUserInFlight(scope.OrgID, scope.ProviderID, scope.UserID)
			orgRes.Release(context.Background())
			return nil, &planDenial{
				Message: fmt.Sprintf("fair-share concurrency limit reached [%s]: %d / %d concurrent requests for this user (1/%d of plan)",
					c.Label, userCurrent-1, fairShare, n),
			}, nil
		}
		return compositeReservation{orgRes, &userConcurrencyReservation{
			state:      e.state,
			orgID:      scope.OrgID,
			providerID: scope.ProviderID,
			userID:     scope.UserID,
		}}, nil, nil
	}

	return orgRes, nil, nil
}

func formatDuration(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d%(time.Minute) == 0:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return d.String()
	}
}
