package org

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/bornholm/genai/llm/provider"
	"github.com/bornholm/go-x/slogx"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/crypto"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/org/component"
	"github.com/pkg/errors"

	_ "github.com/bornholm/genai/llm/provider/mistral"
	_ "github.com/bornholm/genai/llm/provider/openai"
	_ "github.com/bornholm/genai/llm/provider/openrouter"
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
				{Label: "Fournisseurs", Href: ""},
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
				{Label: "Fournisseurs", Href: "/orgs/" + orgSlug + "/admin/providers"},
				{Label: "Nouveau fournisseur", Href: ""},
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
	if billingMode == model.BillingModeSubscription {
		plan, err := parseSubscriptionPlanFromForm(r)
		if err != nil {
			h.renderProviderFormError(w, r, ctx, user, orgSlug, org, p, true,
				"Forfait : "+err.Error()+".")
			return
		}
		p.SetSubscriptionPlan(plan)
	}
	if err := h.providerStore.CreateProvider(ctx, p); err != nil {
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

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	p, err := h.providerStore.GetProviderByID(ctx, model.ProviderID(providerID))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "Provider not found", http.StatusNotFound)
			return
		}
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
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
				{Label: "Fournisseurs", Href: "/orgs/" + orgSlug + "/admin/providers"},
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

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	existing, err := h.providerStore.GetProviderByID(ctx, model.ProviderID(providerID))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "Provider not found", http.StatusNotFound)
			return
		}
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
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

	// --- Retry config ---
	var retryConfig *model.RetryConfig
	if r.FormValue("retry_enabled") == "on" {
		delay, err := parseDurationField(r, "retry_delay_value", "retry_delay_unit")
		if err != nil || delay <= 0 {
			h.renderProviderFormError(w, r, ctx, user, orgSlug, org, existing, false,
				"Retry : le délai doit être un entier strictement positif.")
			return
		}
		attempts, _ := strconv.Atoi(r.FormValue("retry_max_attempts"))
		if attempts < 1 {
			h.renderProviderFormError(w, r, ctx, user, orgSlug, org, existing, false,
				"Retry : le nombre de tentatives doit être ≥ 1.")
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
			h.renderProviderFormError(w, r, ctx, user, orgSlug, org, existing, false,
				"Rate limit : l'intervalle doit être un entier strictement positif.")
			return
		}
		burst, _ := strconv.Atoi(r.FormValue("rate_limit_max_burst"))
		if burst < 1 {
			h.renderProviderFormError(w, r, ctx, user, orgSlug, org, existing, false,
				"Rate limit : la capacité de burst doit être ≥ 1.")
			return
		}
		rateLimitConfig = &model.RateLimitConfig{
			Enabled:  true,
			Interval: interval,
			MaxBurst: burst,
		}
	}

	cloudTier, _ := strconv.Atoi(r.FormValue("cloud_tier"))
	billingMode := model.BillingMode(r.FormValue("billing_mode"))
	if billingMode != model.BillingModeSubscription {
		billingMode = model.BillingModePayg
	}
	var subscriptionPlan *model.SubscriptionPlan
	if billingMode == model.BillingModeSubscription {
		plan, err := parseSubscriptionPlanFromForm(r)
		if err != nil {
			h.renderProviderFormError(w, r, ctx, user, orgSlug, org, existing, false,
				"Forfait : "+err.Error()+".")
			return
		}
		subscriptionPlan = plan
	}

	updated := &updatedProviderAdapter{
		id:               existing.ID(),
		orgID:            existing.OrgID(),
		name:             r.FormValue("name"),
		pType:            r.FormValue("provider_type"),
		baseURL:          strings.TrimSpace(r.FormValue("base_url")),
		apiKey:           apiKey,
		active:           r.FormValue("active") == "on",
		currency:         currency,
		cloudTier:        cloudTier,
		createdAt:        existing.CreatedAt(),
		updatedAt:        time.Now(),
		retryConfig:      retryConfig,
		rateLimitConfig:  rateLimitConfig,
		billingMode:      billingMode,
		subscriptionPlan: subscriptionPlan,
	}

	if err := h.providerStore.SaveProvider(ctx, updated); err != nil {
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

	if err := h.providerStore.DeleteProvider(ctx, model.ProviderID(providerID)); err != nil {
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

	p, err := h.providerStore.GetProviderByID(ctx, model.ProviderID(providerID))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`<span class="text-destructive">Provider not found</span>`))
		return
	}

	decryptedKey, err := crypto.Decrypt(h.secretKey, p.APIKey())
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`<span class="text-destructive">Could not decrypt API key</span>`))
		return
	}

	_, err = testProviderConnection(ctx, p.Type(), p.BaseURL(), decryptedKey)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`<span class="text-destructive">Connection failed: ` + err.Error() + `</span>`))
		return
	}

	w.Write([]byte(`<span class="text-green-600">Connection successful ✓</span>`))
}

func testProviderConnection(ctx context.Context, providerType, baseURL, apiKey string) (bool, error) {
	name := provider.Name(providerType)
	opts := provider.NewChatCompletionProviderOptions(name)
	if opts == nil {
		return false, errors.Errorf("unknown provider %q", providerType)
	}
	v := reflect.ValueOf(opts).Elem()
	if common := v.FieldByName("CommonOptions"); common.IsValid() {
		common.FieldByName("BaseURL").SetString(baseURL)
		common.FieldByName("APIKey").SetString(apiKey)
		// dummy model — just validates credentials
		common.FieldByName("Model").SetString("gpt-3.5-turbo")
	}
	client, err := provider.Create(ctx, func(o *provider.Options) error {
		o.ChatCompletion = &provider.ResolvedClientOptions{
			Provider: name,
			Specific: opts,
		}
		return nil
	})
	if err != nil {
		return false, errors.WithStack(err)
	}
	_ = client
	return true, nil
}

func (h *Handler) getModelsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")
	providerID := r.PathValue("providerID")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	p, err := h.providerStore.GetProviderByID(ctx, model.ProviderID(providerID))
	if err != nil {
		http.Error(w, "Provider not found", http.StatusNotFound)
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
				{Label: "Fournisseurs", Href: "/orgs/" + orgSlug + "/admin/providers"},
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

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	p, err := h.providerStore.GetProviderByID(ctx, model.ProviderID(providerID))
	if err != nil {
		http.Error(w, "Provider not found", http.StatusNotFound)
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
				{Label: "Fournisseurs", Href: "/orgs/" + orgSlug + "/admin/providers"},
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
	if err != nil || pct < 0 || pct > 100 {
		return nil, errors.Errorf("valeur attendue entre 0 et 100, reçu %q", value)
	}
	fraction := pct / 100
	return &fraction, nil
}



// parseSubscriptionPlanFromForm reads the structured subscription plan fields
// submitted by the SubscriptionPlanEditor component. It returns an error the
// caller is expected to show, rather than dropping a field it cannot read: a
// tuning value that silently reverts to its default is a setting the operator
// believes they made.
func parseSubscriptionPlanFromForm(r *http.Request) (*model.SubscriptionPlan, error) {
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
			if vb := parseBudgetField(r.FormValue(prefix + "value_budget")); vb != nil {
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
				return nil, errors.Wrapf(err, "contrainte « %s » : réserve garantie", c.Label)
			}
			if c.PaceSlack, err = parsePlanRatioField(r.FormValue(prefix + "pace_slack")); err != nil {
				return nil, errors.Wrapf(err, "contrainte « %s » : tolérance de rythme", c.Label)
			}
			if c.HappyHourStart, err = parsePlanRatioField(r.FormValue(prefix + "happy_hour_start")); err != nil {
				return nil, errors.Wrapf(err, "contrainte « %s » : ouverture de fin de fenêtre", c.Label)
			}
			if lead := strings.TrimSpace(r.FormValue(prefix + "happy_hour_max_lead")); lead != "" {
				d, parseErr := time.ParseDuration(lead)
				if parseErr != nil || d <= 0 {
					return nil, errors.Errorf("contrainte « %s » : l'avance maximale de l'ouverture doit être une durée positive (ex : 1h)", c.Label)
				}
				pd := model.PlanDuration(d)
				c.HappyHourMaxLead = &pd
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
	return &model.SubscriptionPlan{Label: label, Constraints: constraints}, nil
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

// parseExtraBodyFromForm reads the key/value rows emitted by the ExtraBodyEditor
// component (extra_body_count + extra_body_k{i}_key / _value) into a map.
// Rows with an empty key are skipped. Values are typed by inference (see
// coerceExtraBodyValue). Returns (nil, nil) when no usable row is present.
func parseExtraBodyFromForm(r *http.Request) (map[string]any, error) {
	count, _ := strconv.Atoi(r.FormValue("extra_body_count"))
	if count <= 0 {
		return nil, nil
	}
	extra := make(map[string]any, count)
	for i := range count {
		prefix := fmt.Sprintf("extra_body_k%d_", i)
		key := strings.TrimSpace(r.FormValue(prefix + "key"))
		if key == "" {
			continue
		}
		if _, exists := extra[key]; exists {
			return nil, errors.Errorf("clé « %s » en double", key)
		}
		extra[key] = coerceExtraBodyValue(r.FormValue(prefix + "value"))
	}
	if len(extra) == 0 {
		return nil, nil
	}
	return extra, nil
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
		return 0, errors.Errorf("le champ %q doit être un entier strictement positif", valueField)
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

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	p, err := h.providerStore.GetProviderByID(ctx, model.ProviderID(providerID))
	if err != nil {
		http.Error(w, "Provider not found", http.StatusNotFound)
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
			"Un modèle avec le nom proxy « "+proxyName+" » existe déjà dans cette organisation.")
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
		Tools:      r.FormValue("cap_tools") == "on",
		Vision:     r.FormValue("cap_vision") == "on",
		Reasoning:  r.FormValue("cap_reasoning") == "on",
		Audio:      r.FormValue("cap_audio") == "on",
		Embeddings: r.FormValue("cap_embeddings") == "on",
	})

	extraBody, err := parseExtraBodyFromForm(r)
	if err != nil {
		h.renderModelFormError(w, r, ctx, user, orgSlug, org, p, nil, true,
			"Corps additionnel (extra_body) : "+err.Error())
		return
	}
	m.SetExtraBody(extraBody)

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

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	p, err := h.providerStore.GetProviderByID(ctx, model.ProviderID(providerID))
	if err != nil {
		http.Error(w, "Provider not found", http.StatusNotFound)
		return
	}

	m, err := h.providerStore.GetLLMModelByID(ctx, model.LLMModelID(modelID))
	if err != nil {
		http.Error(w, "Model not found", http.StatusNotFound)
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
				{Label: "Fournisseurs", Href: "/orgs/" + orgSlug + "/admin/providers"},
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

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	p, err := h.providerStore.GetProviderByID(ctx, model.ProviderID(providerID))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "Provider not found", http.StatusNotFound)
			return
		}
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	existing, err := h.providerStore.GetLLMModelByID(ctx, model.LLMModelID(modelID))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "Model not found", http.StatusNotFound)
			return
		}
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
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
			"Un modèle avec le nom proxy « "+proxyName+" » existe déjà dans cette organisation.")
		return
	}

	// --- Token limit config ---
	var tokenLimitConfig *model.TokenLimitConfig
	if r.FormValue("token_limit_enabled") == "on" {
		interval, err := parseDurationField(r, "token_limit_interval_value", "token_limit_interval_unit")
		if err != nil || interval <= 0 {
			h.renderModelFormError(w, r, ctx, user, orgSlug, org, p, existing, false,
				"Limite de tokens : l'intervalle doit être un entier strictement positif.")
			return
		}
		maxTokens, _ := strconv.Atoi(r.FormValue("token_limit_max_tokens"))
		if maxTokens < 1 {
			h.renderModelFormError(w, r, ctx, user, orgSlug, org, p, existing, false,
				"Limite de tokens : le nombre de tokens doit être ≥ 1.")
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
			"Corps additionnel (extra_body) : "+err.Error())
		return
	}

	updated := &updatedLLMModelAdapter{
		id:                          existing.ID(),
		providerID:                  existing.ProviderID(),
		orgID:                       existing.OrgID(),
		proxyName:                   proxyName,
		realModel:                   r.FormValue("real_model"),
		description:                 r.FormValue("description"),
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
			Tools:      r.FormValue("cap_tools") == "on",
			Vision:     r.FormValue("cap_vision") == "on",
			Reasoning:  r.FormValue("cap_reasoning") == "on",
			Audio:      r.FormValue("cap_audio") == "on",
			Embeddings: r.FormValue("cap_embeddings") == "on",
		},
		createdAt:        existing.CreatedAt(),
		updatedAt:        time.Now(),
		tokenLimitConfig: tokenLimitConfig,
		extraBody:        extraBody,
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
				{Label: "Fournisseurs", Href: "/orgs/" + orgSlug + "/admin/providers"},
				{Label: p.Name(), Href: "/orgs/" + orgSlug + "/admin/providers/" + string(p.ID()) + "/models"},
				{Label: m.ProxyName(), Href: ""},
			},
		},
	}
	w.WriteHeader(http.StatusUnprocessableEntity)
	templ.Handler(component.ModelForm(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) renderProviderFormError(w http.ResponseWriter, r *http.Request, ctx context.Context, user model.User, orgSlug string, org model.Organization, p model.Provider, isNew bool, errMsg string) {
	vmodel := component.ProviderFormVModel{
		Org:      org,
		Provider: p,
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
				{Label: "Fournisseurs", Href: "/orgs/" + orgSlug + "/admin/providers"},
				providerBreadcrumb(orgSlug, p, isNew),
			},
		},
	}
	w.WriteHeader(http.StatusUnprocessableEntity)
	templ.Handler(component.ProviderForm(vmodel)).ServeHTTP(w, r)
}

// providerBreadcrumb is the last crumb of the provider form: a provider being
// created has no page to link to yet.
func providerBreadcrumb(orgSlug string, p model.Provider, isNew bool) common.BreadcrumbItem {
	if isNew {
		return common.BreadcrumbItem{Label: "Nouveau fournisseur"}
	}
	return common.BreadcrumbItem{
		Label: p.Name(),
		Href:  "/orgs/" + orgSlug + "/admin/providers/" + string(p.ID()) + "/models",
	}
}

func (h *Handler) deleteModel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")
	providerID := r.PathValue("providerID")
	modelID := r.PathValue("modelID")

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
