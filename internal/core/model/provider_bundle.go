package model

import (
	"encoding/json"
	"math"
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
	Name             string                  `json:"name"`
	Type             string                  `json:"type"`
	BaseURL          string                  `json:"base_url,omitempty"`
	Currency         string                  `json:"currency,omitempty"`
	CloudTier        int                     `json:"cloud_tier"`
	Retry            *RetryConfig            `json:"retry,omitempty"`
	RateLimit        *RateLimitConfig        `json:"rate_limit,omitempty"`
	RequestHeaders   *ProviderRequestHeaders `json:"request_headers,omitempty"`
	BillingMode      BillingMode             `json:"billing_mode,omitempty"`
	SubscriptionPlan *SubscriptionPlan       `json:"subscription_plan,omitempty"`
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
			RequestHeaders:   p.RequestHeaders(),
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

// ParseProviderBundle reads a provider bundle and checks it can be imported.
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
	if !b.Provider.RequestHeaders.Valid() {
		return errors.Wrap(ErrInvalidProviderBundle, "request_headers name a reserved header or exceed the limits")
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
