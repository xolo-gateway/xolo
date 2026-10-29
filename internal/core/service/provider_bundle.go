package service

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// BundleModelAction is what importing a provider bundle does to one of its
// models.
type BundleModelAction string

const (
	BundleModelCreate    BundleModelAction = "create"
	BundleModelUpdate    BundleModelAction = "update"
	BundleModelUnchanged BundleModelAction = "unchanged"
	BundleModelSkip      BundleModelAction = "skip"
)

// BundleFieldChange is one field an import would change, with its current
// and imported values rendered for display. Conflict, when set, explains why
// the field cannot be applied.
type BundleFieldChange struct {
	Field    string
	From     string
	To       string
	Conflict string
}

// BundleModelPlan is what importing one model of a bundle would do.
type BundleModelPlan struct {
	// Index is the position of the model in the bundle, which names it in a
	// selection.
	Index    int
	Model    model.ProviderBundleModel
	Existing model.LLMModel
	Action   BundleModelAction
	// Changes lists the fields an update would change.
	Changes []BundleFieldChange
	// Conflict explains a skip on the proxy name.
	Conflict string
	// KindNotServed, when set, is the provider type whose API does not serve
	// the model's kind: the model is skipped, as the model form refuses it.
	KindNotServed string
}

// ProviderBundlePlan is the dry run of a provider bundle import: nothing is
// written until it is applied.
type ProviderBundlePlan struct {
	Bundle model.ProviderBundle
	// Target is the provider the models go to, nil when the import creates
	// one.
	Target model.Provider
	// Settings lists the provider settings the import would change on the
	// target, or set on the new provider.
	Settings []BundleFieldChange
	// Billing is the change of billing mode and subscription plan, kept apart
	// as it is applied only on request: two organizations sharing one
	// upstream account split its budgets rather than copy them. Nil when the
	// bundle changes neither.
	Billing *BundleFieldChange
	Models  []BundleModelPlan
}

// Changed reports whether applying the whole plan would write anything.
func (p ProviderBundlePlan) Changed() bool {
	if p.Target == nil || len(p.Settings) > 0 || p.Billing != nil {
		return true
	}
	for _, m := range p.Models {
		if m.Action == BundleModelCreate || m.Action == BundleModelUpdate {
			return true
		}
	}
	return false
}

// ProxyNames checks the proxy names an import gives. Conflict explains why a
// name cannot be used in the organization, or is empty when it is free;
// Declare records a name the import takes, so that two models of a bundle
// cannot take the same one.
type ProxyNames interface {
	Conflict(proxyName string) string
	Declare(proxyName string)
	// Release frees the name a renamed model leaves.
	Release(proxyName string)
}

// Provider settings fields.
const (
	BundleFieldType           = "type"
	BundleFieldBaseURL        = "base_url"
	BundleFieldCurrency       = "currency"
	BundleFieldCloudTier      = "cloud_tier"
	BundleFieldRetry          = "retry"
	BundleFieldRateLimit      = "rate_limit"
	BundleFieldRequestHeaders = "request_headers"
	BundleFieldBilling        = "billing"
)

// Model fields.
const (
	BundleFieldProxyName    = "proxy_name"
	BundleFieldDescription  = "description"
	BundleFieldKind         = "kind"
	BundleFieldEnabled      = "enabled"
	BundleFieldPrices       = "prices"
	BundleFieldWindows      = "windows"
	BundleFieldActiveParams = "active_params"
	BundleFieldThroughput   = "throughput"
	BundleFieldCapabilities = "capabilities"
	BundleFieldAPI          = "api"
	BundleFieldTokenLimit   = "token_limit"
	BundleFieldExtraBody    = "extra_body"
)

// bundleModelField reads and writes one field of a bundle model. Related
// values (the three prices, the two windows, the API type and base URL) form
// one field, as they only make sense together.
type bundleModelField struct {
	name string
	get  func(m model.ProviderBundleModel) any
	set  func(dst *model.ProviderBundleModel, src model.ProviderBundleModel)
}

var bundleModelFields = []bundleModelField{
	{BundleFieldProxyName, func(m model.ProviderBundleModel) any { return m.ProxyName },
		func(d *model.ProviderBundleModel, s model.ProviderBundleModel) { d.ProxyName = s.ProxyName }},
	{BundleFieldDescription, func(m model.ProviderBundleModel) any { return m.Description },
		func(d *model.ProviderBundleModel, s model.ProviderBundleModel) { d.Description = s.Description }},
	{BundleFieldKind, func(m model.ProviderBundleModel) any { return model.KindOrDefault(m.Kind) },
		func(d *model.ProviderBundleModel, s model.ProviderBundleModel) { d.Kind = s.Kind }},
	{BundleFieldEnabled, func(m model.ProviderBundleModel) any { return m.Enabled },
		func(d *model.ProviderBundleModel, s model.ProviderBundleModel) { d.Enabled = s.Enabled }},
	{BundleFieldPrices, func(m model.ProviderBundleModel) any { return storedPrices(m.Prices) },
		func(d *model.ProviderBundleModel, s model.ProviderBundleModel) { d.Prices = s.Prices }},
	{BundleFieldWindows, func(m model.ProviderBundleModel) any { return [2]int64{m.ContextWindow, m.OutputWindow} },
		func(d *model.ProviderBundleModel, s model.ProviderBundleModel) {
			d.ContextWindow, d.OutputWindow = s.ContextWindow, s.OutputWindow
		}},
	{BundleFieldActiveParams, func(m model.ProviderBundleModel) any { return m.ActiveParams },
		func(d *model.ProviderBundleModel, s model.ProviderBundleModel) { d.ActiveParams = s.ActiveParams }},
	{BundleFieldThroughput, func(m model.ProviderBundleModel) any { return [2]float64{m.TokensPerSecLow, m.TokensPerSecHigh} },
		func(d *model.ProviderBundleModel, s model.ProviderBundleModel) {
			d.TokensPerSecLow, d.TokensPerSecHigh = s.TokensPerSecLow, s.TokensPerSecHigh
		}},
	{BundleFieldCapabilities, func(m model.ProviderBundleModel) any { return m.Capabilities },
		func(d *model.ProviderBundleModel, s model.ProviderBundleModel) { d.Capabilities = s.Capabilities }},
	{BundleFieldAPI, func(m model.ProviderBundleModel) any { return [2]string{m.APIType, m.BaseURL} },
		func(d *model.ProviderBundleModel, s model.ProviderBundleModel) {
			d.APIType, d.BaseURL = s.APIType, s.BaseURL
		}},
	{BundleFieldTokenLimit, func(m model.ProviderBundleModel) any { return enabledTokenLimit(m.TokenLimit) },
		func(d *model.ProviderBundleModel, s model.ProviderBundleModel) { d.TokenLimit = s.TokenLimit }},
	{BundleFieldExtraBody, func(m model.ProviderBundleModel) any { return jsonValue(m.ExtraBody) },
		func(d *model.ProviderBundleModel, s model.ProviderBundleModel) { d.ExtraBody = s.ExtraBody }},
}

// storedPrices are the prices as Xolo stores them, so that a price the
// storage rounds is not reported as a change on every import, and a cached
// price equal to the input price reads like no cached price.
func storedPrices(p model.ModelPrices) [3]int64 {
	input := model.MicrocentsPer1K(p.Input)
	cached := model.MicrocentsPer1K(p.CachedInput)
	if cached == input {
		cached = 0
	}
	return [3]int64{input, cached, model.MicrocentsPer1K(p.Output)}
}

// enabledTokenLimit is a token limit as it acts: a disabled one is none.
func enabledTokenLimit(c *model.TokenLimitConfig) any {
	if c == nil || !c.Enabled {
		return nil
	}
	return *c
}

// jsonValue renders v as compact JSON, empty for an empty value, so that a
// nil map and an empty one compare equal.
func jsonValue(v any) string {
	if v == nil {
		return ""
	}
	// A nil pointer or an empty map is an absent value, not "null".
	if rv := reflect.ValueOf(v); (rv.Kind() == reflect.Pointer && rv.IsNil()) || (rv.Kind() == reflect.Map && rv.Len() == 0) {
		return ""
	}
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(data)
}

// displayValue renders a field value for the dry run.
func displayValue(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case int:
		return strconv.Itoa(v)
	case [3]int64:
		// Prices: per million tokens, as providers publish them.
		cached := "-"
		if v[1] != 0 {
			cached = formatFloat(model.PricePerMillion(v[1]))
		}
		return formatFloat(model.PricePerMillion(v[0])) + " / " + cached + " / " + formatFloat(model.PricePerMillion(v[2]))
	case [2]int64:
		return strconv.FormatInt(v[0], 10) + " / " + strconv.FormatInt(v[1], 10)
	case [2]float64:
		return formatFloat(v[0]) + " / " + formatFloat(v[1])
	case [2]string:
		if v[1] == "" {
			return v[0]
		}
		return v[0] + " " + v[1]
	default:
		return jsonValue(v)
	}
}

func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// PlanProviderBundleImport compares bundle with the target provider and its
// models, and returns what importing it would do. target is nil when the
// import creates a new provider, and targetModels are then empty. names
// checks the proxy names of the models to create or rename.
func PlanProviderBundleImport(bundle model.ProviderBundle, target model.Provider, targetModels []model.LLMModel, names ProxyNames) ProviderBundlePlan {
	plan := ProviderBundlePlan{Bundle: bundle, Target: target}
	plan.Settings, plan.Billing = planProviderSettings(bundle.Provider, target)

	// A bundle model updates the target model with the same real model and
	// proxy name, else the only remaining target model with its real model.
	var own []model.LLMModel
	for _, m := range targetModels {
		if target != nil && m.ProviderID() == target.ID() {
			own = append(own, m)
		}
	}
	matched := make(map[model.LLMModelID]bool, len(own))
	// byRealModel returns the only target model not matched yet that serves
	// realModel, or nil when there is none or several.
	byRealModel := func(realModel string) model.LLMModel {
		var found model.LLMModel
		for _, m := range own {
			if !matched[m.ID()] && m.RealModel() == realModel {
				if found != nil {
					return nil
				}
				found = m
			}
		}
		return found
	}
	existing := make([]model.LLMModel, len(bundle.Models))
	// Exact matches go first, so that a model renamed in the bundle cannot
	// take the target model another bundle model names exactly.
	for i, bm := range bundle.Models {
		for _, m := range own {
			if !matched[m.ID()] && m.RealModel() == bm.RealModel && m.ProxyName() == bm.ProxyName {
				existing[i], matched[m.ID()] = m, true
				break
			}
		}
	}
	for i, bm := range bundle.Models {
		if existing[i] == nil {
			if m := byRealModel(bm.RealModel); m != nil {
				existing[i], matched[m.ID()] = m, true
			}
		}
	}

	// The models to update are planned first, so that a name one of them
	// leaves is free for a model to create.
	plans := make([]BundleModelPlan, len(bundle.Models))
	for _, updates := range []bool{true, false} {
		for i, bm := range bundle.Models {
			if (existing[i] != nil) != updates {
				continue
			}
			plans[i] = planBundleModel(i, bm, existing[i], target, bundle.Provider.Type, names)
		}
	}
	plan.Models = plans
	return plan
}

// planBundleModel plans the import of the bundle model bm, at index i, into
// the target model existing (nil to create it).
func planBundleModel(i int, bm model.ProviderBundleModel, existing model.LLMModel, target model.Provider, bundleType string, names ProxyNames) BundleModelPlan {
	mp := BundleModelPlan{Index: i, Model: bm, Existing: existing}

	// The kind must be one the API serving the model serves, as the model
	// form requires: the target's own API when the bundle sets none. An
	// unset kind is the default kind of that API.
	served := bm.APIType
	if served == "" {
		served = bundleType
		if target != nil {
			served = target.Type()
		}
	}
	if mp.Model.Kind == "" {
		mp.Model.Kind = model.DefaultProviderTypeKind(served)
	}
	if !model.ProviderTypeServes(served, mp.Model.Kind) {
		mp.Action, mp.KindNotServed = BundleModelSkip, served
		return mp
	}

	if existing == nil {
		mp.Action = BundleModelCreate
		if conflict := names.Conflict(bm.ProxyName); conflict != "" {
			mp.Action, mp.Conflict = BundleModelSkip, conflict
		} else {
			names.Declare(bm.ProxyName)
		}
		return mp
	}

	current := model.BundleModelOf(existing)
	for _, f := range bundleModelFields {
		from, to := f.get(current), f.get(mp.Model)
		if reflect.DeepEqual(from, to) {
			continue
		}
		change := BundleFieldChange{Field: f.name, From: displayValue(from), To: displayValue(to)}
		if f.name == BundleFieldProxyName {
			if conflict := names.Conflict(bm.ProxyName); conflict != "" {
				change.Conflict = conflict
			} else {
				names.Declare(bm.ProxyName)
				names.Release(existing.ProxyName())
			}
		}
		mp.Changes = append(mp.Changes, change)
	}
	mp.Action = BundleModelUpdate
	if len(mp.Changes) == 0 {
		mp.Action = BundleModelUnchanged
	}
	return mp
}

func planProviderSettings(s model.ProviderBundleSettings, target model.Provider) ([]BundleFieldChange, *BundleFieldChange) {
	var current model.ProviderBundleSettings
	if target != nil {
		current = model.NewProviderBundle(target, nil, false, time.Time{}).Provider
	}
	fields := []struct {
		name     string
		from, to any
	}{
		{BundleFieldType, current.Type, s.Type},
		{BundleFieldBaseURL, current.BaseURL, s.BaseURL},
		{BundleFieldCurrency, current.Currency, s.Currency},
		{BundleFieldCloudTier, current.CloudTier, s.CloudTier},
		{BundleFieldRetry, jsonValue(current.Retry), jsonValue(s.Retry)},
		{BundleFieldRateLimit, jsonValue(current.RateLimit), jsonValue(s.RateLimit)},
		{BundleFieldRequestHeaders, jsonValue(current.RequestHeaders), jsonValue(s.RequestHeaders)},
	}
	var changes []BundleFieldChange
	for _, f := range fields {
		if target != nil && reflect.DeepEqual(f.from, f.to) {
			continue
		}
		changes = append(changes, BundleFieldChange{Field: f.name, From: displayValue(f.from), To: displayValue(f.to)})
	}

	from, to := billingValue(current), billingValue(s)
	if from == to || (target == nil && s.SubscriptionPlan == nil && billingModeOrPayg(s.BillingMode) == model.BillingModePayg) {
		return changes, nil
	}
	return changes, &BundleFieldChange{Field: BundleFieldBilling, From: from, To: to}
}

func billingValue(s model.ProviderBundleSettings) string {
	return jsonValue(struct {
		Mode model.BillingMode       `json:"mode"`
		Plan *model.SubscriptionPlan `json:"plan,omitempty"`
	}{billingModeOrPayg(s.BillingMode), s.SubscriptionPlan})
}

// BundleSelection is what the operator chose to apply from a plan.
type BundleSelection struct {
	// Settings are the provider settings fields to apply to an existing
	// target; a new provider takes them all.
	Settings map[string]bool
	// Billing applies the billing mode and subscription plan.
	Billing bool
	// Models are the indexes of the bundle models to create.
	Models map[int]bool
	// Fields are, per bundle model index, the fields to update.
	Fields map[int]map[string]bool
}

// NewProviderTarget is the provider a bundle import creates.
type NewProviderTarget struct {
	OrgID model.OrgID
	Name  string
	// APIKey is already encrypted.
	APIKey string
}

// BundleApplyResult counts what an import wrote.
type BundleApplyResult struct {
	Provider model.Provider
	// ProviderChanged reports that the provider was created or updated.
	ProviderChanged bool
	Created         int
	Updated         int
	// Failed lists the models the store refused, the import going on with
	// the others.
	Failed []BundleModelFailure
}

// Wrote reports whether the import wrote anything.
func (r BundleApplyResult) Wrote() bool {
	return r.ProviderChanged || r.Created > 0 || r.Updated > 0
}

// BundleModelFailure is a model of the bundle the store refused to write.
type BundleModelFailure struct {
	Index     int
	ProxyName string
	Err       error
}

// ApplyProviderBundle writes the selected part of plan through store. When
// plan has no target, newProvider describes the provider to create. Every
// write goes through the store, so that the projections, the audit and the
// event feed see it. The writes are not atomic: an import interrupted half
// way is completed by importing the bundle again, which only applies what
// still differs.
func ApplyProviderBundle(ctx context.Context, store port.ProviderStore, plan ProviderBundlePlan, sel BundleSelection, newProvider *NewProviderTarget) (BundleApplyResult, error) {
	var result BundleApplyResult

	target, changed, err := applyProviderSettings(ctx, store, plan, sel, newProvider)
	if err != nil {
		return result, err
	}
	result.Provider, result.ProviderChanged = target, changed

	for _, mp := range plan.Models {
		switch mp.Action {
		case BundleModelCreate:
			if !sel.Models[mp.Index] {
				continue
			}
			m := model.NewLLMModelFromBundle(model.NewLLMModelID(), target.ID(), target.OrgID(), time.Now(), mp.Model)
			if err := store.CreateLLMModel(ctx, m); err != nil {
				result.Failed = append(result.Failed, BundleModelFailure{Index: mp.Index, ProxyName: m.ProxyName(), Err: errors.WithStack(err)})
				continue
			}
			result.Created++
		case BundleModelUpdate:
			fields := sel.Fields[mp.Index]
			updated := model.BundleModelOf(mp.Existing)
			changed := false
			for _, change := range mp.Changes {
				if !fields[change.Field] || change.Conflict != "" {
					continue
				}
				for _, f := range bundleModelFields {
					if f.name == change.Field {
						f.set(&updated, mp.Model)
						changed = true
					}
				}
			}
			if !changed {
				continue
			}
			m := model.NewLLMModelFromBundle(mp.Existing.ID(), mp.Existing.ProviderID(), mp.Existing.OrgID(), mp.Existing.CreatedAt(), updated)
			if err := store.SaveLLMModel(ctx, m); err != nil {
				result.Failed = append(result.Failed, BundleModelFailure{Index: mp.Index, ProxyName: m.ProxyName(), Err: errors.WithStack(err)})
				continue
			}
			result.Updated++
		}
	}
	return result, nil
}

func applyProviderSettings(ctx context.Context, store port.ProviderStore, plan ProviderBundlePlan, sel BundleSelection, newProvider *NewProviderTarget) (model.Provider, bool, error) {
	s := plan.Bundle.Provider
	if plan.Target == nil {
		if newProvider == nil {
			return nil, false, errors.New("no provider to import into")
		}
		p := model.NewProvider(newProvider.OrgID, newProvider.Name, s.Type, s.BaseURL, newProvider.APIKey, s.Currency)
		p.SetCloudTier(s.CloudTier)
		p.SetRetryConfig(s.Retry)
		p.SetRateLimitConfig(s.RateLimit)
		p.SetRequestHeaders(s.RequestHeaders)
		if sel.Billing && plan.Billing != nil {
			p.SetBillingMode(billingModeOrPayg(s.BillingMode))
			p.SetSubscriptionPlan(s.SubscriptionPlan)
		}
		if err := store.CreateProvider(ctx, p); err != nil {
			return nil, false, errors.Wrap(err, "could not create provider")
		}
		return p, true, nil
	}

	t := plan.Target
	updated := model.CopyProvider(t)
	changed := false
	for _, change := range plan.Settings {
		if !sel.Settings[change.Field] {
			continue
		}
		changed = true
		switch change.Field {
		case BundleFieldType:
			updated.SetType(s.Type)
		case BundleFieldBaseURL:
			updated.SetBaseURL(s.BaseURL)
		case BundleFieldCurrency:
			updated.SetCurrency(s.Currency)
		case BundleFieldCloudTier:
			updated.SetCloudTier(s.CloudTier)
		case BundleFieldRetry:
			updated.SetRetryConfig(s.Retry)
		case BundleFieldRateLimit:
			updated.SetRateLimitConfig(s.RateLimit)
		case BundleFieldRequestHeaders:
			updated.SetRequestHeaders(s.RequestHeaders)
		}
	}
	if sel.Billing && plan.Billing != nil {
		changed = true
		updated.SetBillingMode(billingModeOrPayg(s.BillingMode))
		updated.SetSubscriptionPlan(s.SubscriptionPlan)
	}
	if !changed {
		return t, false, nil
	}
	updated.SetUpdatedAt(time.Now())
	if err := store.SaveProvider(ctx, updated); err != nil {
		return nil, false, errors.Wrap(err, "could not update provider")
	}
	return updated, true, nil
}

// billingModeOrPayg reads an empty billing mode as pay-as-you-go, the mode of
// a provider created without one.
func billingModeOrPayg(m model.BillingMode) model.BillingMode {
	if m == "" {
		return model.BillingModePayg
	}
	return m
}
