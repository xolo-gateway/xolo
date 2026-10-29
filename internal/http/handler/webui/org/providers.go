package org

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/bornholm/genai/llm/provider"
	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/crypto"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	webcommon "github.com/xolo-gateway/xolo/internal/http/handler/webui/common"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/org/component"
	"github.com/xolo-gateway/xolo/internal/i18n"

	_ "github.com/bornholm/genai/llm/provider/anthropic"
	_ "github.com/bornholm/genai/llm/provider/gemini"
	_ "github.com/bornholm/genai/llm/provider/mistral"
	_ "github.com/bornholm/genai/llm/provider/openai"
	_ "github.com/bornholm/genai/llm/provider/openrouter"
	_ "github.com/bornholm/genai/llm/provider/responses"
	_ "github.com/bornholm/genai/llm/provider/typesafe"
)

func (h *Handler) getProvidersPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	providers, err := h.providerStore.ListProviders(ctx, org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not list providers", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	models, err := h.providerStore.ListLLMModels(ctx, org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not list models", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	modelCounts := make(map[model.ProviderID]int, len(providers))
	for _, m := range models {
		modelCounts[m.ProviderID()]++
	}

	vmodel := component.ProvidersPageVModel{
		Org:         org,
		Providers:   providers,
		ModelCounts: modelCounts,
		Success:     r.URL.Query().Get("success"),
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + orgSlug + "-providers",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: i18n.T(ctx, "org.nav.providers"), Href: ""},
			},
		},
	}

	templ.Handler(component.ProvidersPage(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) getNewProviderPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	vmodel := component.ProviderFormVModel{
		Org:   org,
		IsNew: true,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + orgSlug + "-providers",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: i18n.T(ctx, "org.nav.providers"), Href: "/orgs/" + orgSlug + "/admin/providers"},
				{Label: i18n.T(ctx, "org.nav.providers_new_provider"), Href: ""},
			},
		},
	}

	templ.Handler(component.ProviderForm(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) createProvider(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	apiKey := r.FormValue("api_key")
	encryptedKey, err := crypto.Encrypt(h.secretKey, apiKey)
	if err != nil {
		slog.ErrorContext(ctx, "could not encrypt API key", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	cloudTier, _ := strconv.Atoi(r.FormValue("cloud_tier"))
	billingMode := model.BillingMode(r.FormValue("billing_mode"))
	if billingMode != model.BillingModeSubscription {
		billingMode = model.BillingModePayg
	}
	p := model.NewProvider(org.ID(), r.FormValue("name"), r.FormValue("provider_type"), strings.TrimSpace(r.FormValue("base_url")), encryptedKey, r.FormValue("currency"))
	p.SetCloudTier(cloudTier)
	p.SetBillingMode(billingMode)
	if !model.IsKnownProviderType(p.Type()) {
		h.renderProviderFormError(w, r, ctx, user, orgSlug, org, p, true, unknownProviderTypeMessage(ctx, p.Type()))
		return
	}
	if apiKey == "" {
		h.renderProviderFormError(w, r, ctx, user, orgSlug, org, p, true, i18n.T(ctx, "org.errors.providers.api_key_required"))
		return
	}
	requestHeaders, err := parseRequestHeadersFromForm(r)
	if err != nil {
		h.renderProviderFormError(w, r, ctx, user, orgSlug, org, p, true, i18n.T(ctx, "org.errors.providers.request_headers", err.Error()))
		return
	}
	p.SetRequestHeaders(requestHeaders)
	// The creation form does not render the plan editor (see provider_form.templ,
	// which shows it under !IsNew only), so no plan field is ever posted here:
	// a subscription provider is created without a plan and gets one on edit.
	if err := h.providerStore.CreateProvider(ctx, p); err != nil {
		if webcommon.RejectLocked(w, r, err) {
			return
		}
		slog.ErrorContext(ctx, "could not create provider", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/providers?success=created", http.StatusSeeOther)
}

func (h *Handler) getEditProviderPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")
	providerID := r.PathValue("providerID")

	org, p, err := h.resolveOrgAndProvider(ctx, orgSlug, providerID)
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}

	vmodel := component.ProviderFormVModel{
		Org:      org,
		Provider: p,
		IsNew:    false,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + orgSlug + "-providers",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: i18n.T(ctx, "org.nav.providers"), Href: "/orgs/" + orgSlug + "/admin/providers"},
				{Label: p.Name(), Href: ""},
			},
		},
	}

	templ.Handler(component.ProviderForm(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) updateProvider(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")
	providerID := r.PathValue("providerID")

	org, existing, err := h.resolveOrgAndProvider(ctx, orgSlug, providerID)
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	apiKey := existing.APIKey()
	if newKey := r.FormValue("api_key"); newKey != "" {
		apiKey, err = crypto.Encrypt(h.secretKey, newKey)
		if err != nil {
			slog.ErrorContext(ctx, "could not encrypt API key", slogx.Error(err))
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
	}

	currency := r.FormValue("currency")
	if currency == "" {
		currency = existing.Currency()
	}

	billingMode := model.BillingMode(r.FormValue("billing_mode"))
	if billingMode != model.BillingModeSubscription {
		billingMode = model.BillingModePayg
	}

	// The plan is parsed before the other validations so that every error path
	// below renders the form on the plan as typed. Only the plan block makes
	// that round trip: the retry and rate-limit sections are rendered from the
	// stored configuration on every error path, as they always were. Parsing
	// the plan first at least keeps the plan itself whole when one of those
	// sections is what fails. The extra keys make the same round trip; they
	// adjust the plan's windows, so they are read on the plan as submitted. A
	// rejection of the keys themselves shows the stored ones.
	var (
		subscriptionPlan *model.SubscriptionPlan
		planErr          error
	)
	if billingMode == model.BillingModeSubscription {
		subscriptionPlan, planErr = parseSubscriptionPlanFromForm(r)
	}
	extraAPIKeys, keysErr := parseProviderKeysFromForm(r, h.secretKey, existing.ExtraAPIKeys(), subscriptionPlan)
	rejected := func(plan *model.SubscriptionPlan) providerWithPlan {
		p := submittedProvider(r, existing, billingMode, plan)
		if keysErr == nil {
			p.extraKeys = redisplayedKeys(extraAPIKeys, existing.ExtraAPIKeys())
		}
		return p
	}
	if planErr != nil {
		h.renderProviderFormError(w, r, ctx, user, orgSlug, org, rejected(subscriptionPlan), false,
			i18n.T(ctx, "org.errors.providers.plan", planErr.Error()))
		return
	}
	if keysErr != nil {
		h.renderProviderFormError(w, r, ctx, user, orgSlug, org, rejected(subscriptionPlan), false,
			i18n.T(ctx, "org.errors.providers.api_keys", keysErr.Error()))
		return
	}
	formProvider := rejected(subscriptionPlan)
	if billingMode != model.BillingModeSubscription {
		formProvider.plan = existing.SubscriptionPlan()
	}

	// --- Retry config ---
	var retryConfig *model.RetryConfig
	if r.FormValue("retry_enabled") == "on" {
		delay, err := parseDurationField(r, "retry_delay_value", "retry_delay_unit")
		if err != nil || delay <= 0 {
			h.renderProviderFormError(w, r, ctx, user, orgSlug, org, formProvider, false,
				i18n.T(ctx, "org.errors.providers.retry_delay"))
			return
		}
		attempts, _ := strconv.Atoi(r.FormValue("retry_max_attempts"))
		if attempts < 1 {
			h.renderProviderFormError(w, r, ctx, user, orgSlug, org, formProvider, false,
				i18n.T(ctx, "org.errors.providers.retry_attempts"))
			return
		}
		retryConfig = &model.RetryConfig{
			Enabled:     true,
			MaxAttempts: attempts,
			Delay:       delay,
		}
	}

	// --- Rate limit config ---
	var rateLimitConfig *model.RateLimitConfig
	if r.FormValue("rate_limit_enabled") == "on" {
		interval, err := parseDurationField(r, "rate_limit_interval_value", "rate_limit_interval_unit")
		if err != nil || interval <= 0 {
			h.renderProviderFormError(w, r, ctx, user, orgSlug, org, formProvider, false,
				i18n.T(ctx, "org.errors.providers.rate_limit_interval"))
			return
		}
		burst, _ := strconv.Atoi(r.FormValue("rate_limit_max_burst"))
		if burst < 1 {
			h.renderProviderFormError(w, r, ctx, user, orgSlug, org, formProvider, false,
				i18n.T(ctx, "org.errors.providers.rate_limit_burst"))
			return
		}
		rateLimitConfig = &model.RateLimitConfig{
			Enabled:  true,
			Interval: interval,
			MaxBurst: burst,
		}
	}

	requestHeaders, err := parseRequestHeadersFromForm(r)
	if err != nil {
		h.renderProviderFormError(w, r, ctx, user, orgSlug, org, formProvider, false,
			i18n.T(ctx, "org.errors.providers.request_headers", err.Error()))
		return
	}

	if providerType := r.FormValue("provider_type"); !model.IsKnownProviderType(providerType) {
		h.renderProviderFormError(w, r, ctx, user, orgSlug, org, formProvider, false, unknownProviderTypeMessage(ctx, providerType))
		return
	}

	cloudTier, _ := strconv.Atoi(r.FormValue("cloud_tier"))
	updated := &updatedProviderAdapter{
		id:               existing.ID(),
		orgID:            existing.OrgID(),
		name:             r.FormValue("name"),
		pType:            r.FormValue("provider_type"),
		baseURL:          strings.TrimSpace(r.FormValue("base_url")),
		apiKey:           apiKey,
		extraAPIKeys:     extraAPIKeys,
		active:           r.FormValue("active") == "on",
		currency:         currency,
		cloudTier:        cloudTier,
		createdAt:        existing.CreatedAt(),
		updatedAt:        time.Now(),
		retryConfig:      retryConfig,
		rateLimitConfig:  rateLimitConfig,
		billingMode:      billingMode,
		subscriptionPlan: subscriptionPlan,
		requestHeaders:   requestHeaders,
	}

	if err := h.providerStore.SaveProvider(ctx, updated); err != nil {
		if webcommon.RejectLocked(w, r, err) {
			return
		}
		slog.ErrorContext(ctx, "could not save provider", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/providers?success=updated", http.StatusSeeOther)
}

func (h *Handler) deleteProvider(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")
	providerID := r.PathValue("providerID")

	if _, _, err := h.resolveOrgAndProvider(ctx, orgSlug, providerID); err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}

	if err := h.providerStore.DeleteProvider(ctx, model.ProviderID(providerID)); err != nil {
		if webcommon.RejectLocked(w, r, err) {
			return
		}
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "Provider not found", http.StatusNotFound)
			return
		}
		slog.ErrorContext(ctx, "could not delete provider", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/providers?success=deleted", http.StatusSeeOther)
}

func (h *Handler) testProvider(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	providerID := r.PathValue("providerID")

	_, p, err := h.resolveOrgAndProvider(ctx, r.PathValue("orgSlug"), providerID)
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}

	decryptedKey, err := crypto.Decrypt(h.secretKey, p.APIKey())
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`<span class="text-destructive">Could not decrypt API key</span>`))
		return
	}

	_, err = testProviderConnection(ctx, p.Type(), p.BaseURL(), decryptedKey, p.RequestHeaders())
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`<span class="text-destructive">Connection failed: ` + err.Error() + `</span>`))
		return
	}

	w.Write([]byte(`<span class="text-green-600">Connection successful ✓</span>`))
}

// unknownProviderTypeMessage is the form error for a type the registry
// does not know, including a type stored before it was removed from the
// form (an edit must not silently rewrite it).
func unknownProviderTypeMessage(ctx context.Context, providerType string) string {
	if providerType == "" {
		return i18n.T(ctx, "org.errors.providers.type_required")
	}
	return i18n.T(ctx, "org.errors.providers.type_unknown", providerType)
}

func testProviderConnection(ctx context.Context, providerType, baseURL, apiKey string, headers *model.ProviderRequestHeaders) (bool, error) {
	name := provider.Name(providerType)
	// dummy model — just validates credentials
	opts := newProviderOptions(provider.NewChatCompletionProviderOptions, name, baseURL, apiKey, "gpt-3.5-turbo", headers)
	setClient := func(o *provider.Options, resolved *provider.ResolvedClientOptions) { o.ChatCompletion = resolved }
	if opts == nil {
		// A type without chat, such as typesafe, is checked through the
		// client of the models it serves.
		opts = newProviderOptions(provider.NewDecisionProviderOptions, name, baseURL, apiKey, "", headers)
		setClient = func(o *provider.Options, resolved *provider.ResolvedClientOptions) { o.Decision = resolved }
	}
	if opts == nil {
		return false, errors.Errorf("unknown provider %q", providerType)
	}
	client, err := provider.Create(ctx, func(o *provider.Options) error {
		setClient(o, &provider.ResolvedClientOptions{
			Provider: name,
			Specific: opts,
		})
		return nil
	})
	if err != nil {
		return false, errors.WithStack(err)
	}
	closeClient(client)
	return true, nil
}

func (h *Handler) getModelsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")
	providerID := r.PathValue("providerID")

	org, p, err := h.resolveOrgAndProvider(ctx, orgSlug, providerID)
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}

	models, err := h.providerStore.ListLLMModels(ctx, org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not list models", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	// Filter to this provider
	var filtered []model.LLMModel
	for _, m := range models {
		if m.ProviderID() == p.ID() {
			filtered = append(filtered, m)
		}
	}

	vmodel := component.ModelsPageVModel{
		Org:      org,
		Provider: p,
		Models:   filtered,
		Success:  r.URL.Query().Get("success"),
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + orgSlug + "-providers",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: i18n.T(ctx, "org.nav.providers"), Href: "/orgs/" + orgSlug + "/admin/providers"},
				{Label: p.Name(), Href: "/orgs/" + orgSlug + "/admin/providers/" + string(p.ID()) + "/models"},
			},
		},
	}

	templ.Handler(component.ModelsPage(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) getNewModelPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")
	providerID := r.PathValue("providerID")

	org, p, err := h.resolveOrgAndProvider(ctx, orgSlug, providerID)
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}

	vmodel := component.ModelFormVModel{
		Org:      org,
		Provider: p,
		IsNew:    true,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + orgSlug + "-providers",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: i18n.T(ctx, "org.nav.providers"), Href: "/orgs/" + orgSlug + "/admin/providers"},
				{Label: p.Name(), Href: "/orgs/" + orgSlug + "/admin/providers/" + string(p.ID()) + "/models"},
				{Label: p.Name(), Href: ""},
			},
		},
	}

	templ.Handler(component.ModelForm(vmodel)).ServeHTTP(w, r)
}

func parseIntField(v string) int64 {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func parseFloat64Field(v string) float64 {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return 0
	}
	return f
}

// parseActiveParamsField parses a billions value (e.g. "7" for 7B) into raw int64.
// Returns 0 if the input is empty or invalid.
func parseActiveParamsField(v string) int64 {
	if v == "" {
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 {
		return 0
	}
	return int64(f * 1e9)
}

// parsePlanRatioField reads one of the fair-share percentages. The stored form is
// a fraction, the edited one a percentage.
//
// An empty field clears the setting, so the allocator applies its default. An
// unusable one (a typo, a value out of range) is an error rather than a silent
// fallback: the form is the only writer of a plan, so reading "30 %" as "unset"
// would erase a setting the operator had deliberately made, and tell them
// nothing about it.
func parsePlanRatioField(value string) (*float64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	pct, err := strconv.ParseFloat(value, 64)
	// ParseFloat accepts "NaN", and NaN is neither < 0 nor > 100: without the
	// explicit check it would pass the range test, be stored on the constraint,
	// and fail the whole provider save inside json.Marshal with a bare 500.
	if err != nil || math.IsNaN(pct) || pct < 0 || pct > 100 {
		return nil, formError{key: "org.errors.providers.ratio_range", args: []any{value}}
	}
	fraction := pct / 100
	return &fraction, nil
}

// parseSubscriptionPlanFromForm reads the structured subscription plan fields
// submitted by the SubscriptionPlanEditor component. It returns an error the
// caller is expected to show, rather than dropping a field it cannot read: a
// tuning value that silently reverts to its default is a setting the operator
// believes they made.
// It returns the whole plan alongside the error — every constraint, readable
// fields filled in — so the caller can re-render the form on what the operator
// actually typed instead of on what is stored: losing a plan to one mistyped
// percentage is its own defect.
func parseSubscriptionPlanFromForm(r *http.Request) (*model.SubscriptionPlan, error) {
	ctx := r.Context()
	label := strings.TrimSpace(r.FormValue("plan_label"))
	countStr := r.FormValue("plan_constraint_count")
	if countStr == "" {
		return nil, nil
	}
	count, _ := strconv.Atoi(countStr)
	if count <= 0 && label == "" {
		return nil, nil
	}

	constraints := make([]model.PlanConstraint, 0, count)
	// The first unreadable field is reported, but parsing carries on: the plan
	// handed back with the error must hold every constraint the operator typed,
	// or one typo on the first row would wipe the rows below it off the screen.
	var firstErr error
	fail := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	for i := range count {
		prefix := fmt.Sprintf("plan_c%d_", i)
		kind := model.PlanConstraintKind(r.FormValue(prefix + "kind"))
		if kind == "" {
			continue
		}
		c := model.PlanConstraint{
			Kind:  kind,
			Label: strings.TrimSpace(r.FormValue(prefix + "label")),
		}
		switch kind {
		case model.ConstraintRollingWindow:
			if d, err := time.ParseDuration(r.FormValue(prefix + "duration")); err == nil && d > 0 {
				c.Duration = model.PlanDuration(d)
			}
			if tb := parsePlanTokenBudget(r.FormValue(prefix + "token_budget")); tb != nil {
				c.TokenBudget = tb
			}
			if vb, err := parseBudgetField(r.FormValue(prefix + "value_budget")); err != nil {
				fail(errors.New(i18n.T(ctx, "org.errors.providers.constraint_value_budget", c.Label, localizedError(ctx, err))))
			} else if vb != nil {
				c.ValueBudget = vb
			}
			// A "reset dans" countdown turns the window into a fixed (tumbling) window
			// aligned on the provider's real reset schedule. We convert it, relative to
			// now, into an absolute anchor so the alignment survives restarts.
			if anchor := computeWindowAnchor(r.FormValue(prefix+"reset_in"), c.Duration.Duration()); anchor != nil {
				c.WindowAnchor = anchor
			}
			// Fair-share tuning. Left empty, each keeps the allocator's default:
			// the form is the only writer of a plan, so a field it does not read is
			// a field the next save silently erases.
			var err error
			if c.ReserveRatio, err = parsePlanRatioField(r.FormValue(prefix + "reserve_ratio")); err != nil {
				fail(errors.New(i18n.T(ctx, "org.errors.providers.constraint_reserve_ratio", c.Label, localizedError(ctx, err))))
			}
			if c.PaceSlack, err = parsePlanRatioField(r.FormValue(prefix + "pace_slack")); err != nil {
				fail(errors.New(i18n.T(ctx, "org.errors.providers.constraint_pace_slack", c.Label, localizedError(ctx, err))))
			}
			if c.HappyHourStart, err = parsePlanRatioField(r.FormValue(prefix + "happy_hour_start")); err != nil {
				fail(errors.New(i18n.T(ctx, "org.errors.providers.constraint_happy_hour_start", c.Label, localizedError(ctx, err))))
			}
			// 0 % ouvrirait la fenêtre entière, ce que l'allocateur refuse de faire :
			// il retomberait sur son défaut, et l'éditeur réafficherait un réglage
			// que le moteur n'applique pas. C'est 100 qui désactive l'ouverture.
			if c.HappyHourStart != nil && *c.HappyHourStart <= 0 {
				fail(errors.New(i18n.T(ctx, "org.errors.providers.constraint_happy_hour_positive", c.Label)))
			}
			// Per-model metering: the budgets above become those of every
			// upstream model, each with its own window, and the overrides
			// below give some models a budget of their own. Shared metering
			// keeps a single window, the overrides setting the rate at which
			// each model fills it.
			switch scope := model.PlanConstraintScope(r.FormValue(prefix + "scope")); scope {
			case model.PlanScopeModel, model.PlanScopeShared:
				c.Scope = scope
			}
			if budgets, err := parsePlanModelBudgets(r.FormValue(prefix + "model_budgets")); err != nil {
				fail(errors.New(i18n.T(ctx, "org.errors.providers.constraint_model_budgets", c.Label, localizedError(ctx, err))))
			} else if len(budgets) > 0 {
				if c.Scope != model.PlanScopeModel && c.Scope != model.PlanScopeShared {
					fail(errors.New(i18n.T(ctx, "org.errors.providers.constraint_model_budgets_scope", c.Label)))
				}
				if c.Scope == model.PlanScopeShared {
					checkSharedModelBudgets(ctx, c, budgets, fail)
				}
				c.ModelBudgets = budgets
			}
			if lead := strings.TrimSpace(r.FormValue(prefix + "happy_hour_max_lead")); lead != "" {
				// Same parser as the "reset dans" field above: a form that accepts
				// "4d13h" in one duration field and refuses "1d" in the next is a
				// trap, and a weekly window makes days the natural unit.
				if d, ok := parseResetIn(lead); ok && d > 0 {
					pd := model.PlanDuration(d)
					c.HappyHourMaxLead = &pd
				} else {
					fail(errors.New(i18n.T(ctx, "org.errors.providers.constraint_happy_hour_lead", c.Label)))
				}
			}
		case model.ConstraintConcurrency:
			if mc, err := strconv.Atoi(r.FormValue(prefix + "max_concurrent")); err == nil && mc > 0 {
				c.MaxConcurrent = &mc
			}
		}
		constraints = append(constraints, c)
	}

	if label == "" && len(constraints) == 0 {
		return nil, nil
	}
	return &model.SubscriptionPlan{Label: label, Constraints: constraints}, firstErr
}

// checkSharedModelBudgets reports the model budgets a shared constraint cannot
// weigh: a zero one would weigh infinitely, and one of a kind the constraint
// has no budget of would weigh nothing, both silently. Models are reported in
// name order so the first error shown does not change between two saves.
func checkSharedModelBudgets(ctx context.Context, c model.PlanConstraint, budgets map[string]model.PlanBudget, fail func(error)) {
	for _, name := range slices.Sorted(maps.Keys(budgets)) {
		b := budgets[name]
		for _, kind := range []struct{ model, constraint *int64 }{{b.TokenBudget, c.TokenBudget}, {b.ValueBudget, c.ValueBudget}} {
			switch {
			case kind.model == nil:
			case *kind.model == 0:
				fail(errors.New(i18n.T(ctx, "org.errors.providers.constraint_shared_zero", c.Label, name)))
			case kind.constraint == nil:
				fail(errors.New(i18n.T(ctx, "org.errors.providers.constraint_shared_missing", c.Label, name)))
			}
		}
	}
}

// computeWindowAnchor turns a "reset dans" countdown (e.g. "4h29m", "4d13h") into an
// absolute window anchor, relative to now. The anchor is the start of the window that
// will reset at now+remaining. Returns nil when the field is empty/invalid or when the
// window duration is unset (anchoring is meaningless without a period).
func computeWindowAnchor(resetIn string, dur time.Duration) *time.Time {
	if dur <= 0 {
		return nil
	}
	remaining, ok := parseResetIn(resetIn)
	if !ok {
		return nil
	}
	// A valid countdown is at most one period; fold anything larger back into (0, dur].
	if remaining > dur {
		remaining = remaining % dur
		if remaining == 0 {
			remaining = dur
		}
	}
	// nextReset = now + remaining ; current window start = nextReset - dur.
	anchor := time.Now().Add(remaining).Add(-dur)
	return &anchor
}

// parseResetIn parses a friendly countdown string. It accepts an optional leading day
// component ("4d") followed by a standard Go duration ("13h", "29m", "13h29m"), so both
// "4d13h" and "4h29m" are valid. Returns false when the string is empty or unparseable.
func parseResetIn(s string) (time.Duration, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, false
	}
	var total time.Duration
	if i := strings.IndexByte(s, 'd'); i >= 0 {
		n, err := strconv.Atoi(strings.TrimSpace(s[:i]))
		if err != nil {
			return 0, false
		}
		total += time.Duration(n) * 24 * time.Hour
		s = strings.TrimSpace(s[i+1:])
	}
	if s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, false
		}
		total += d
	}
	if total <= 0 {
		return 0, false
	}
	return total, true
}

// parsePlanModelBudgets reads the per-model budget overrides of a constraint,
// one model per line:
//
//	kimi-k3 = 3
//	deepseek-v4.1-flash = 16, 50000000 tokens
//	longcat-2.5-preview-free = illimité
//
// The model is the upstream (real) model name. A value is in the provider's
// currency, a token budget is suffixed with "tokens", and "illimité" leaves
// the model unlimited. Blank lines and lines starting with # are ignored.
func parsePlanModelBudgets(text string) (map[string]model.PlanBudget, error) {
	budgets := make(map[string]model.PlanBudget)
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, spec, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, formError{key: "org.errors.providers.model_budgets_format", args: []any{i + 1, line}}
		}
		if _, dup := budgets[name]; dup {
			return nil, formError{key: "org.errors.providers.model_budgets_duplicate", args: []any{i + 1, name}}
		}
		var b model.PlanBudget
		for part := range strings.SplitSeq(spec, ",") {
			part = strings.TrimSpace(part)
			switch lower := strings.ToLower(part); {
			case lower == "illimité" || lower == "illimite" || lower == "unlimited":
				// No budget on this dimension.
			case strings.HasSuffix(lower, "tokens"):
				n, err := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(lower, "tokens")), 10, 64)
				if err != nil || n < 0 {
					return nil, formError{key: "org.errors.providers.model_budgets_tokens", args: []any{i + 1, part}}
				}
				b.TokenBudget = &n
			default:
				v, err := parseBudgetField(part)
				if err != nil || v == nil {
					return nil, formError{key: "org.errors.providers.model_budgets_value", args: []any{i + 1, part}}
				}
				b.ValueBudget = v
			}
		}
		budgets[name] = b
	}
	return budgets, nil
}

func parsePlanTokenBudget(v string) *int64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return nil
	}
	return &n
}

func parseCostField(v string) int64 {
	// Parse a dollar value per 1M tokens and convert to microcents per 1K tokens.
	// e.g. "2.50" ($/1M) → 2500 microcents/1K  (since 1M = 1000×1K, and 1$ = 1_000_000 microcents)
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0
	}
	return int64(f * 1_000)
}

// parseModelAPIFromForm reads the API type and base URL a model overrides its
// provider's with. Both are optional: empty keeps the provider's.
func parseModelAPIFromForm(r *http.Request) (apiType, baseURL string, err error) {
	apiType = strings.TrimSpace(r.FormValue("api_type"))
	if apiType != "" && !model.IsKnownProviderType(apiType) {
		return "", "", errors.New(i18n.T(r.Context(), "org.errors.providers.api_type_unknown", apiType))
	}
	baseURL = strings.TrimSpace(r.FormValue("api_base_url"))
	if baseURL != "" && !model.IsValidBaseURL(baseURL) {
		return "", "", errors.New(i18n.T(r.Context(), "org.errors.providers.api_base_url", baseURL))
	}
	return apiType, baseURL, nil
}

// parseModelKindFromForm reads the kind of a model served through apiType,
// its API type override, or its provider's when empty. A missing kind keeps
// current, the kind of the model updated, or for a new model (current empty)
// takes the default kind of that API. The kind the new model form preselected
// from the provider's type (kind_default) gives way to the only kind the
// override serves, when it serves no such model: the form could not know it,
// and there is nothing left to choose. Otherwise the kind must be one that
// API serves, a model of another kind could never be called: the server
// cannot tell a preselected kind from a chosen one, so it refuses rather than
// pick among several.
func parseModelKindFromForm(r *http.Request, p model.Provider, apiType string, current model.ModelKind) (model.ModelKind, error) {
	served := apiType
	if served == "" {
		served = p.Type()
	}
	kind := model.ModelKind(strings.TrimSpace(r.FormValue("kind")))
	if kind == "" {
		if current == "" {
			return model.DefaultProviderTypeKind(served), nil
		}
		kind = current
	}
	if preselected := model.ModelKind(r.FormValue("kind_default")); kind == preselected && !model.ProviderTypeServes(served, kind) {
		if only := model.ProviderTypeKinds(served); len(only) == 1 {
			return only[0], nil
		}
	}
	if !model.IsKnownModelKind(kind) {
		return "", errors.New(i18n.T(r.Context(), "org.errors.providers.kind_unknown", string(kind)))
	}
	if !model.ProviderTypeServes(served, kind) {
		return "", errors.New(i18n.T(r.Context(), "org.errors.providers.kind_not_served", served, common.ModelKindLabel(r.Context(), kind)))
	}
	return kind, nil
}

// parseExtraBodyFromForm reads the key/value rows emitted by the ExtraBodyEditor
// component (extra_body_count + extra_body_k{i}_key / _value) into a map.
// Rows with an empty key are skipped. Values are typed by inference (see
// coerceExtraBodyValue). Returns (nil, nil) when no usable row is present.
func parseExtraBodyFromForm(r *http.Request) (map[string]any, error) {
	rows, err := parseKeyValueRowsFromForm(r, "extra_body")
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	extra := make(map[string]any, len(rows))
	for key, value := range rows {
		extra[key] = coerceExtraBodyValue(value)
	}
	return extra, nil
}

// parseKeyValueRowsFromForm reads the rows a KeyValueEditor named name emits
// (name_count + name_k{i}_key / _value). Rows with an empty key are skipped,
// a key given twice is an error.
func parseKeyValueRowsFromForm(r *http.Request, name string) (map[string]string, error) {
	count, _ := strconv.Atoi(r.FormValue(name + "_count"))
	rows := make(map[string]string, max(count, 0))
	for i := range max(count, 0) {
		prefix := fmt.Sprintf("%s_k%d_", name, i)
		key := strings.TrimSpace(r.FormValue(prefix + "key"))
		if key == "" {
			continue
		}
		if _, exists := rows[key]; exists {
			return nil, errors.New(i18n.T(r.Context(), "org.errors.providers.duplicate_key", key))
		}
		rows[key] = r.FormValue(prefix + "value")
	}
	return rows, nil
}

// maxProviderKeys bounds the keys a provider holds besides its own. The form
// posts their count: it sizes what the parser allocates, so it is checked
// against this bound first.
const maxProviderKeys = 64

// redisplayedKeys are the keys a rejected form is rendered back with: the ones
// posted, a new key without id nor secret, which its value must be typed
// again for, like the provider's own key on creation.
func redisplayedKeys(posted, stored []model.ProviderKey) []model.ProviderKey {
	keys := make([]model.ProviderKey, 0, len(posted))
	for _, k := range posted {
		if !slices.ContainsFunc(stored, func(s model.ProviderKey) bool { return s.ID == k.ID }) {
			k.ID, k.APIKey = "", ""
		}
		keys = append(keys, k)
	}
	return keys
}

// parseProviderKeysFromForm reads the keys a provider holds besides its own
// (provider_key_count + provider_key_k{i}_id / _label / _value). A row keeps
// the id of the stored key it edits, and that key's secret when its value is
// left blank; a row without id is a new key, whose value is required. A row
// with neither label nor value is skipped, so an added row left empty is not
// an error. A form that does not carry the section keeps the stored keys.
func parseProviderKeysFromForm(r *http.Request, secretKey string, stored []model.ProviderKey, plan *model.SubscriptionPlan) ([]model.ProviderKey, error) {
	ctx := r.Context()
	if _, posted := r.Form["provider_key_count"]; !posted {
		// The keys section is shown for a subscription provider only: a form
		// without it keeps the stored keys.
		return stored, nil
	}
	byID := make(map[model.ProviderKeyID]model.ProviderKey, len(stored))
	for _, k := range stored {
		byID[k.ID] = k
	}
	count, err := strconv.Atoi(r.FormValue("provider_key_count"))
	if err != nil || count < 0 || count > maxProviderKeys {
		return nil, errors.New(i18n.T(ctx, "org.errors.providers.api_keys_count", maxProviderKeys))
	}
	keys := make([]model.ProviderKey, 0, count)
	labels := make(map[string]bool, count)
	seenIDs := make(map[model.ProviderKeyID]bool, count)
	for i := range count {
		prefix := fmt.Sprintf("provider_key_k%d_", i)
		id := model.ProviderKeyID(strings.TrimSpace(r.FormValue(prefix + "id")))
		label := strings.TrimSpace(r.FormValue(prefix + "label"))
		value := strings.TrimSpace(r.FormValue(prefix + "value"))

		current, known := byID[id]
		if id != "" && (!known || seenIDs[id]) {
			// A key removed meanwhile, an id that never was one or one posted
			// twice: the row is read as a new key, which must then carry its
			// value. A stored key is never kept twice.
			id = ""
		}
		seenIDs[id] = true
		if id == "" && label == "" && value == "" {
			continue
		}
		if label == "" {
			return nil, errors.New(i18n.T(ctx, "org.errors.providers.api_key_label"))
		}
		if labels[label] {
			return nil, errors.New(i18n.T(ctx, "org.errors.providers.api_key_duplicate", label))
		}
		labels[label] = true

		key := model.ProviderKey{ID: id, Label: label, APIKey: current.APIKey}
		overrides, err := parseKeyOverridesFromForm(r, prefix, label, plan)
		if err != nil {
			return nil, err
		}
		key.Overrides = overrides
		if id == "" {
			if value == "" {
				return nil, errors.New(i18n.T(ctx, "org.errors.providers.api_key_value", label))
			}
			key.ID = model.NewProviderKeyID()
		}
		if value != "" {
			encrypted, err := crypto.Encrypt(secretKey, value)
			if err != nil {
				return nil, errors.WithStack(err)
			}
			key.APIKey = encrypted
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, nil
	}
	return keys, nil
}

// parseKeyOverridesFromForm reads how a key adjusts the rolling windows of the
// plan (provider_key_k{i}_o{j}_label / _value_budget / _token_budget /
// _model_budgets / _reset_in). A window is named by its label, so an
// adjustment follows its window when the plan's rows move, and is dropped
// with it. Fields left empty keep the plan's.
func parseKeyOverridesFromForm(r *http.Request, prefix, keyLabel string, plan *model.SubscriptionPlan) (map[string]model.PlanConstraintOverride, error) {
	ctx := r.Context()
	if plan == nil {
		return nil, nil
	}
	var overrides map[string]model.PlanConstraintOverride
	for j := 0; j < maxKeyOverrides; j++ {
		op := fmt.Sprintf("%so%d_", prefix, j)
		if _, posted := r.Form[op+"label"]; !posted {
			break
		}
		label := r.FormValue(op + "label")
		i := slices.IndexFunc(plan.Constraints, func(c model.PlanConstraint) bool { return c.Label == label })
		if i < 0 || plan.Constraints[i].Kind != model.ConstraintRollingWindow {
			continue
		}
		c := plan.Constraints[i]
		fail := func(err error) error {
			return errors.New(i18n.T(ctx, "org.errors.providers.api_key_override", keyLabel, label, localizedError(ctx, err)))
		}

		var o model.PlanConstraintOverride
		if c.ValueBudget != nil {
			vb, err := parseBudgetField(strings.TrimSpace(r.FormValue(op + "value_budget")))
			if err != nil {
				return nil, fail(err)
			}
			o.ValueBudget = vb
		}
		if c.TokenBudget != nil {
			o.TokenBudget = parsePlanTokenBudget(r.FormValue(op + "token_budget"))
		}
		if c.Scope == model.PlanScopeModel || c.Scope == model.PlanScopeShared {
			budgets, err := parsePlanModelBudgets(r.FormValue(op + "model_budgets"))
			if err != nil {
				return nil, fail(err)
			}
			if len(budgets) > 0 {
				o.ModelBudgets = budgets
			}
		}
		o.WindowAnchor = computeWindowAnchor(r.FormValue(op+"reset_in"), c.Duration.Duration())
		if o.Empty() {
			continue
		}
		if c.Scope == model.PlanScopeShared && len(o.ModelBudgets) > 0 {
			var firstErr error
			checkSharedModelBudgets(ctx, o.Apply(c), o.ModelBudgets, func(err error) {
				if firstErr == nil {
					firstErr = err
				}
			})
			if firstErr != nil {
				return nil, fail(firstErr)
			}
		}
		if overrides == nil {
			overrides = map[string]model.PlanConstraintOverride{}
		}
		overrides[label] = o
	}
	return overrides, nil
}

// maxKeyOverrides bounds the windows a key's adjustments are read for, as
// maxProviderKeys bounds the keys.
const maxKeyOverrides = 64

// parseRequestHeadersFromForm reads the session header and the fixed headers
// of a provider. It returns nil when neither is set.
func parseRequestHeadersFromForm(r *http.Request) (*model.ProviderRequestHeaders, error) {
	rows, err := parseKeyValueRowsFromForm(r, "request_header")
	if err != nil {
		return nil, err
	}
	headers := &model.ProviderRequestHeaders{SessionHeader: strings.TrimSpace(r.FormValue("session_header"))}
	for name, value := range rows {
		if headers.Headers == nil {
			headers.Headers = map[string]string{}
		}
		headers.Headers[name] = strings.TrimSpace(value)
	}
	if headers.Empty() {
		return nil, nil
	}
	if !headers.Valid() {
		return nil, errors.New(i18n.T(r.Context(), "org.errors.providers.invalid_header"))
	}
	return headers, nil
}

// coerceExtraBodyValue infers a typed value from the textual form input:
// "true"/"false" (case-insensitive) become booleans, an integer or float string
// becomes a number, everything else stays a string. This keeps the editor
// key/value-only while still sending correctly typed JSON to the provider.
func coerceExtraBodyValue(raw string) any {
	v := strings.TrimSpace(raw)
	switch strings.ToLower(v) {
	case "true":
		return true
	case "false":
		return false
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return f
	}
	return v
}

// parseDurationField reads a (value, unit) pair from the form and returns a time.Duration.
// value must be a positive integer; unit must be "ms", "s", or "min" (default: "s").
// Returns 0, nil if the value field is empty or absent.
// Returns an error if the value field is present but invalid or ≤ 0.
func parseDurationField(r *http.Request, valueField, unitField string) (time.Duration, error) {
	valueStr := r.FormValue(valueField)
	if valueStr == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(valueStr, 10, 64)
	if err != nil || v <= 0 {
		return 0, errors.New(i18n.T(r.Context(), "org.errors.providers.positive_integer_field", valueField))
	}
	var multiplier time.Duration
	switch r.FormValue(unitField) {
	case "ms":
		multiplier = time.Millisecond
	case "min":
		multiplier = time.Minute
	default: // "s" or empty
		multiplier = time.Second
	}
	return time.Duration(v) * multiplier, nil
}

func (h *Handler) createModel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")
	providerID := r.PathValue("providerID")

	org, p, err := h.resolveOrgAndProvider(ctx, orgSlug, providerID)
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	proxyName := r.FormValue("proxy_name")

	// Reject duplicate proxy names within this organization.
	if conflicting, err := h.providerStore.GetLLMModelByProxyName(ctx, org.ID(), proxyName); err == nil && conflicting != nil {
		h.renderModelFormError(w, r, ctx, user, orgSlug, org, p, nil, true,
			i18n.T(ctx, "org.errors.providers.proxy_name_taken", proxyName))
		return
	}
	if msg, err := h.virtualModelConflict(ctx, org.ID(), proxyName); err != nil {
		slog.ErrorContext(ctx, "could not check virtual model names", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	} else if msg != "" {
		h.renderModelFormError(w, r, ctx, user, orgSlug, org, p, nil, true, msg)
		return
	}
	if msg, err := h.teamNameConflict(ctx, org.ID(), proxyName); err != nil {
		slog.ErrorContext(ctx, "could not check team slugs", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	} else if msg != "" {
		h.renderModelFormError(w, r, ctx, user, orgSlug, org, p, nil, true, msg)
		return
	}

	m := model.NewLLMModel(
		p.ID(), org.ID(),
		proxyName,
		r.FormValue("real_model"),
		r.FormValue("description"),
		parseCostField(r.FormValue("prompt_cost")),
		parseCostField(r.FormValue("completion_cost")),
	)
	m.SetCachedPromptCostPer1KTokens(parseCostField(r.FormValue("cached_prompt_cost")))
	m.SetContextWindow(parseIntField(r.FormValue("context_window")))
	m.SetOutputWindow(parseIntField(r.FormValue("output_window")))
	m.SetActiveParams(parseActiveParamsField(r.FormValue("active_params")))
	m.SetTokensPerSecLow(parseFloat64Field(r.FormValue("tokens_per_sec_low")))
	m.SetTokensPerSecHigh(parseFloat64Field(r.FormValue("tokens_per_sec_high")))
	m.SetCapabilities(model.ModelCapabilities{
		Tools:     r.FormValue("cap_tools") == "on",
		Vision:    r.FormValue("cap_vision") == "on",
		Reasoning: r.FormValue("cap_reasoning") == "on",
		Audio:     r.FormValue("cap_audio") == "on",
	})

	extraBody, err := parseExtraBodyFromForm(r)
	if err != nil {
		h.renderModelFormError(w, r, ctx, user, orgSlug, org, p, nil, true,
			i18n.T(ctx, "org.errors.providers.extra_body", err.Error()))
		return
	}
	m.SetExtraBody(extraBody)

	apiType, apiBaseURL, err := parseModelAPIFromForm(r)
	if err != nil {
		h.renderModelFormError(w, r, ctx, user, orgSlug, org, p, nil, true, i18n.T(ctx, "org.errors.providers.api_access", err.Error()))
		return
	}
	m.SetAPIType(apiType)
	m.SetBaseURL(apiBaseURL)

	kind, err := parseModelKindFromForm(r, p, apiType, "")
	if err != nil {
		h.renderModelFormError(w, r, ctx, user, orgSlug, org, p, nil, true, i18n.T(ctx, "org.errors.providers.kind", err.Error()))
		return
	}
	m.SetKind(kind)

	if err := h.providerStore.CreateLLMModel(ctx, m); err != nil {
		slog.ErrorContext(ctx, "could not create model", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/providers/"+string(p.ID())+"/models?success=created", http.StatusSeeOther)
}

func (h *Handler) getEditModelPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")
	providerID := r.PathValue("providerID")
	modelID := r.PathValue("modelID")

	org, p, m, err := h.resolveOrgAndProviderModel(ctx, orgSlug, providerID, modelID)
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}

	vmodel := component.ModelFormVModel{
		Org:      org,
		Provider: p,
		Model:    m,
		IsNew:    false,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + orgSlug + "-providers",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: i18n.T(ctx, "org.nav.providers"), Href: "/orgs/" + orgSlug + "/admin/providers"},
				{Label: p.Name(), Href: "/orgs/" + orgSlug + "/admin/providers/" + string(p.ID()) + "/models"},
				{Label: m.ProxyName(), Href: ""},
			},
		},
	}

	templ.Handler(component.ModelForm(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) updateModel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")
	providerID := r.PathValue("providerID")
	modelID := r.PathValue("modelID")

	org, p, existing, err := h.resolveOrgAndProviderModel(ctx, orgSlug, providerID, modelID)
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	proxyName := r.FormValue("proxy_name")

	// Reject duplicate proxy names within this organization (excluding the current model).
	if conflicting, err := h.providerStore.GetLLMModelByProxyName(ctx, org.ID(), proxyName); err == nil && conflicting != nil && conflicting.ID() != existing.ID() {
		h.renderModelFormError(w, r, ctx, user, orgSlug, org, p, existing, false,
			i18n.T(ctx, "org.errors.providers.proxy_name_taken", proxyName))
		return
	}
	if proxyName != existing.ProxyName() {
		if msg, err := h.virtualModelConflict(ctx, org.ID(), proxyName); err != nil {
			slog.ErrorContext(ctx, "could not check virtual model names", slogx.Error(err))
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		} else if msg != "" {
			h.renderModelFormError(w, r, ctx, user, orgSlug, org, p, existing, false, msg)
			return
		}
		if msg, err := h.teamNameConflict(ctx, org.ID(), proxyName); err != nil {
			slog.ErrorContext(ctx, "could not check team slugs", slogx.Error(err))
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		} else if msg != "" {
			h.renderModelFormError(w, r, ctx, user, orgSlug, org, p, existing, false, msg)
			return
		}
	}

	// --- Token limit config ---
	var tokenLimitConfig *model.TokenLimitConfig
	if r.FormValue("token_limit_enabled") == "on" {
		interval, err := parseDurationField(r, "token_limit_interval_value", "token_limit_interval_unit")
		if err != nil || interval <= 0 {
			h.renderModelFormError(w, r, ctx, user, orgSlug, org, p, existing, false,
				i18n.T(ctx, "org.errors.providers.token_limit_interval"))
			return
		}
		maxTokens, _ := strconv.Atoi(r.FormValue("token_limit_max_tokens"))
		if maxTokens < 1 {
			h.renderModelFormError(w, r, ctx, user, orgSlug, org, p, existing, false,
				i18n.T(ctx, "org.errors.providers.token_limit_max"))
			return
		}
		tokenLimitConfig = &model.TokenLimitConfig{
			Enabled:   true,
			MaxTokens: maxTokens,
			Interval:  interval,
		}
	}

	extraBody, err := parseExtraBodyFromForm(r)
	if err != nil {
		h.renderModelFormError(w, r, ctx, user, orgSlug, org, p, existing, false,
			i18n.T(ctx, "org.errors.providers.extra_body", err.Error()))
		return
	}

	apiType, apiBaseURL, err := parseModelAPIFromForm(r)
	if err != nil {
		h.renderModelFormError(w, r, ctx, user, orgSlug, org, p, existing, false, i18n.T(ctx, "org.errors.providers.api_access", err.Error()))
		return
	}

	kind, err := parseModelKindFromForm(r, p, apiType, existing.Kind())
	if err != nil {
		h.renderModelFormError(w, r, ctx, user, orgSlug, org, p, existing, false, i18n.T(ctx, "org.errors.providers.kind", err.Error()))
		return
	}

	updated := &updatedLLMModelAdapter{
		id:                          existing.ID(),
		providerID:                  existing.ProviderID(),
		orgID:                       existing.OrgID(),
		proxyName:                   proxyName,
		realModel:                   r.FormValue("real_model"),
		description:                 r.FormValue("description"),
		kind:                        kind,
		enabled:                     r.FormValue("enabled") == "on",
		promptCostPer1KTokens:       parseCostField(r.FormValue("prompt_cost")),
		cachedPromptCostPer1KTokens: parseCostField(r.FormValue("cached_prompt_cost")),
		completionCostPer1KTokens:   parseCostField(r.FormValue("completion_cost")),
		contextWindow:               parseIntField(r.FormValue("context_window")),
		outputWindow:                parseIntField(r.FormValue("output_window")),
		activeParams:                parseActiveParamsField(r.FormValue("active_params")),
		tokensPerSecLow:             parseFloat64Field(r.FormValue("tokens_per_sec_low")),
		tokensPerSecHigh:            parseFloat64Field(r.FormValue("tokens_per_sec_high")),
		capabilities: model.ModelCapabilities{
			Tools:     r.FormValue("cap_tools") == "on",
			Vision:    r.FormValue("cap_vision") == "on",
			Reasoning: r.FormValue("cap_reasoning") == "on",
			Audio:     r.FormValue("cap_audio") == "on",
		},
		createdAt:        existing.CreatedAt(),
		updatedAt:        time.Now(),
		tokenLimitConfig: tokenLimitConfig,
		extraBody:        extraBody,
		apiType:          apiType,
		baseURL:          apiBaseURL,
	}

	if err := h.providerStore.SaveLLMModel(ctx, updated); err != nil {
		slog.ErrorContext(ctx, "could not save model", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/providers/"+providerID+"/models?success=updated", http.StatusSeeOther)
}

func (h *Handler) renderModelFormError(w http.ResponseWriter, r *http.Request, ctx context.Context, user model.User, orgSlug string, org model.Organization, p model.Provider, m model.LLMModel, isNew bool, errMsg string) {
	vmodel := component.ModelFormVModel{
		Org:      org,
		Provider: p,
		Model:    m,
		IsNew:    isNew,
		Error:    errMsg,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + orgSlug + "-providers",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: i18n.T(ctx, "org.nav.providers"), Href: "/orgs/" + orgSlug + "/admin/providers"},
				{Label: p.Name(), Href: "/orgs/" + orgSlug + "/admin/providers/" + string(p.ID()) + "/models"},
				modelCrumb(ctx, m),
			},
		},
	}
	w.WriteHeader(http.StatusUnprocessableEntity)
	templ.Handler(component.ModelForm(vmodel)).ServeHTTP(w, r)
}

// modelCrumb is the last breadcrumb of the model form: the model's proxy name,
// or a plain label on a creation that failed, where nothing was stored.
func modelCrumb(ctx context.Context, m model.LLMModel) common.BreadcrumbItem {
	if m == nil {
		return common.BreadcrumbItem{Label: i18n.T(ctx, "org.nav.providers_new_model"), Href: ""}
	}
	return common.BreadcrumbItem{Label: m.ProxyName(), Href: ""}
}

// providerCrumb is the last breadcrumb of the provider form: the provider's
// models page when it exists, a dead-end label on a creation that failed,
// where the id was minted but nothing stored.
func providerCrumb(ctx context.Context, orgSlug string, p model.Provider, isNew bool) common.BreadcrumbItem {
	if isNew {
		return common.BreadcrumbItem{Label: i18n.T(ctx, "org.nav.providers_new_provider"), Href: ""}
	}
	return common.BreadcrumbItem{Label: p.Name(), Href: "/orgs/" + orgSlug + "/admin/providers/" + string(p.ID()) + "/models"}
}

func (h *Handler) renderProviderFormError(w http.ResponseWriter, r *http.Request, ctx context.Context, user model.User, orgSlug string, org model.Organization, p model.Provider, isNew bool, errMsg string) {
	vmodel := component.ProviderFormVModel{
		Org:       org,
		Provider:  p,
		IsNew:     isNew,
		Error:     errMsg,
		Submitted: r.Form,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + orgSlug + "-providers",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: i18n.T(ctx, "org.nav.providers"), Href: "/orgs/" + orgSlug + "/admin/providers"},
				providerCrumb(ctx, orgSlug, p, isNew),
			},
		},
	}
	w.WriteHeader(http.StatusUnprocessableEntity)
	templ.Handler(component.ProviderForm(vmodel)).ServeHTTP(w, r)
}

// providerWithPlan re-renders the edit form with what was submitted when
// the submit is rejected. The stored provider supplies what the form does
// not carry (id, dates, encrypted key); the submitted values win for every
// field the form does, so an operator switching a provider from PAYG to
// subscription, changing its currency or unticking "active" sees that
// choice come back rather than the stored row, and does not save the old
// value on resubmit. A blank submitted name or base URL falls back to the
// stored one; a blank type does not, the error message asks for one.
type providerWithPlan struct {
	model.Provider
	name        string
	baseURL     string
	pType       string
	currency    string
	cloudTier   int
	active      bool
	billingMode model.BillingMode
	plan        *model.SubscriptionPlan
	// extraKeys, when set, are the keys as posted, see redisplayedKeys.
	extraKeys []model.ProviderKey
}

func (p providerWithPlan) Name() string                              { return firstNonEmpty(p.name, p.Provider.Name()) }
func (p providerWithPlan) BaseURL() string                           { return firstNonEmpty(p.baseURL, p.Provider.BaseURL()) }
func (p providerWithPlan) Type() string                              { return p.pType }
func (p providerWithPlan) Currency() string                          { return p.currency }
func (p providerWithPlan) CloudTier() int                            { return p.cloudTier }
func (p providerWithPlan) Active() bool                              { return p.active }
func (p providerWithPlan) BillingMode() model.BillingMode            { return p.billingMode }
func (p providerWithPlan) SubscriptionPlan() *model.SubscriptionPlan { return p.plan }
func (p providerWithPlan) ExtraAPIKeys() []model.ProviderKey {
	if p.extraKeys != nil {
		return p.extraKeys
	}
	return p.Provider.ExtraAPIKeys()
}

// submittedProvider builds the providerWithPlan for a rejected edit from the
// posted form.
func submittedProvider(r *http.Request, existing model.Provider, billingMode model.BillingMode, plan *model.SubscriptionPlan) providerWithPlan {
	cloudTier, _ := strconv.Atoi(r.FormValue("cloud_tier"))
	return providerWithPlan{
		Provider:    existing,
		name:        r.FormValue("name"),
		baseURL:     strings.TrimSpace(r.FormValue("base_url")),
		pType:       r.FormValue("provider_type"),
		currency:    firstNonEmpty(r.FormValue("currency"), existing.Currency()),
		cloudTier:   cloudTier,
		active:      r.FormValue("active") == "on",
		billingMode: billingMode,
		plan:        plan,
	}
}

func firstNonEmpty(submitted, stored string) string {
	if submitted != "" {
		return submitted
	}
	return stored
}

func (h *Handler) deleteModel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")
	providerID := r.PathValue("providerID")
	modelID := r.PathValue("modelID")

	if _, _, _, err := h.resolveOrgAndProviderModel(ctx, orgSlug, providerID, modelID); err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}

	if err := h.providerStore.DeleteLLMModel(ctx, model.LLMModelID(modelID)); err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "Model not found", http.StatusNotFound)
			return
		}
		slog.ErrorContext(ctx, "could not delete model", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/providers/"+providerID+"/models?success=deleted", http.StatusSeeOther)
}
