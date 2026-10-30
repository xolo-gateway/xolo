// Package catalog reads the open model catalog maintained by the project
// (github.com/xolo-gateway/catalog): the models, the providers serving them,
// their prices and subscription plans, each figure with its provenance.
//
// A provider of the catalog converts into a provider bundle
// (model.ProviderBundle), so that importing from the catalog goes through
// the same dry run as importing a bundle exported by another organization.
package catalog

import (
	"bytes"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// Kind and Format identify the catalog documents this version of Xolo reads.
const (
	Kind   = "xolo.catalog"
	Format = "1"
)

// ErrInvalid reports a document that is not a catalog this version reads.
var ErrInvalid = errors.New("invalid model catalog")

// ErrNotFound reports a provider, plan or model the catalog does not have.
var ErrNotFound = errors.New("not in the model catalog")

// Status tells how a value was obtained.
type Status string

const (
	StatusPublished Status = "published"
	StatusMeasured  Status = "measured"
	StatusEstimated Status = "estimated"
)

// Provenance is where a value comes from.
type Provenance struct {
	Status Status `json:"status"`
	Source string `json:"source"`
	Date   string `json:"date"`
	Note   string `json:"note,omitempty"`
}

// Count is a sourced whole number: a token count or a parameter count.
type Count struct {
	Value int64 `json:"value"`
	Provenance
}

// Catalog is one release of the catalog.
type Catalog struct {
	Kind      string     `json:"kind"`
	Format    string     `json:"format"`
	Version   string     `json:"version"`
	BuiltAt   string     `json:"built_at"`
	Models    []Model    `json:"models"`
	Providers []Provider `json:"providers"`
}

// Model is an upstream model, whoever serves it.
type Model struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Family        string        `json:"family,omitempty"`
	Vendor        string        `json:"vendor"`
	OpenWeights   *bool         `json:"open_weights,omitempty"`
	Kind          string        `json:"kind,omitempty"`
	ContextWindow *Count        `json:"context_window,omitempty"`
	OutputWindow  *Count        `json:"output_window,omitempty"`
	Capabilities  *Capabilities `json:"capabilities,omitempty"`
	Params        *Params       `json:"params,omitempty"`
}

// Capabilities are what a model accepts and does.
type Capabilities struct {
	Tools     bool `json:"tools"`
	Vision    bool `json:"vision"`
	Reasoning bool `json:"reasoning"`
	Audio     bool `json:"audio"`
	Provenance
}

// Params are the parameter counts of a model.
type Params struct {
	Total  *Count `json:"total,omitempty"`
	Active *Count `json:"active,omitempty"`
}

// Provider is a provider, the models it serves and the plans it sells.
type Provider struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Website  string `json:"website"`
	Currency string `json:"currency"`
	Xolo     struct {
		Type    string `json:"type"`
		BaseURL string `json:"base_url"`
	} `json:"xolo"`
	Plans     []Plan     `json:"plans,omitempty"`
	Offerings []Offering `json:"offerings"`
}

// Offering is one model as a provider serves it.
type Offering struct {
	Model         string      `json:"model"`
	ModelID       string      `json:"model_id"`
	APIType       string      `json:"api_type,omitempty"`
	BaseURL       string      `json:"base_url,omitempty"`
	ContextWindow *Count      `json:"context_window,omitempty"`
	OutputWindow  *Count      `json:"output_window,omitempty"`
	Prices        Prices      `json:"prices"`
	Throughput    *Throughput `json:"throughput,omitempty"`
}

// Prices are per million tokens in the provider's currency. Input,
// CachedInput and Output are the base tier; the prices that vary with the
// prompt size or the time of day are kept as published in Tiers and Peak.
type Prices struct {
	Input       float64 `json:"input"`
	CachedInput float64 `json:"cached_input,omitempty"`
	// CacheWrite is read for completeness: Xolo bills no cache write, so it
	// is not imported.
	CacheWrite float64         `json:"cache_write,omitempty"`
	Output     float64         `json:"output"`
	Tiers      json.RawMessage `json:"tiers,omitempty"`
	Peak       json.RawMessage `json:"peak,omitempty"`
	Provenance
}

// Varies reports whether the provider publishes other prices than the base
// tier, which Xolo, storing one price per model, does not apply.
func (p Prices) Varies() bool {
	return published(p.Tiers) || published(p.Peak)
}

// published reports whether raw holds a value: an empty array or object, or
// null, holds none.
func published(raw json.RawMessage) bool {
	var v any
	if len(bytes.TrimSpace(raw)) == 0 || json.Unmarshal(raw, &v) != nil {
		return false
	}
	switch v := v.(type) {
	case nil:
		return false
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	}
	return true
}

// Throughput is a decode throughput in tokens per second.
type Throughput struct {
	Low  float64 `json:"low"`
	High float64 `json:"high"`
	Provenance
}

// Plan is a subscription plan.
type Plan struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Price *struct {
		Amount float64 `json:"amount"`
		Period string  `json:"period"`
	} `json:"price,omitempty"`
	Constraints []Constraint `json:"constraints"`
	Provenance
}

// Constraint is a plan constraint, in the vocabulary of Xolo's plans.
type Constraint struct {
	Kind          string            `json:"kind"`
	Label         string            `json:"label"`
	Duration      string            `json:"duration,omitempty"`
	Scope         string            `json:"scope,omitempty"`
	TokenBudget   *int64            `json:"token_budget,omitempty"`
	ValueBudget   *float64          `json:"value_budget,omitempty"`
	MaxConcurrent *int              `json:"max_concurrent,omitempty"`
	ModelBudgets  map[string]Budget `json:"model_budgets,omitempty"`
}

// Budget overrides the budgets of a constraint for one model, keyed by the
// offering's model_id: the provider's model ID, which Xolo's plans match on.
// Both nil means unlimited.
type Budget struct {
	TokenBudget *int64   `json:"token_budget,omitempty"`
	ValueBudget *float64 `json:"value_budget,omitempty"`
}

// Parse reads a catalog release.
func Parse(data []byte) (*Catalog, error) {
	var c Catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, errors.Wrap(ErrInvalid, err.Error())
	}
	if c.Kind != Kind || c.Format != Format {
		return nil, errors.Wrapf(ErrInvalid, "kind %q format %q, want %q format %q", c.Kind, c.Format, Kind, Format)
	}
	// Prices are only meaningful in their currency: a release with a
	// provider lacking one is refused, and the last release read is kept. A
	// provider billing in a currency Xolo does not support is left out.
	providers := c.Providers[:0]
	for _, p := range c.Providers {
		if len(p.Currency) != 3 {
			return nil, errors.Wrapf(ErrInvalid, "provider %q has no currency", p.ID)
		}
		if slices.Contains(model.SupportedCurrencies, p.Currency) {
			providers = append(providers, p)
		}
	}
	c.Providers = providers
	return &c, nil
}

// Model returns the model of the given ID, or nil.
func (c *Catalog) Model(id string) *Model {
	for i := range c.Models {
		if c.Models[i].ID == id {
			return &c.Models[i]
		}
	}
	return nil
}

// Provider returns the provider of the given ID, or nil.
func (c *Catalog) Provider(id string) *Provider {
	for i := range c.Providers {
		if c.Providers[i].ID == id {
			return &c.Providers[i]
		}
	}
	return nil
}

// Plan returns the plan of the given ID, or nil.
func (p *Provider) Plan(id string) *Plan {
	for i := range p.Plans {
		if p.Plans[i].ID == id {
			return &p.Plans[i]
		}
	}
	return nil
}

// Offering returns the offering of the given provider model ID, or nil.
func (p *Provider) Offering(modelID string) *Offering {
	for i := range p.Offerings {
		if p.Offerings[i].ModelID == modelID {
			return &p.Offerings[i]
		}
	}
	return nil
}

// ServesAt reports whether a Xolo provider at baseURL reaches the offering o
// of p: at p's own endpoint, or at the one o is served through.
func (p *Provider) ServesAt(o *Offering, baseURL string) bool {
	normalized := strings.TrimRight(baseURL, "/")
	return normalized != "" && (strings.TrimRight(p.Xolo.BaseURL, "/") == normalized || strings.TrimRight(o.BaseURL, "/") == normalized)
}

// ProviderAt returns the catalog provider a Xolo provider at baseURL most
// likely is: the one at that endpoint, else the first with an offering
// served there, by the rule of ServesAt. It returns nil when none is.
func (c *Catalog) ProviderAt(baseURL string) *Provider {
	if p := c.MatchProvider(baseURL); p != nil {
		return p
	}
	for i := range c.Providers {
		if c.Providers[i].ReachedAt(baseURL) {
			return &c.Providers[i]
		}
	}
	return nil
}

// ReachedAt reports whether a Xolo provider at baseURL is p: at its
// endpoint, or at the one one of its offerings is served through.
func (p *Provider) ReachedAt(baseURL string) bool {
	normalized := strings.TrimRight(baseURL, "/")
	if normalized != "" && strings.TrimRight(p.Xolo.BaseURL, "/") == normalized {
		return true
	}
	return slices.ContainsFunc(p.Offerings, func(o Offering) bool { return p.ServesAt(&o, baseURL) })
}

// MatchProvider returns the catalog provider an existing provider most likely
// is: the one with the same base URL, else none.
func (c *Catalog) MatchProvider(baseURL string) *Provider {
	normalized := strings.TrimRight(baseURL, "/")
	for i := range c.Providers {
		if strings.TrimRight(c.Providers[i].Xolo.BaseURL, "/") == normalized {
			return &c.Providers[i]
		}
	}
	return nil
}

// Bundle converts the provider providerID into a provider bundle, with the
// plan planID as its subscription plan when it is not empty. Each offering
// gives one model, under its provider model ID as proxy name, with the
// windows of the offering or else of the model, and the model's parameter
// counts and capabilities. Prices are the base tier.
func (c *Catalog) Bundle(providerID, planID string) (model.ProviderBundle, error) {
	p := c.Provider(providerID)
	if p == nil {
		return model.ProviderBundle{}, errors.Wrapf(ErrNotFound, "provider %q", providerID)
	}
	bundle := model.ProviderBundle{
		Kind:       model.ProviderBundleKind,
		Version:    model.ProviderBundleVersion,
		ExportedAt: time.Now().UTC(),
		Provider: model.ProviderBundleSettings{
			Name:     p.Name,
			Type:     p.Xolo.Type,
			BaseURL:  p.Xolo.BaseURL,
			Currency: p.Currency,
		},
		Models: make([]model.ProviderBundleModel, 0, len(p.Offerings)),
	}
	if planID != "" {
		plan := p.Plan(planID)
		if plan == nil {
			return model.ProviderBundle{}, errors.Wrapf(ErrNotFound, "plan %q of %q", planID, providerID)
		}
		sp, err := plan.SubscriptionPlan()
		if err != nil {
			return model.ProviderBundle{}, err
		}
		// A per-model budget is keyed by the model_id of an offering, the
		// model Xolo's plans match on: under a key no offering has, the
		// release is wrong. One of an offering the bundle leaves out applies
		// to no model and goes, unless its constraint is shared: its
		// smallest budget weighs the models the plan does not list.
		for _, constraint := range sp.Constraints {
			for key := range constraint.ModelBudgets {
				i := slices.IndexFunc(p.Offerings, func(o Offering) bool { return o.ModelID == key })
				if i < 0 {
					return model.ProviderBundle{}, errors.Wrapf(ErrInvalid, "plan %q of %q: model budget %q names no offering", planID, providerID, key)
				}
				if !p.Offerings[i].Reachable(p) && !constraint.Shared() {
					delete(constraint.ModelBudgets, key)
				}
			}
		}
		bundle.Provider.BillingMode = model.BillingModeSubscription
		bundle.Provider.SubscriptionPlan = sp
	}
	for _, o := range p.Offerings {
		if !o.Reachable(p) {
			continue
		}
		bundle.Models = append(bundle.Models, c.BundleModel(p, o))
	}
	return bundle, nil
}

// Reachable reports whether Xolo can call the offering: an offering served
// through another API than its provider's needs that API's base URL, the
// provider's own being of no use to it. The catalog schema requires it; an
// entry without one is left out rather than imported at a wrong endpoint.
func (o Offering) Reachable(p *Provider) bool {
	return o.APIType == "" || o.APIType == p.Xolo.Type || o.BaseURL != ""
}

// BundleModel describes the offering o of p as a bundle model.
func (c *Catalog) BundleModel(p *Provider, o Offering) model.ProviderBundleModel {
	bm := model.ProviderBundleModel{
		RealModel: o.ModelID,
		ProxyName: o.ModelID,
		Enabled:   true,
		Prices: model.ModelPrices{
			Input:       o.Prices.Input,
			CachedInput: o.Prices.CachedInput,
			Output:      o.Prices.Output,
		},
	}
	// The API type and base URL are kept when they are not the provider's
	// own.
	if o.APIType != p.Xolo.Type {
		bm.APIType = o.APIType
	}
	if strings.TrimRight(o.BaseURL, "/") != strings.TrimRight(p.Xolo.BaseURL, "/") {
		bm.BaseURL = o.BaseURL
	}
	if m := c.Model(o.Model); m != nil {
		bm.Description = m.Name
		if m.Kind != "" {
			bm.Kind = model.ModelKind(m.Kind)
		}
		if m.ContextWindow != nil {
			bm.ContextWindow = m.ContextWindow.Value
		}
		if m.OutputWindow != nil {
			bm.OutputWindow = m.OutputWindow.Value
		}
		if m.Capabilities != nil {
			bm.Capabilities = model.ModelCapabilities{Tools: m.Capabilities.Tools, Vision: m.Capabilities.Vision, Reasoning: m.Capabilities.Reasoning, Audio: m.Capabilities.Audio}
		}
		if m.Params != nil && m.Params.Active != nil {
			bm.ActiveParams = m.Params.Active.Value
		}
	}
	// The provider may serve a narrower window than the model's.
	if o.ContextWindow != nil {
		bm.ContextWindow = o.ContextWindow.Value
	}
	if o.OutputWindow != nil {
		bm.OutputWindow = o.OutputWindow.Value
	}
	if o.Throughput != nil {
		bm.TokensPerSecLow, bm.TokensPerSecHigh = o.Throughput.Low, o.Throughput.High
	}
	return bm
}

// SubscriptionPlan converts the plan into a Xolo subscription plan. Value
// budgets are in the provider's currency, stored in microcents.
func (p Plan) SubscriptionPlan() (*model.SubscriptionPlan, error) {
	sp := &model.SubscriptionPlan{Label: p.Name}
	for _, c := range p.Constraints {
		pc := model.PlanConstraint{
			Kind:          model.PlanConstraintKind(c.Kind),
			Label:         c.Label,
			TokenBudget:   c.TokenBudget,
			ValueBudget:   microcents(c.ValueBudget),
			MaxConcurrent: c.MaxConcurrent,
		}
		if c.Duration != "" {
			d, err := time.ParseDuration(c.Duration)
			if err != nil {
				return nil, errors.Wrapf(ErrInvalid, "plan %q: duration %q", p.ID, c.Duration)
			}
			pc.Duration = model.PlanDuration(d)
		}
		switch c.Scope {
		case "model":
			pc.Scope = model.PlanScopeModel
		case "shared":
			pc.Scope = model.PlanScopeShared
		}
		if len(c.ModelBudgets) > 0 {
			pc.ModelBudgets = make(map[string]model.PlanBudget, len(c.ModelBudgets))
			for id, b := range c.ModelBudgets {
				pc.ModelBudgets[id] = model.PlanBudget{TokenBudget: b.TokenBudget, ValueBudget: microcents(b.ValueBudget)}
			}
		}
		sp.Constraints = append(sp.Constraints, pc)
	}
	return sp, nil
}

// microcents converts an amount in currency units into microcents
// (1 microcent = 0.000001 unit).
func microcents(amount *float64) *int64 {
	if amount == nil {
		return nil
	}
	v := int64(math.Round(*amount * 1_000_000))
	return &v
}
