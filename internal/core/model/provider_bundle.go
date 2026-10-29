package model

import (
	"encoding/json"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/pkg/errors"
)

// ProviderBundleKind and ProviderBundleVersion identify the portable export
// format of a provider and its models.
const (
	ProviderBundleKind    = "xolo.provider"
	ProviderBundleVersion = "1"
)

// ErrInvalidProviderBundle reports a document that is not a provider bundle
// this version of Xolo can read.
var ErrInvalidProviderBundle = errors.New("invalid provider bundle")

// ProviderBundle is the portable export of one provider and its models, to
// configure the same provider in another organization. It holds no secret
// and no internal ID: the API key stays out, and models are keyed by the
// model the provider sees (RealModel).
type ProviderBundle struct {
	Kind       string                 `json:"kind"`
	Version    string                 `json:"version"`
	ExportedAt time.Time              `json:"exported_at"`
	Provider   ProviderBundleSettings `json:"provider"`
	Models     []ProviderBundleModel  `json:"models"`
}

// ProviderBundleSettings are the settings of the exported provider.
type ProviderBundleSettings struct {
	Name           string                  `json:"name"`
	Type           string                  `json:"type"`
	BaseURL        string                  `json:"base_url,omitempty"`
	Currency       string                  `json:"currency,omitempty"`
	CloudTier      int                     `json:"cloud_tier"`
	Retry          *RetryConfig            `json:"retry,omitempty"`
	RateLimit      *RateLimitConfig        `json:"rate_limit,omitempty"`
	RequestHeaders *ProviderRequestHeaders `json:"request_headers,omitempty"`
	// OmittedHeaders names the request headers left out of the export
	// because their name looks like a credential's: they must be configured
	// again on the target. Names only, never values.
	OmittedHeaders   []string          `json:"omitted_request_headers,omitempty"`
	BillingMode      BillingMode       `json:"billing_mode,omitempty"`
	SubscriptionPlan *SubscriptionPlan `json:"subscription_plan,omitempty"`
}

// ProviderBundleModel is one model of a provider bundle. Its prices are in
// the provider's currency per million tokens, the unit providers publish.
type ProviderBundleModel struct {
	RealModel        string            `json:"real_model"`
	ProxyName        string            `json:"proxy_name"`
	Description      string            `json:"description,omitempty"`
	Kind             ModelKind         `json:"kind,omitempty"`
	Enabled          bool              `json:"enabled"`
	Prices           ModelPrices       `json:"prices"`
	ContextWindow    int64             `json:"context_window,omitempty"`
	OutputWindow     int64             `json:"output_window,omitempty"`
	ActiveParams     int64             `json:"active_params,omitempty"`
	TokensPerSecLow  float64           `json:"tokens_per_sec_low,omitempty"`
	TokensPerSecHigh float64           `json:"tokens_per_sec_high,omitempty"`
	Capabilities     ModelCapabilities `json:"capabilities"`
	APIType          string            `json:"api_type,omitempty"`
	BaseURL          string            `json:"base_url,omitempty"`
	TokenLimit       *TokenLimitConfig `json:"token_limit,omitempty"`
	ExtraBody        map[string]any    `json:"extra_body,omitempty"`
}

// ModelPrices are the prices of a model per million tokens. CachedInput is
// zero when the provider bills cached tokens like the others.
type ModelPrices struct {
	Input       float64 `json:"input"`
	CachedInput float64 `json:"cached_input,omitempty"`
	Output      float64 `json:"output"`
}

// MaxPricePerMillion bounds a price per million tokens in a bundle: far above
// any real price, far below what the storage in microcents could overflow.
const MaxPricePerMillion = 1_000_000

// PricePerMillion converts a cost in microcents per 1K tokens, the unit Xolo
// stores, into a price per million tokens.
func PricePerMillion(microcentsPer1K int64) float64 {
	return float64(microcentsPer1K) / 1000
}

// MicrocentsPer1K converts a price per million tokens into microcents per 1K
// tokens, rounded to the nearest unit Xolo can store.
func MicrocentsPer1K(pricePerMillion float64) int64 {
	return int64(math.Round(pricePerMillion * 1000))
}

// NewProviderBundle exports p and its models. With enabledOnly, the disabled
// models are left out.
func NewProviderBundle(p Provider, models []LLMModel, enabledOnly bool, now time.Time) ProviderBundle {
	bundle := ProviderBundle{
		Kind:       ProviderBundleKind,
		Version:    ProviderBundleVersion,
		ExportedAt: now.UTC(),
		Provider: ProviderBundleSettings{
			Name:             p.Name(),
			Type:             p.Type(),
			BaseURL:          p.BaseURL(),
			Currency:         p.Currency(),
			CloudTier:        p.CloudTier(),
			Retry:            p.RetryConfig(),
			RateLimit:        p.RateLimitConfig(),
			RequestHeaders:   portableHeaders(p.RequestHeaders()),
			OmittedHeaders:   omittedHeaders(p.RequestHeaders()),
			BillingMode:      p.BillingMode(),
			SubscriptionPlan: p.SubscriptionPlan(),
		},
		Models: []ProviderBundleModel{},
	}
	for _, m := range models {
		if m.ProviderID() != p.ID() || (enabledOnly && !m.Enabled()) {
			continue
		}
		bundle.Models = append(bundle.Models, BundleModelOf(m))
	}
	return bundle
}

// credentialHeaderWords mark a header name that likely carries a credential.
// The reserved headers (Authorization, X-Api-Key, Cookie…) cannot be
// configured at all; a gateway may still take its key in a header of its
// own, such as Api-Key or Ocp-Apim-Subscription-Key.
var credentialHeaderWords = []string{"key", "token", "auth", "secret", "password", "signature", "session", "cookie", "credential"}

// The word list is a safety net, not a guarantee: a credential under a name
// with none of these words is exported, and a plain header whose name holds
// one is left out. The export names what it left out (OmittedHeaders) so
// that the import can say what to configure again.
func credentialHeader(name string) bool {
	lower := strings.ToLower(name)
	return slices.ContainsFunc(credentialHeaderWords, func(w string) bool { return strings.Contains(lower, w) })
}

// omittedHeaders names, sorted, the request headers portableHeaders leaves
// out.
func omittedHeaders(h *ProviderRequestHeaders) []string {
	if h == nil {
		return nil
	}
	var names []string
	for name := range h.Headers {
		if credentialHeader(name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// MergeImportedHeaders returns the headers a provider gets when it takes the
// imported ones: those of the bundle, plus its own headers a bundle never
// carries (those portableHeaders leaves out), which the import must not
// erase. A bundle without session header keeps the provider's: a provider
// that needs one refuses the requests without it.
func MergeImportedHeaders(current, imported *ProviderRequestHeaders) *ProviderRequestHeaders {
	merged := &ProviderRequestHeaders{}
	if current != nil {
		merged.SessionHeader = current.SessionHeader
	}
	if imported != nil {
		if imported.SessionHeader != "" {
			merged.SessionHeader = imported.SessionHeader
		}
		for name, value := range imported.Headers {
			if merged.Headers == nil {
				merged.Headers = map[string]string{}
			}
			merged.Headers[name] = value
		}
	}
	if current != nil {
		for name, value := range current.Headers {
			if !credentialHeader(name) {
				continue
			}
			if merged.Headers == nil {
				merged.Headers = map[string]string{}
			}
			merged.Headers[name] = value
		}
	}
	if merged.Empty() {
		return nil
	}
	return merged
}

// portableHeaders returns the request headers that can travel in a bundle:
// those whose name does not look like a credential's. The session header,
// a name rather than a value, is kept.
// ImportedHeaders is what MergeImportedHeaders gives a provider with the
// headers current, as a bundle shows it: the dry run compares it with the
// provider's own.
func ImportedHeaders(current, imported *ProviderRequestHeaders) *ProviderRequestHeaders {
	return portableHeaders(MergeImportedHeaders(current, imported))
}

func portableHeaders(h *ProviderRequestHeaders) *ProviderRequestHeaders {
	if h == nil {
		return nil
	}
	portable := &ProviderRequestHeaders{SessionHeader: h.SessionHeader}
	for name, value := range h.Headers {
		if credentialHeader(name) {
			continue
		}
		if portable.Headers == nil {
			portable.Headers = map[string]string{}
		}
		portable.Headers[name] = value
	}
	if portable.Empty() {
		return nil
	}
	return portable
}

// BundleModelOf describes m as a bundle model.
func BundleModelOf(m LLMModel) ProviderBundleModel {
	bm := ProviderBundleModel{
		RealModel:   m.RealModel(),
		ProxyName:   m.ProxyName(),
		Description: m.Description(),
		Kind:        m.Kind(),
		Enabled:     m.Enabled(),
		Prices: ModelPrices{
			Input:  PricePerMillion(m.PromptCostPer1KTokens()),
			Output: PricePerMillion(m.CompletionCostPer1KTokens()),
		},
		ContextWindow:    m.ContextWindow(),
		OutputWindow:     m.OutputWindow(),
		ActiveParams:     m.ActiveParams(),
		TokensPerSecLow:  m.TokensPerSecLow(),
		TokensPerSecHigh: m.TokensPerSecHigh(),
		Capabilities:     m.Capabilities(),
		APIType:          m.APIType(),
		BaseURL:          m.BaseURL(),
		TokenLimit:       m.TokenLimitConfig(),
		ExtraBody:        m.ExtraBody(),
	}
	// CachedPromptCostPer1KTokens falls back on the prompt cost: only a
	// cached price of its own is exported.
	if cached := m.CachedPromptCostPer1KTokens(); cached != m.PromptCostPer1KTokens() {
		bm.Prices.CachedInput = PricePerMillion(cached)
	}
	return bm
}

// ParseProviderBundle reads a provider bundle and checks its structure. The
// limits of every provider write path are checked by the service
// (ValidateBundleSettings), which the importer calls before planning.
func ParseProviderBundle(data []byte) (ProviderBundle, error) {
	var bundle ProviderBundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return ProviderBundle{}, errors.Wrap(ErrInvalidProviderBundle, err.Error())
	}
	if err := bundle.Validate(); err != nil {
		return ProviderBundle{}, err
	}
	return bundle, nil
}

// Validate checks that the bundle is a provider bundle of a supported
// version, of a known provider type, whose models each name the model the
// provider sees under a proxy name, every model once.
func (b ProviderBundle) Validate() error {
	if b.Kind != ProviderBundleKind {
		return errors.Wrapf(ErrInvalidProviderBundle, "kind %q is not %q", b.Kind, ProviderBundleKind)
	}
	if b.Version != ProviderBundleVersion {
		return errors.Wrapf(ErrInvalidProviderBundle, "version %q is not supported", b.Version)
	}
	if !IsKnownProviderType(b.Provider.Type) {
		return errors.Wrapf(ErrInvalidProviderBundle, "unknown provider type %q", b.Provider.Type)
	}
	// The resilience settings follow the rules of the provider and model
	// forms: an enabled setting needs a positive interval and count.
	if r := b.Provider.Retry; r != nil && r.Enabled && (r.Delay <= 0 || r.MaxAttempts < 1) {
		return errors.Wrap(ErrInvalidProviderBundle, "retry needs a positive delay and at least one attempt")
	}
	if r := b.Provider.RateLimit; r != nil && r.Enabled && (r.Interval <= 0 || r.MaxBurst < 1) {
		return errors.Wrap(ErrInvalidProviderBundle, "rate_limit needs a positive interval and a burst of at least one")
	}
	if m := b.Provider.BillingMode; m != "" && m != BillingModePayg && m != BillingModeSubscription {
		return errors.Wrapf(ErrInvalidProviderBundle, "unknown billing_mode %q", m)
	}
	// Prices only mean something in their currency.
	if c := b.Provider.Currency; !slices.Contains(SupportedCurrencies, c) {
		return errors.Wrapf(ErrInvalidProviderBundle, "unsupported or missing currency %q", c)
	}
	if b.Provider.CloudTier < 0 || b.Provider.CloudTier > 2 {
		return errors.Wrapf(ErrInvalidProviderBundle, "cloud_tier %d is not 0, 1 or 2", b.Provider.CloudTier)
	}
	if err := validateBundlePlan(b.Provider.SubscriptionPlan); err != nil {
		return err
	}
	if !b.Provider.RequestHeaders.Valid() {
		return errors.Wrap(ErrInvalidProviderBundle, "request_headers name a reserved header or exceed the limits")
	}
	// A bundle never carries a header whose name looks like a credential:
	// an export leaves them out, and an import keeps the target's own. One
	// written by hand is refused rather than silently ignored.
	if h := b.Provider.RequestHeaders; h != nil {
		for name := range h.Headers {
			if credentialHeader(name) {
				return errors.Wrapf(ErrInvalidProviderBundle, "request header %q looks like a credential, which a bundle does not carry", name)
			}
		}
	}
	if b.Provider.BaseURL != "" && !IsValidBaseURL(b.Provider.BaseURL) {
		return errors.Wrapf(ErrInvalidProviderBundle, "invalid base_url %q", b.Provider.BaseURL)
	}
	seen := make(map[string]bool, len(b.Models))
	for i, m := range b.Models {
		if m.RealModel == "" || m.ProxyName == "" {
			return errors.Wrapf(ErrInvalidProviderBundle, "model %d has no real_model or proxy_name", i+1)
		}
		key := m.RealModel + "\x00" + m.ProxyName
		if seen[key] {
			return errors.Wrapf(ErrInvalidProviderBundle, "model %q is listed twice under %q", m.RealModel, m.ProxyName)
		}
		seen[key] = true
		if m.Kind != "" && !IsKnownModelKind(m.Kind) {
			return errors.Wrapf(ErrInvalidProviderBundle, "model %q has an unknown kind %q", m.RealModel, m.Kind)
		}
		if m.APIType != "" && !IsKnownProviderType(m.APIType) {
			return errors.Wrapf(ErrInvalidProviderBundle, "model %q has an unknown api_type %q", m.RealModel, m.APIType)
		}
		if m.BaseURL != "" && !IsValidBaseURL(m.BaseURL) {
			return errors.Wrapf(ErrInvalidProviderBundle, "model %q has an invalid base_url %q", m.RealModel, m.BaseURL)
		}
		served := m.APIType
		if served == "" {
			served = b.Provider.Type
		}
		if m.Kind != "" && !ProviderTypeServes(served, m.Kind) {
			return errors.Wrapf(ErrInvalidProviderBundle, "model %q is of kind %q, which %q does not serve", m.RealModel, m.Kind, served)
		}
		if l := m.TokenLimit; l != nil && l.Enabled && (l.Interval <= 0 || l.MaxTokens < 1) {
			return errors.Wrapf(ErrInvalidProviderBundle, "model %q: token_limit needs a positive interval and at least one token", m.RealModel)
		}
		if m.ContextWindow < 0 || m.OutputWindow < 0 || m.ActiveParams < 0 {
			return errors.Wrapf(ErrInvalidProviderBundle, "model %q has a negative window or parameter count", m.RealModel)
		}
		for _, tps := range []float64{m.TokensPerSecLow, m.TokensPerSecHigh} {
			if tps < 0 || math.IsNaN(tps) || math.IsInf(tps, 0) {
				return errors.Wrapf(ErrInvalidProviderBundle, "model %q has a throughput out of range", m.RealModel)
			}
		}
		for _, price := range []float64{m.Prices.Input, m.Prices.CachedInput, m.Prices.Output} {
			if price < 0 || price > MaxPricePerMillion || math.IsNaN(price) {
				return errors.Wrapf(ErrInvalidProviderBundle, "model %q has a price out of range", m.RealModel)
			}
		}
	}
	return nil
}

// NewLLMModelFromBundle builds the model bm describes, under the given
// identity: a new one, or an existing model being updated.
func NewLLMModelFromBundle(id LLMModelID, providerID ProviderID, orgID OrgID, createdAt time.Time, bm ProviderBundleModel) *BaseLLMModel {
	prompt := MicrocentsPer1K(bm.Prices.Input)
	// Zero falls back on the prompt cost, as in the model form.
	var cached int64
	if bm.Prices.CachedInput > 0 {
		cached = MicrocentsPer1K(bm.Prices.CachedInput)
	}
	return &BaseLLMModel{
		id:                          id,
		providerID:                  providerID,
		orgID:                       orgID,
		proxyName:                   bm.ProxyName,
		realModel:                   bm.RealModel,
		description:                 bm.Description,
		kind:                        bm.Kind,
		enabled:                     bm.Enabled,
		promptCostPer1KTokens:       prompt,
		cachedPromptCostPer1KTokens: cached,
		completionCostPer1KTokens:   MicrocentsPer1K(bm.Prices.Output),
		contextWindow:               bm.ContextWindow,
		outputWindow:                bm.OutputWindow,
		activeParams:                bm.ActiveParams,
		tokensPerSecLow:             bm.TokensPerSecLow,
		tokensPerSecHigh:            bm.TokensPerSecHigh,
		capabilities:                bm.Capabilities,
		createdAt:                   createdAt,
		updatedAt:                   time.Now(),
		tokenLimitConfig:            bm.TokenLimit,
		extraBody:                   bm.ExtraBody,
		apiType:                     bm.APIType,
		baseURL:                     bm.BaseURL,
	}
}

// validateBundlePlan checks the structure of a bundle's subscription plan:
// known constraint kinds, a positive window, budgets and fair-share ratios
// in range, per-model budgets on a per-model constraint only, and at least
// one concurrent request. The limits of every provider write path (counts,
// lengths, bounds) are service.ValidateBundleSettings', which an importer
// calls next.
func validateBundlePlan(plan *SubscriptionPlan) error {
	if plan == nil {
		return nil
	}
	ratio := func(r *float64) bool { return r == nil || (!math.IsNaN(*r) && *r >= 0 && *r <= 1) }
	budget := func(v *int64) bool { return v == nil || *v >= 0 }
	for _, c := range plan.Constraints {
		bad := func(what string) error {
			return errors.Wrapf(ErrInvalidProviderBundle, "plan constraint %q: %s", c.Label, what)
		}
		if c.Scope != "" && c.Scope != PlanScopeProvider && c.Scope != PlanScopeModel && c.Scope != PlanScopeShared {
			return bad("has an unknown scope")
		}
		switch c.Kind {
		case ConstraintRollingWindow:
			if c.Duration.Duration() <= 0 {
				return bad("needs a positive duration")
			}
			if !budget(c.TokenBudget) || !budget(c.ValueBudget) {
				return bad("has a negative budget")
			}
			if !ratio(c.ReserveRatio) || !ratio(c.PaceSlack) || !ratio(c.HappyHourStart) {
				return bad("has a fair-share ratio out of 0..1")
			}
			if c.HappyHourStart != nil && *c.HappyHourStart <= 0 {
				return bad("needs a positive happy hour start")
			}
			if c.HappyHourMaxLead != nil && c.HappyHourMaxLead.Duration() <= 0 {
				return bad("needs a positive happy hour lead")
			}
			if len(c.ModelBudgets) > 0 && !c.PerModel() && !c.Shared() {
				return bad("has per-model budgets but is not counted per model nor shared")
			}
			for _, mb := range c.ModelBudgets {
				if !budget(mb.TokenBudget) || !budget(mb.ValueBudget) {
					return bad("has a negative per-model budget")
				}
				if c.Shared() {
					// A shared window divides by each model's budget, and only
					// weighs the kinds it limits itself.
					if (mb.TokenBudget != nil && *mb.TokenBudget == 0) || (mb.ValueBudget != nil && *mb.ValueBudget == 0) {
						return bad("has a zero model budget, which a shared window cannot weigh")
					}
					if (mb.TokenBudget != nil && c.TokenBudget == nil) || (mb.ValueBudget != nil && c.ValueBudget == nil) {
						return bad("has a model budget of a kind the shared window does not limit")
					}
				}
			}
		case ConstraintConcurrency:
			if c.MaxConcurrent == nil || *c.MaxConcurrent < 1 {
				return bad("needs at least one concurrent request")
			}
			// A concurrency limit is always provider-wide.
			if c.Scope == PlanScopeModel || c.Scope == PlanScopeShared {
				return bad("is a concurrency limit, which cannot be counted per model")
			}
		default:
			return bad("has an unknown kind")
		}
	}
	return nil
}
