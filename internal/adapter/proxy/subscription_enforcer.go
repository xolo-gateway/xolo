package proxy

import (
	"context"
	"log/slog"
	"strings"
	"time"

	genaiProxy "github.com/bornholm/genai/proxy"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/pkg/errors"
)

const metaPlanReservations = "xolo.plan.reservations"

// XoloSubscriptionEnforcer is a Pre/Post/Error hook that enforces subscription plan constraints.
// For providers with BillingMode=subscription it checks rolling-window budgets and concurrency
// limits; PAYG providers are skipped entirely.
//
// State is in-memory (not shared across replicas): the upstream 429 resync acts as the safety net.
type XoloSubscriptionEnforcer struct {
	providerStore port.ProviderStore
	orgStore      port.OrgStore
	state         *SubscriptionStateDetailed
	evaluators    map[model.PlanConstraintKind]constraintEvaluator
}

func NewXoloSubscriptionEnforcer(
	providerStore port.ProviderStore,
	usageStore port.UsageStore,
	state *SubscriptionStateDetailed,
	orgStore port.OrgStore,
) *XoloSubscriptionEnforcer {
	return &XoloSubscriptionEnforcer{
		providerStore: providerStore,
		orgStore:      orgStore,
		state:         state,
		evaluators: map[model.PlanConstraintKind]constraintEvaluator{
			model.ConstraintRollingWindow: newRollingWindowEvaluator(usageStore),
			model.ConstraintConcurrency:   &concurrencyEvaluator{state: state},
		},
	}
}

func (e *XoloSubscriptionEnforcer) Name() string  { return "xolo.subscription-enforcer" }
func (e *XoloSubscriptionEnforcer) Priority() int { return 6 } // just after quota-enforcer (5)

// PreRequest implements genaiProxy.PreRequestHook.
func (e *XoloSubscriptionEnforcer) PreRequest(ctx context.Context, req *genaiProxy.ProxyRequest) (*genaiProxy.HookResult, error) {
	populateMetaFromContext(ctx, req)

	orgID := OrgIDFromMeta(req.Metadata)
	if orgID == "" {
		return nil, nil
	}

	p, err := e.resolveProvider(ctx, req)
	if err != nil || p == nil {
		return nil, nil
	}
	if p.BillingMode() != model.BillingModeSubscription {
		return nil, nil
	}

	plan := p.SubscriptionPlan()
	if plan == nil || len(plan.Constraints) == 0 {
		return nil, nil
	}

	userID := model.UserID(req.UserID)

	// Resolve org member count for per-user fair-share enforcement.
	memberCount := 0
	if userID != "" {
		_, count, err := e.orgStore.ListOrgMembers(ctx, orgID, port.ListOrgMembersOptions{})
		if err != nil {
			slog.WarnContext(ctx, "subscription enforcer: could not list org members for fair-share, skipping user fair-share check",
				slog.Any("error", err), slog.String("org", string(orgID)))
		} else {
			memberCount = int(count)
		}
	}

	scope := planScope{
		OrgID:       orgID,
		ProviderID:  p.ID(),
		UserID:      userID,
		MemberCount: memberCount,
	}

	// Check cooldowns first (fast path, no DB).
	for _, c := range plan.Constraints {
		if e.state.IsExhausted(orgID, p.ID(), c.Label) {
			return &genaiProxy.HookResult{Response: planExhaustedResponse(
				"plan quota in cooldown [" + c.Label + "]: upstream provider reported exhaustion",
			)}, nil
		}
	}

	// Acquire each constraint in order; release all on denial.
	reservations := make([]planReservation, 0, len(plan.Constraints))
	for _, c := range plan.Constraints {
		ev, ok := e.evaluators[c.Kind]
		if !ok {
			continue
		}
		res, denial, err := ev.Acquire(ctx, scope, c)
		if err != nil {
			releaseAll(ctx, reservations)
			return nil, errors.WithStack(err)
		}
		if denial != nil {
			releaseAll(ctx, reservations)
			return &genaiProxy.HookResult{Response: planExhaustedResponse(denial.Message)}, nil
		}
		reservations = append(reservations, res)
	}

	// Stash reservations for release in PostResponse/OnError.
	req.Metadata[metaPlanReservations] = reservations
	return nil, nil
}

// PostResponse implements genaiProxy.PostResponseHook.
func (e *XoloSubscriptionEnforcer) PostResponse(ctx context.Context, req *genaiProxy.ProxyRequest, res *genaiProxy.ProxyResponse) (*genaiProxy.HookResult, error) {
	e.releaseReservations(ctx, req)

	// If upstream returned 429/throttle, mark the plan as exhausted for a short cooldown
	// so we don't hammer the provider before the window resets.
	if res != nil && res.StatusCode == 429 {
		e.markExhaustedFromResponse(ctx, req, res)
	}

	return nil, nil
}

// OnError implements genaiProxy.ErrorHook.
func (e *XoloSubscriptionEnforcer) OnError(ctx context.Context, req *genaiProxy.ProxyRequest, err error) (*genaiProxy.HookResult, error) {
	e.releaseReservations(ctx, req)
	return nil, nil
}

func (e *XoloSubscriptionEnforcer) releaseReservations(ctx context.Context, req *genaiProxy.ProxyRequest) {
	reservations, _ := req.Metadata[metaPlanReservations].([]planReservation)
	releaseAll(ctx, reservations)
	delete(req.Metadata, metaPlanReservations)
}

func (e *XoloSubscriptionEnforcer) markExhaustedFromResponse(ctx context.Context, req *genaiProxy.ProxyRequest, res *genaiProxy.ProxyResponse) {
	orgID := OrgIDFromMeta(req.Metadata)
	p, err := e.resolveProvider(ctx, req)
	if err != nil || p == nil || p.BillingMode() != model.BillingModeSubscription {
		return
	}
	plan := p.SubscriptionPlan()
	if plan == nil {
		return
	}
	// Detect whether the 429 looks like an upstream plan exhaustion
	// (not our own plan_exhausted code, which we generate ourselves).
	if isOwnResponse(res) {
		return
	}
	// Default cooldown: 5 minutes. Could be refined from Retry-After header in the future.
	cooldown := 5 * time.Minute
	for _, c := range plan.Constraints {
		slog.WarnContext(ctx, "subscription enforcer: upstream 429 received, marking plan constraint as exhausted",
			slog.String("provider", string(p.ID())),
			slog.String("org", string(orgID)),
			slog.String("constraint", c.Label),
			slog.Duration("cooldown", cooldown),
		)
		e.state.MarkExhausted(orgID, p.ID(), c.Label, time.Now().Add(cooldown))
	}
}

func (e *XoloSubscriptionEnforcer) resolveProvider(ctx context.Context, req *genaiProxy.ProxyRequest) (model.Provider, error) {
	modelID := ModelIDFromMeta(req.Metadata)
	if modelID != "" {
		m, err := e.providerStore.GetLLMModelByID(ctx, modelID)
		if err != nil {
			return nil, nil
		}
		return e.providerStore.GetProviderByID(ctx, m.ProviderID())
	}

	orgID := OrgIDFromMeta(req.Metadata)
	if orgID == "" {
		return nil, nil
	}
	_, proxyName, err := parseQualifiedModelName(req.Model)
	if err != nil {
		return nil, nil
	}
	m, err := e.providerStore.GetLLMModelByProxyName(ctx, orgID, proxyName)
	if err != nil {
		return nil, nil
	}
	return e.providerStore.GetProviderByID(ctx, m.ProviderID())
}

func releaseAll(ctx context.Context, reservations []planReservation) {
	for _, r := range reservations {
		r.Release(ctx)
	}
}

func isOwnResponse(res *genaiProxy.ProxyResponse) bool {
	if res == nil || res.Body == nil {
		return false
	}
	body, ok := res.Body.(map[string]any)
	if !ok {
		return false
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		return false
	}
	code, _ := errObj["code"].(string)
	return strings.HasPrefix(code, "plan_") || code == "quota_exceeded"
}

func planExhaustedResponse(message string) *genaiProxy.ProxyResponse {
	return &genaiProxy.ProxyResponse{
		StatusCode: 429,
		Body: map[string]any{
			"error": map[string]any{
				"message": message,
				"type":    "rate_limit_error",
				"code":    "plan_exhausted",
			},
		},
	}
}

var _ genaiProxy.PreRequestHook = &XoloSubscriptionEnforcer{}
var _ genaiProxy.PostResponseHook = &XoloSubscriptionEnforcer{}
var _ genaiProxy.ErrorHook = &XoloSubscriptionEnforcer{}
