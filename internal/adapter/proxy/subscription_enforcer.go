package proxy

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bornholm/genai/llm"
	genaiProxy "github.com/bornholm/genai/proxy"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	"github.com/xolo-gateway/xolo/internal/metrics"
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
	upstream      *upstreamModelCache
}

func NewXoloSubscriptionEnforcer(
	providerStore port.ProviderStore,
	usageStore port.UsageStore,
	fairShare *service.FairShareService,
	state *SubscriptionStateDetailed,
	orgStore port.OrgStore,
) *XoloSubscriptionEnforcer {
	return &XoloSubscriptionEnforcer{
		providerStore: providerStore,
		orgStore:      orgStore,
		state:         state,
		evaluators: map[model.PlanConstraintKind]constraintEvaluator{
			model.ConstraintRollingWindow: newRollingWindowEvaluator(usageStore, fairShare),
			model.ConstraintConcurrency:   &concurrencyEvaluator{state: state},
		},
		upstream: newUpstreamModelCache(upstreamModelsCacheTTL),
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

	m, p := e.resolveTarget(ctx, req)
	if p == nil || p.BillingMode() != model.BillingModeSubscription {
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
		Currency:    p.Currency(),
	}
	if plan.HasPerModelConstraint() {
		scope.UpstreamModel = m.RealModel()
		scope.ModelIDs = e.upstreamModelIDs(ctx, m)
	}

	// Check cooldowns first (fast path, no DB).
	for _, c := range plan.Constraints {
		if c.PerModel() && scope.UpstreamModel == "" {
			continue
		}
		if e.state.IsExhausted(orgID, p.ID(), c.Label, cooldownModel(c, scope.UpstreamModel)) {
			return &genaiProxy.HookResult{Response: planExhaustedResponse(
				"plan quota in cooldown [" + scope.constraintName(c) + "]: upstream provider reported exhaustion",
			)}, nil
		}
	}

	// Acquire each constraint in order; release all on denial.
	reservations := make([]planReservation, 0, len(plan.Constraints))
	for _, c := range plan.Constraints {
		if c.PerModel() {
			// A model with no upstream name has nothing to meter a per-model
			// constraint against; the provider-wide ones still apply.
			if scope.UpstreamModel == "" {
				continue
			}
			c = c.ForModel(scope.UpstreamModel)
		}
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

	// A 429 the proxy answers with is either one of ours, or an upstream one a
	// relay passed through as is.
	if res != nil && res.StatusCode == http.StatusTooManyRequests && !isOwnResponse(res) {
		e.markExhausted(ctx, req, res.Headers)
	}

	return nil, nil
}

// OnError implements genaiProxy.ErrorHook.
func (e *XoloSubscriptionEnforcer) OnError(ctx context.Context, req *genaiProxy.ProxyRequest, err error) (*genaiProxy.HookResult, error) {
	e.releaseReservations(ctx, req)

	// An upstream 429 reaches the hooks as an error: mark the plan as exhausted
	// for a cooldown so the provider is not hammered before its window resets.
	var httpErr *llm.HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusTooManyRequests {
		e.markExhausted(ctx, req, httpErr.Header)
	}

	return nil, nil
}

func (e *XoloSubscriptionEnforcer) releaseReservations(ctx context.Context, req *genaiProxy.ProxyRequest) {
	reservations, _ := req.Metadata[metaPlanReservations].([]planReservation)
	releaseAll(ctx, reservations)
	delete(req.Metadata, metaPlanReservations)
}

// defaultExhaustionCooldown is how long a constraint stays exhausted after an
// upstream 429 that does not say when to come back.
const defaultExhaustionCooldown = 5 * time.Minute

// markExhausted puts the plan in cooldown after an upstream 429, for as long as
// its Retry-After asks, or defaultExhaustionCooldown.
//
// When the plan meters models separately the 429 is attributed to the
// requested model: only that model's per-model constraints go into cooldown,
// so exhausting one model does not lock the others out. The provider-wide
// constraints of such a plan stay open: a 429 there cannot be told apart from
// one model's exhaustion, and closing them would lock every model out for it.
// Otherwise every constraint of the plan does, the provider being the only
// thing the 429 can be pinned on.
func (e *XoloSubscriptionEnforcer) markExhausted(ctx context.Context, req *genaiProxy.ProxyRequest, header http.Header) {
	orgID := OrgIDFromMeta(req.Metadata)
	m, p := e.resolveTarget(ctx, req)
	if p == nil || p.BillingMode() != model.BillingModeSubscription {
		return
	}
	plan := p.SubscriptionPlan()
	if plan == nil {
		return
	}

	now := time.Now()
	cooldown, ok := retryAfter(header, now)
	if !ok {
		cooldown = defaultExhaustionCooldown
	}
	until := now.Add(cooldown)

	perModel := plan.HasPerModelConstraint()
	for _, c := range plan.Constraints {
		upstreamModel := ""
		if perModel {
			if !c.PerModel() {
				continue
			}
			upstreamModel = m.RealModel()
		}
		slog.WarnContext(ctx, "subscription enforcer: upstream 429 received, marking plan constraint as exhausted",
			slog.String("provider", string(p.ID())),
			slog.String("org", string(orgID)),
			slog.String("constraint", c.Label),
			slog.String("upstream_model", upstreamModel),
			slog.Duration("cooldown", cooldown),
		)
		e.state.MarkExhausted(orgID, p.ID(), c.Label, upstreamModel, until)
	}
}

// retryAfter reads a Retry-After header, in seconds or as an HTTP date.
func retryAfter(header http.Header, now time.Time) (time.Duration, bool) {
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		return 0, false
	}
	if secs, err := strconv.ParseInt(value, 10, 64); err == nil {
		if secs <= 0 {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return at.Sub(now), true
	}
	return 0, false
}

// cooldownModel is the upstream model a constraint's cooldown is keyed on:
// the requested one for a per-model constraint, none for a provider-wide one.
func cooldownModel(c model.PlanConstraint, upstreamModel string) string {
	if c.PerModel() {
		return upstreamModel
	}
	return ""
}

// upstreamModelIDs lists the models of the provider that target the same
// upstream model as m: the provider meters them as one, whatever proxy name
// they are published under. The grouping is cached for upstreamModelsCacheTTL.
// On a store error it falls back to m alone, uncached, which undercounts only
// when the model has aliases; PlanModelGroupingFailures counts those requests.
func (e *XoloSubscriptionEnforcer) upstreamModelIDs(ctx context.Context, m model.LLMModel) []model.LLMModelID {
	now := time.Now()
	if ids, ok := e.upstream.get(m.ID(), now); ok {
		return ids
	}
	models, err := e.providerStore.ListLLMModels(ctx, m.OrgID())
	if err != nil {
		metrics.PlanModelGroupingFailures.With(prometheus.Labels{metrics.LabelOrg: string(m.OrgID())}).Inc()
		slog.WarnContext(ctx, "subscription enforcer: could not list models, metering the requested model alone",
			slog.Any("error", err), slog.String("model", string(m.ID())))
		return []model.LLMModelID{m.ID()}
	}
	ids := model.UpstreamModelIDs(models, m)
	e.upstream.put(m.ID(), ids, now)
	return ids
}

// resolveTarget returns the model a request targets and its provider, or nils
// when either cannot be resolved.
func (e *XoloSubscriptionEnforcer) resolveTarget(ctx context.Context, req *genaiProxy.ProxyRequest) (model.LLMModel, model.Provider) {
	var (
		m   model.LLMModel
		err error
	)
	if modelID := ModelIDFromMeta(req.Metadata); modelID != "" {
		m, err = e.providerStore.GetLLMModelByID(ctx, modelID)
	} else {
		orgID := OrgIDFromMeta(req.Metadata)
		if orgID == "" {
			return nil, nil
		}
		_, proxyName, parseErr := parseQualifiedModelName(req.Model)
		if parseErr != nil {
			return nil, nil
		}
		m, err = e.providerStore.GetLLMModelByProxyName(ctx, orgID, proxyName)
	}
	if err != nil || m == nil {
		return nil, nil
	}
	p, err := e.providerStore.GetProviderByID(ctx, m.ProviderID())
	if err != nil {
		return nil, nil
	}
	return m, p
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
