package proxy

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"slices"
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
	usageStore    port.UsageStore
	orgStore      port.OrgStore
	state         *SubscriptionStateDetailed
	evaluators    map[model.PlanConstraintKind]constraintEvaluator
	rolling       *rollingWindowEvaluator
	models        *orgModelCache
}

func NewXoloSubscriptionEnforcer(
	providerStore port.ProviderStore,
	usageStore port.UsageStore,
	fairShare *service.FairShareService,
	state *SubscriptionStateDetailed,
	orgStore port.OrgStore,
) *XoloSubscriptionEnforcer {
	rolling := newRollingWindowEvaluator(usageStore, fairShare)
	return &XoloSubscriptionEnforcer{
		providerStore: providerStore,
		usageStore:    usageStore,
		orgStore:      orgStore,
		state:         state,
		evaluators: map[model.PlanConstraintKind]constraintEvaluator{
			model.ConstraintRollingWindow: rolling,
			model.ConstraintConcurrency:   &concurrencyEvaluator{state: state},
		},
		rolling: rolling,
		models:  newOrgModelCache(upstreamModelsCacheTTL),
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
	if plan.HasPerModelConstraint() || plan.HasSharedConstraint() {
		// Read once for both: a failure is not cached, and reading it twice
		// would double both the load on a struggling store and the metric.
		models, ok := e.orgModels(ctx, orgID)
		if plan.HasPerModelConstraint() {
			scope.UpstreamModel = m.RealModel()
			scope.ModelIDs = upstreamModelIDs(models, ok, m)
		}
		// On a failure the usage is weighed without the models, each at the
		// constraint's highest rate: the plan closes early rather than late.
		scope.Models = models
	}

	// A provider holding several keys holds a whole plan on each: the users
	// share the pool, and each request then goes through one key with room
	// left (see pickKey).
	keys := model.ProviderKeyIDs(p)
	pooled := len(keys) > 1
	shared := model.PooledPlan(p)
	if pooled {
		now := time.Now()
		for _, c := range plan.Constraints {
			if windows := model.KeyWindows(p, c.Label, now); windows != nil {
				if scope.KeyWindows == nil {
					scope.KeyWindows = map[string]map[model.ProviderKeyID]time.Time{}
				}
				scope.KeyWindows[c.Label] = windows
			}
		}
	}

	// Check cooldowns first (fast path, no DB). The keys of a pool are checked
	// one by one when one is picked.
	if !pooled {
		for _, c := range plan.Constraints {
			if c.PerModel() && scope.UpstreamModel == "" {
				continue
			}
			if e.state.IsKeyExhausted(orgID, p.ID(), model.PrimaryProviderKey, c.Label, cooldownModel(c, scope.UpstreamModel)) {
				return &genaiProxy.HookResult{Response: planExhaustedResponse(
					"plan quota in cooldown [" + scope.constraintName(c) + "]: upstream provider reported exhaustion",
				)}, nil
			}
		}
	}

	// Acquire each constraint in order; release all on denial.
	reservations := make([]planReservation, 0, len(shared.Constraints))
	for _, c := range shared.Constraints {
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

	if pooled {
		chosen, fallbacks, denial, err := e.pickKey(ctx, scope, p, keys)
		if err != nil {
			releaseAll(ctx, reservations)
			return nil, errors.WithStack(err)
		}
		if denial != nil {
			releaseAll(ctx, reservations)
			return &genaiProxy.HookResult{Response: planExhaustedResponse(denial.Message)}, nil
		}
		req.Metadata[MetaProviderKey] = chosen
		req.Metadata[metaProviderKeyFallbacks] = fallbacks
	}

	// Stash reservations for release in PostResponse/OnError.
	req.Metadata[metaPlanReservations] = reservations
	return nil, nil
}

// pickKey chooses the API key a request of a pooled provider goes through,
// and the keys it may move to when that one turns out exhausted upstream.
//
// A user stays on the key their last request went through, where the upstream
// prompt cache of their conversations lives, as long as it has room left and
// is not much fuller than the emptiest key (see rebalanceMargin). A user
// without one, whose key ran out or drifted too far ahead, is moved to the key
// with the most room left; keys with as much room are ranked per user
// (model.KeyAffinity), so the users arriving on an idle plan spread over the
// keys. The fallbacks are the other keys with room left, in the same order.
//
// Each key is held to its own plan (model.KeyPlan); the users' shares were
// already decided on the pool.
func (e *XoloSubscriptionEnforcer) pickKey(ctx context.Context, scope planScope, p model.Provider, keys []model.ProviderKeyID) (model.ProviderKeyID, []model.ProviderKeyID, *planDenial, error) {
	sticky, hasSticky := model.ProviderKeyID(""), false
	if scope.UserID != "" {
		last, ok, err := e.usageStore.LastPlanUsageKey(ctx, model.PlanUsageScope{OrgID: scope.OrgID, ProviderID: scope.ProviderID}, scope.UserID)
		if err != nil {
			// Not fatal: the user is placed as a newcomer, at worst on a key
			// whose cache holds none of their conversations.
			slog.WarnContext(ctx, "subscription enforcer: could not read the user's last provider key",
				slog.Any("error", err), slog.String("provider", string(scope.ProviderID)))
		}
		hasSticky = ok && err == nil && slices.Contains(keys, last)
		sticky = last
	}

	states, err := e.keyStates(ctx, scope, p, keys)
	if err != nil {
		return "", nil, nil, err
	}

	open := make([]model.ProviderKeyID, 0, len(keys))
	var lastDenial *planDenial
	for _, key := range keys {
		if states[key].denial != nil {
			lastDenial = states[key].denial
			continue
		}
		open = append(open, key)
	}
	if len(open) == 0 {
		if lastDenial == nil {
			lastDenial = &planDenial{Message: "plan quota exceeded: the plan's budget is used up"}
		}
		return "", nil, lastDenial, nil
	}

	slices.SortStableFunc(open, func(a, b model.ProviderKeyID) int {
		if c := cmp.Compare(states[a].fill, states[b].fill); c != 0 {
			return c
		}
		return cmp.Compare(model.KeyAffinity(scope.UserID, a), model.KeyAffinity(scope.UserID, b))
	})
	if hasSticky {
		i := slices.Index(open, sticky)
		if i > 0 && states[sticky].fill-states[open[0]].fill <= rebalanceMargin(scope.UserID, sticky) {
			open = slices.Insert(slices.Delete(open, i, i+1), 0, sticky)
		}
	}
	return open[0], open[1:], nil, nil
}

// The bounds of rebalanceMargin, as a share of a key's budget.
const (
	minRebalanceMargin = 0.10
	maxRebalanceMargin = 0.30
)

// rebalanceMargin is how much fuller than the emptiest key a user's key may
// get before the user leaves it, giving up the prompt cache it holds for
// them. Each user has their own margin, drawn between minRebalanceMargin and
// maxRebalanceMargin, so that as the gap between keys widens the users leave
// a few at a time instead of all at once, which would only move the
// imbalance to the other key. Once moved, a user stays on their new key until
// it gets as far ahead in turn.
//
// The margin is drawn apart from model.KeyAffinity: the users that ranking
// placed on a key are those it ranks first, and their margins would otherwise
// all sit at the low end.
func rebalanceMargin(userID model.UserID, key model.ProviderKeyID) float64 {
	rank := float64(model.KeyAffinity(userID+"\x00margin", key)) / float64(math.MaxUint64)
	return minRebalanceMargin + rank*(maxRebalanceMargin-minRebalanceMargin)
}

// keyState is how much of the plan one key of a pool consumed: the fullest of
// its rolling windows, and the denial when one of them is used up or in
// cooldown.
type keyState struct {
	fill   float64
	denial *planDenial
}

// keyStates measures every key of a pool against its own plan, with one read
// per rolling window for all the keys together, each key in its own window.
// The concurrency constraints are held on the pool only.
func (e *XoloSubscriptionEnforcer) keyStates(ctx context.Context, scope planScope, p model.Provider, keys []model.ProviderKeyID) (map[model.ProviderKeyID]keyState, error) {
	plans := make(map[model.ProviderKeyID]*model.SubscriptionPlan, len(keys))
	for _, key := range keys {
		plans[key] = model.KeyPlan(p, key)
	}
	states := make(map[model.ProviderKeyID]keyState, len(keys))
	for i, c := range p.SubscriptionPlan().Constraints {
		if c.PerModel() && scope.UpstreamModel == "" {
			continue
		}
		var usage map[model.ProviderKeyID]map[model.LLMModelID]port.PlanUsageTotals
		if c.Kind == model.ConstraintRollingWindow && c.Duration.Duration() > 0 && anyBudget(plans, i, scope.UpstreamModel) {
			var err error
			if usage, err = e.rolling.keysUsage(ctx, scope, c.ForModel(scope.UpstreamModel)); err != nil {
				return nil, err
			}
		}
		for _, key := range keys {
			state := states[key]
			if state.denial != nil {
				continue
			}
			kc := plans[key].Constraints[i]
			if kc.PerModel() {
				kc = kc.ForModel(scope.UpstreamModel)
			}
			keyScope := scope.forKey(key)
			if e.state.IsKeyExhausted(scope.OrgID, scope.ProviderID, key, c.Label, cooldownModel(c, scope.UpstreamModel)) {
				state.denial = &planDenial{Message: "plan quota in cooldown [" + keyScope.constraintName(kc) + "]: upstream provider reported exhaustion"}
			} else if usage != nil && (kc.TokenBudget != nil || kc.ValueBudget != nil) {
				u := keyUsage(kc, scope.Models, usage[key])
				state.denial = budgetDenial(keyScope, kc, keyScope.constraintName(kc), u.Tokens, u.Value)
				state.fill = max(state.fill, fillRatio(kc, u))
			}
			states[key] = state
		}
	}
	return states, nil
}

// anyBudget reports whether one of the keys' plans holds a budget on its i-th
// constraint, as it applies to the requested upstream model, without which
// its usage is not worth reading. A per-model window may hold its budgets in
// its model budgets alone.
func anyBudget(plans map[model.ProviderKeyID]*model.SubscriptionPlan, i int, upstreamModel string) bool {
	for _, plan := range plans {
		c := plan.Constraints[i]
		if c.PerModel() {
			c = c.ForModel(upstreamModel)
		}
		if c.TokenBudget != nil || c.ValueBudget != nil {
			return true
		}
	}
	return false
}

// MarkKeyExhausted puts in cooldown an API key a request moved away from after
// an upstream 429, see keyFailoverClient.
func (e *XoloSubscriptionEnforcer) MarkKeyExhausted(ctx context.Context, metadata map[string]any, key model.ProviderKeyID, header http.Header) {
	e.markExhausted(ctx, &genaiProxy.ProxyRequest{Metadata: metadata}, key, header)
}

// PostResponse implements genaiProxy.PostResponseHook.
func (e *XoloSubscriptionEnforcer) PostResponse(ctx context.Context, req *genaiProxy.ProxyRequest, res *genaiProxy.ProxyResponse) (*genaiProxy.HookResult, error) {
	e.releaseReservations(ctx, req)

	// A 429 the proxy answers with is either one of ours, or an upstream one a
	// relay passed through as is.
	if res != nil && res.StatusCode == http.StatusTooManyRequests && !isOwnResponse(res) && !isUpstreamCapacityError(res.Body) {
		e.markExhausted(ctx, req, usedKey(req), res.Headers)
	}

	return nil, nil
}

// OnError implements genaiProxy.ErrorHook.
func (e *XoloSubscriptionEnforcer) OnError(ctx context.Context, req *genaiProxy.ProxyRequest, err error) (*genaiProxy.HookResult, error) {
	e.releaseReservations(ctx, req)

	// An upstream 429 reaches the hooks as an error: mark the plan as exhausted
	// for a cooldown so the provider is not hammered before its window resets.
	var httpErr *llm.HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusTooManyRequests && !isUpstreamCapacityError(httpErr.Body) {
		e.markExhausted(ctx, req, usedKey(req), httpErr.Header)
	}

	return nil, nil
}

func (e *XoloSubscriptionEnforcer) releaseReservations(ctx context.Context, req *genaiProxy.ProxyRequest) {
	reservations, _ := req.Metadata[metaPlanReservations].([]planReservation)
	releaseAll(ctx, reservations)
	delete(req.Metadata, metaPlanReservations)
}

// usedKey is the API key of the provider a request last went through.
func usedKey(req *genaiProxy.ProxyRequest) model.ProviderKeyID {
	key, _ := req.Metadata[MetaProviderKeyUsed].(model.ProviderKeyID)
	return key
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
// thing the 429 can be pinned on. A shared constraint is provider-wide in this
// respect: it goes into cooldown for every model, unless the plan also meters
// per model, where it stays open like the other provider-wide ones.
//
// The cooldown applies to the API key the 429 came from: the other keys of a
// pool hold plans of their own.
func (e *XoloSubscriptionEnforcer) markExhausted(ctx context.Context, req *genaiProxy.ProxyRequest, key model.ProviderKeyID, header http.Header) {
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
			slog.String("provider_key", string(key)),
			slog.Duration("cooldown", cooldown),
		)
		e.state.MarkExhausted(orgID, p.ID(), key, c.Label, upstreamModel, until)
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
// they are published under. When the models could not be listed (ok false) it
// falls back to m alone, which undercounts only when the model has aliases.
func upstreamModelIDs(models []model.LLMModel, ok bool, m model.LLMModel) []model.LLMModelID {
	if !ok {
		return []model.LLMModelID{m.ID()}
	}
	return model.UpstreamModelIDs(models, m)
}

// orgModels lists the models of an organization, cached for
// upstreamModelsCacheTTL. A store error is not cached and reports false;
// PlanModelGroupingFailures counts the requests it affects.
func (e *XoloSubscriptionEnforcer) orgModels(ctx context.Context, orgID model.OrgID) ([]model.LLMModel, bool) {
	now := time.Now()
	if models, ok := e.models.get(orgID, now); ok {
		return models, true
	}
	models, err := e.providerStore.ListLLMModels(ctx, orgID)
	if err != nil {
		metrics.PlanModelGroupingFailures.With(prometheus.Labels{metrics.LabelOrg: string(orgID)}).Inc()
		slog.WarnContext(ctx, "subscription enforcer: could not list models, metering without them",
			slog.Any("error", err), slog.String("org", string(orgID)))
		return nil, false
	}
	e.models.put(orgID, models, now)
	return models, true
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

// isUpstreamCapacityError reports whether an upstream 429 body puts the
// refusal on the provider's side (an OpenAI-style error of type server_error,
// such as "the cell is at capacity; retry shortly") rather than on the plan.
// Such a refusal says nothing of the plan's budgets: it reaches the client as
// is, without putting the plan in cooldown, which would refuse every request
// of the model as exhausted for defaultExhaustionCooldown.
//
// The body is the decoded JSON a relay passes through, the raw text of an
// llm.HTTPError, or raw JSON bytes.
func isUpstreamCapacityError(body any) bool {
	var raw []byte
	switch b := body.(type) {
	case map[string]any:
		return isCapacityErrorObject(b)
	case string:
		raw = []byte(b)
	case []byte:
		raw = b
	case json.RawMessage:
		raw = b
	default:
		return false
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return false
	}
	return isCapacityErrorObject(decoded)
}

func isCapacityErrorObject(decoded map[string]any) bool {
	errObj, ok := decoded["error"].(map[string]any)
	if !ok {
		return false
	}
	errType, _ := errObj["type"].(string)
	return errType == "server_error"
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
