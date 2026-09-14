package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// planScope identifies the org+provider+user context for a subscription plan constraint.
type planScope struct {
	OrgID       model.OrgID
	ProviderID  model.ProviderID
	UserID      model.UserID // empty if no user context
	MemberCount int          // 0 disables per-user fair-share checks
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
}

func (e *rollingWindowEvaluator) Kind() model.PlanConstraintKind { return model.ConstraintRollingWindow }

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
				c.Label, formatMicrocents(providerValue, "USD"), formatDuration(dur)),
		}, nil
	}

	// Per-user fair-share check.
	if scope.UserID != "" && scope.MemberCount > 0 {
		userTokens, userProviderValue, err := e.usageStore.SumUserPlanUsageSince(ctx, scope.UserID, scope.OrgID, scope.ProviderID, since)
		if err != nil {
			return nil, nil, err
		}

		// How many members actually compete for the budget in this window. A
		// failure here degrades to "everybody is active", i.e. the static
		// budget/members share this replaces — never to a wider allowance.
		activeUsers, err := e.usageStore.CountActivePlanUsersSince(ctx, scope.OrgID, scope.ProviderID, since)
		if err != nil {
			slog.WarnContext(ctx, "rolling window: could not count active plan users, falling back to full member count",
				slog.Any("error", err), slog.String("org", string(scope.OrgID)), slog.String("provider", string(scope.ProviderID)))
			activeUsers = int64(scope.MemberCount)
		}
		// The caller has not been recorded yet when this is their first request of
		// the window; count them in so the share they get is the one they keep.
		if userTokens == 0 && userProviderValue == 0 {
			activeUsers++
		}

		base := model.FairShareParams{
			TotalMembers: scope.MemberCount,
			ActiveUsers:  int(activeUsers),
			Elapsed:      c.ElapsedFraction(now),
		}

		if c.TokenBudget != nil {
			p := c.FairShareParamsFor(base)
			p.Budget, p.UsedTotal = *c.TokenBudget, tokens
			alloc := model.ComputeFairShare(p)
			if userTokens >= alloc.Allowance {
				return nil, &planDenial{
					Message: fmt.Sprintf("fair-share quota exceeded [%s]: %d / %d tokens used in the last %s (%s allocation, %d of %d members active)",
						c.Label, userTokens, alloc.Allowance, formatDuration(dur), alloc.Mode, base.ActiveUsers, scope.MemberCount),
				}, nil
			}
		}

		if c.ValueBudget != nil {
			p := c.FairShareParamsFor(base)
			p.Budget, p.UsedTotal = *c.ValueBudget, providerValue
			alloc := model.ComputeFairShare(p)
			if userProviderValue >= alloc.Allowance {
				return nil, &planDenial{
					Message: fmt.Sprintf("fair-share quota exceeded [%s]: value budget of %s / %s reached in the last %s (%s allocation, %d of %d members active)",
						c.Label, formatMicrocents(userProviderValue, "USD"), formatMicrocents(alloc.Allowance, "USD"), formatDuration(dur), alloc.Mode, base.ActiveUsers, scope.MemberCount),
				}, nil
			}
		}
	}

	return noopReservation{}, nil, nil
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
