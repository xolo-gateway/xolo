package service

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
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
	// KindNotServed, on a kind or API change, is the provider type whose
	// API would not serve the model's kind, UnservedKind: the model form
	// refuses that pair.
	KindNotServed string
	UnservedKind  model.ModelKind
	// Unserved, on a provider type change, lists the proxy names of the
	// target's models the new API would not serve: models the bundle does
	// not contain, or keeps on the provider's API with the same kind.
	Unserved []string
	// DroppedOverrides, on the billing change, lists the adjustments of the
	// target's extra keys the imported plan no longer has a window for, or
	// a budget to adjust: they are removed with the plan they applied to.
	DroppedOverrides []model.KeyOverrideRef
}

// Refused reports whether the change cannot be applied.
func (c BundleFieldChange) Refused() bool {
	return c.Conflict != "" || c.KindNotServed != "" || len(c.Unserved) > 0
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
	// the model's kind, UnservedKind: the model is skipped, as the model form
	// refuses it.
	KindNotServed string
	UnservedKind  model.ModelKind
	// RefusedField, in a result's Skipped, names the one change of an
	// updated model that could not be applied, its other changes written.
	RefusedField string
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
	return PlanProviderBundleImportWith(bundle, target, targetModels, names, nil)
}

// PlanProviderBundleImportWith plans the import knowing the selection it is
// about to apply: when it changes the type of the target, the models without
// an API of their own are checked against the type they will be served by.
func PlanProviderBundleImportWith(bundle model.ProviderBundle, target model.Provider, targetModels []model.LLMModel, names ProxyNames, sel *BundleSelection) ProviderBundlePlan {
	plan := ProviderBundlePlan{Bundle: bundle, Target: target}
	plan.Settings, plan.Billing = planProviderSettings(bundle.Provider, target)
	if target != nil && plan.Billing != nil {
		_, plan.Billing.DroppedOverrides = model.FitKeyOverrides(target.ExtraAPIKeys(), bundle.Provider.SubscriptionPlan)
	}

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

	// Without a selection, the dry run assumes every change applies; at
	// apply time, only the selected ones count.
	selected := func(i int, field string) bool { return sel == nil || sel.Fields[i][field] }

	// A type change is refused while it would leave a model of the target
	// on an API that does not serve its kind, as the model form refuses.
	typeRefused := false
	if target != nil {
		for i := range plan.Settings {
			if plan.Settings[i].Field != BundleFieldType {
				continue
			}
			plan.Settings[i].Unserved = unservedByType(bundle, existing, own, matched, bundle.Provider.Type, selected)
			typeRefused = plan.Settings[i].Refused()
		}
	}

	// The API a model without one of its own is served by: the target's,
	// or the bundle's when the import creates the provider or changes its
	// type.
	servedType := bundle.Provider.Type
	if target != nil && (sel == nil || !sel.Settings[BundleFieldType] || typeRefused) {
		servedType = target.Type()
	}

	renameConflicts := resolveRenames(bundle, existing, selected, names)

	// The models to update are planned first, so that a name one of them
	// leaves is free for a model to create.
	plans := make([]BundleModelPlan, len(bundle.Models))
	for _, updates := range []bool{true, false} {
		for i, bm := range bundle.Models {
			if (existing[i] != nil) != updates {
				continue
			}
			conflict, decided := renameConflicts[i]
			plans[i] = planBundleModel(i, bm, existing[i], servedType, decided, conflict, selected(i, BundleFieldAPI), selected(i, BundleFieldKind), names)
		}
	}
	plan.Models = plans
	return plan
}

// unservedByType returns the proxy names of the target's models that would
// be left on the API of providerType, which does not serve their kind: the
// models of own that no bundle model updates, and those the selected changes
// of their bundle entry leave on the provider's API with such a kind.
func unservedByType(bundle model.ProviderBundle, existing, own []model.LLMModel, matched map[model.LLMModelID]bool, providerType string, selected func(int, string) bool) []string {
	var names []string
	for _, m := range own {
		if !matched[m.ID()] && m.APIType() == "" && !model.ProviderTypeServes(providerType, m.Kind()) {
			names = append(names, m.ProxyName())
		}
	}
	for i, m := range existing {
		if m == nil {
			continue
		}
		apiSel, kindSel := selected(i, BundleFieldAPI), selected(i, BundleFieldKind)
		bm := bundle.Models[i]
		api, kind, _, _ := settleAPIAndKind(bm, bundleModelKind(bm, m, providerType, apiSel), m, providerType, apiSel, kindSel)
		if api == "" && !model.ProviderTypeServes(providerType, kind) {
			names = append(names, m.ProxyName())
		}
	}
	slices.Sort(names)
	return names
}

// bundleModelKind is the kind a bundle model sets: its own, or the default
// kind of the API serving it when it sets none. existing is nil for a model
// to create.
func bundleModelKind(bm model.ProviderBundleModel, existing model.LLMModel, servedType string, apiSel bool) model.ModelKind {
	if bm.Kind != "" {
		return bm.Kind
	}
	served := bm.APIType
	if existing != nil && !apiSel {
		served = existing.APIType()
	}
	if served == "" {
		served = servedType
	}
	return model.DefaultProviderTypeKind(served)
}

// settleAPIAndKind returns the API (empty for the provider's) and the kind
// an existing model ends with once the selected changes of its bundle entry
// are written, and which of these changes are refused because they would
// leave it on an API that does not serve its kind, as the model form
// refuses: the API change, else the kind change, else both.
func settleAPIAndKind(bm model.ProviderBundleModel, bundleKind model.ModelKind, existing model.LLMModel, servedType string, apiSel, kindSel bool) (api string, kind model.ModelKind, apiRefused, kindRefused bool) {
	apiChange := apiSel && bm.APIType != existing.APIType()
	kindChange := kindSel && bundleKind != existing.Kind()
	serves := func(withAPI, withKind bool) bool {
		a, k := existing.APIType(), existing.Kind()
		if withAPI {
			a = bm.APIType
		}
		if withKind {
			k = bundleKind
		}
		if a == "" {
			a = servedType
		}
		return model.ProviderTypeServes(a, k)
	}
	switch {
	case serves(apiChange, kindChange):
	case serves(false, kindChange):
		apiRefused = apiChange
	case serves(apiChange, false):
		kindRefused = kindChange
	default:
		apiRefused, kindRefused = apiChange, kindChange
	}
	api, kind = existing.APIType(), existing.Kind()
	if apiChange && !apiRefused {
		api = bm.APIType
	}
	if kindChange && !kindRefused {
		kind = bundleKind
	}
	return api, kind, apiRefused, kindRefused
}

// orProviderType is the type whose API serves a model with api: its own, or
// the provider's when it has none.
func orProviderType(api, providerType string) string {
	if api == "" {
		return providerType
	}
	return api
}

// resolveRenames settles, before any model is planned, the final name of
// every selected rename, so that the dry run never offers a name that stays
// taken. The names the renames leave are freed first, whatever the order of
// the bundle; a rename whose new name is still held (outside the bundle, by
// a model that keeps its name, or by an earlier rename to the same name) is
// refused and its model takes its own name back, which may refuse others in
// turn, until nothing moves. A cycle of renames (a swap) cannot be written
// without a temporary name: its last rename is refused too, which refuses
// the rest of the cycle. It returns, by bundle index, the conflict of each
// selected rename, empty when accepted.
func resolveRenames(bundle model.ProviderBundle, existing []model.LLMModel, selected func(int, string) bool, names ProxyNames) map[int]string {
	var renames []int
	for i, bm := range bundle.Models {
		if m := existing[i]; m != nil && m.ProxyName() != bm.ProxyName && selected(i, BundleFieldProxyName) {
			renames = append(renames, i)
			names.Release(m.ProxyName())
		}
	}
	target := func(i int) string { return bundle.Models[i].ProxyName }
	old := func(i int) string { return existing[i].ProxyName() }

	refused := map[int]bool{}
	var claimed []string
	// settle claims the new names in bundle order; a refusal gives the
	// model its own name back and starts a new round, as it may refuse
	// renames claimed earlier. Refusals only grow, so the rounds end.
	settle := func() {
		for {
			for _, name := range claimed {
				names.Release(name)
			}
			claimed = claimed[:0]
			for i := range refused {
				names.Declare(old(i))
			}
			again := false
			for _, i := range renames {
				if refused[i] {
					continue
				}
				if names.Conflict(target(i)) != "" {
					refused[i] = true
					names.Declare(old(i))
					again = true
					continue
				}
				names.Declare(target(i))
				claimed = append(claimed, target(i))
			}
			if !again {
				return
			}
		}
	}

	for {
		settle()
		// A cycle among the accepted renames: each takes the name the next
		// one leaves. Refusing its last rename refuses the others in turn.
		leaving := map[string]int{}
		for _, i := range renames {
			if !refused[i] {
				leaving[old(i)] = i
			}
		}
		cycle := -1
		for _, start := range renames {
			if refused[start] {
				continue
			}
			// Follow the chain of names from start; a rename met twice
			// closes a cycle, made of the renames from its first visit on.
			var path []int
			at := map[int]int{}
			for i, ok := start, true; ok; i, ok = leaving[target(i)] {
				if first, seen := at[i]; seen {
					cycle = slices.Max(path[first:])
					break
				}
				at[i] = len(path)
				path = append(path, i)
			}
			if cycle >= 0 {
				break
			}
		}
		if cycle < 0 {
			break
		}
		refused[cycle] = true
	}

	conflicts := make(map[int]string, len(renames))
	for _, i := range renames {
		if !refused[i] {
			conflicts[i] = ""
			continue
		}
		// The name it would take stays held, by a model of the bundle or
		// outside it: that holder is the reason. A name freed since the
		// refusal (its holder refused in turn) is taken after all.
		if conflict := names.Conflict(target(i)); conflict != "" {
			conflicts[i] = conflict
			continue
		}
		names.Release(old(i))
		names.Declare(target(i))
		conflicts[i] = ""
	}
	return conflicts
}

// planBundleModel plans the import of the bundle model bm, at index i, into
// the target model existing (nil to create it).
func planBundleModel(i int, bm model.ProviderBundleModel, existing model.LLMModel, servedType string, renameDecided bool, renameConflict string, apiSel, kindSel bool, names ProxyNames) BundleModelPlan {
	mp := BundleModelPlan{Index: i, Model: bm, Existing: existing}
	mp.Model.Kind = bundleModelKind(bm, existing, servedType, apiSel)

	// The kind must be one the API serving the model serves, as the model
	// form requires: the bundle's API, or the provider's when it sets none.
	if existing == nil {
		served := bm.APIType
		if served == "" {
			served = servedType
		}
		if !model.ProviderTypeServes(served, mp.Model.Kind) {
			mp.Action, mp.KindNotServed, mp.UnservedKind = BundleModelSkip, served, mp.Model.Kind
		}
	}
	if mp.Action == BundleModelSkip {
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

	// An existing model takes only the API and kind changes that leave it
	// served.
	api, kind, apiRefused, kindRefused := settleAPIAndKind(bm, mp.Model.Kind, existing, servedType, apiSel, kindSel)
	current := model.BundleModelOf(existing)
	for _, f := range bundleModelFields {
		from, to := f.get(current), f.get(mp.Model)
		if reflect.DeepEqual(from, to) {
			continue
		}
		change := BundleFieldChange{Field: f.name, From: displayValue(from), To: displayValue(to)}
		// The API that would not serve, and the kind it would not serve.
		if f.name == BundleFieldKind && kindRefused {
			change.KindNotServed, change.UnservedKind = orProviderType(api, servedType), mp.Model.Kind
		}
		if f.name == BundleFieldAPI && apiRefused {
			change.KindNotServed, change.UnservedKind = orProviderType(bm.APIType, servedType), kind
		}
		if f.name == BundleFieldProxyName {
			if renameDecided {
				// Settled with every other rename by resolveRenames.
				change.Conflict = renameConflict
			} else {
				// A rename the operator did not select takes no name: it is
				// only shown, with what would stop it.
				change.Conflict = names.Conflict(bm.ProxyName)
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
	imported := s.RequestHeaders
	if target != nil {
		current = model.NewProviderBundle(target, nil, false, time.Time{}).Provider
		// The headers the provider ends with: the bundle's, with those of
		// its own a bundle never erases.
		imported = model.ImportedHeaders(target.RequestHeaders(), s.RequestHeaders)
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
		{BundleFieldRequestHeaders, jsonValue(current.RequestHeaders), jsonValue(imported)},
	}
	var changes []BundleFieldChange
	for _, f := range fields {
		if target != nil && (f.name == BundleFieldCurrency || reflect.DeepEqual(f.from, f.to)) {
			// The target keeps its currency: its other models are priced
			// in it. A bundle priced in another one is converted first.
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
	// Skipped lists the selected models the plan applied could not write:
	// the selection changed what they are served by or the names they can
	// take since the dry run the operator chose from. A model updated with
	// refused changes appears once per refused field, RefusedField naming
	// it.
	Skipped []BundleModelPlan
	// SkippedSettings lists the selected provider settings the plan applied
	// refused, such as a type change that would leave a model unserved.
	SkippedSettings []BundleFieldChange
	// SettingsFailed is the store's refusal of the provider settings of an
	// existing target, written after its models.
	SettingsFailed error
	// TypeUnchecked is the failure to read the models back, which leaves
	// the selected type change unapplied: it could not be checked.
	TypeUnchecked error
	// Stranded lists the models written for a provider type the provider
	// did not take, which its own type does not serve.
	Stranded []string
	// written holds the models the import wrote.
	written []model.LLMModel
}

// Refused reports whether a selected change could not be written.
func (r BundleApplyResult) Refused() bool {
	return len(r.Failed) > 0 || len(r.Skipped) > 0 || len(r.SkippedSettings) > 0 || r.SettingsFailed != nil || r.TypeUnchecked != nil || len(r.Stranded) > 0
}

// Wrote reports whether the import wrote anything.
func (r BundleApplyResult) Wrote() bool {
	return r.ProviderChanged || r.Created > 0 || r.Updated > 0
}

// BundleModelFailure is a model of the bundle the store refused to write.
type BundleModelFailure struct {
	Index     int
	ProxyName string
	// ModelID is the model an update failed to write, empty for a create.
	ModelID model.LLMModelID
	Err     error
}

// ApplyProviderBundle writes the selected part of plan through store. When
// plan has no target, newProvider describes the provider to create. Every
// write goes through the store, so that the projections, the audit and the
// event feed see it. The writes are not atomic: an import interrupted half
// way is completed by importing the bundle again, which only applies what
// still differs.
func ApplyProviderBundle(ctx context.Context, store port.ProviderStore, plan ProviderBundlePlan, sel BundleSelection, newProvider *NewProviderTarget) (BundleApplyResult, error) {
	var result BundleApplyResult
	if plan.Target != nil && plan.Bundle.Provider.Currency != plan.Target.Currency() {
		// The target keeps its currency, its other models being priced in
		// it: a merge writes prices in that currency only.
		return result, errors.Wrapf(port.ErrInvalid, "bundle priced in %q, provider in %q: convert it first", plan.Bundle.Provider.Currency, plan.Target.Currency())
	}

	target, updated, err := applyProviderSettings(ctx, store, plan, sel, newProvider)
	if err != nil {
		return result, err
	}
	result.Provider, result.ProviderChanged = target, plan.Target == nil
	for _, change := range plan.Settings {
		if change.Refused() && sel.Settings[change.Field] {
			result.SkippedSettings = append(result.SkippedSettings, change)
		}
	}

	for _, mp := range plan.Models {
		if mp.Action == BundleModelSkip && (sel.Models[mp.Index] || len(sel.Fields[mp.Index]) > 0) {
			result.Skipped = append(result.Skipped, mp)
			continue
		}
		// A selected change the apply plan finds in conflict, such as a
		// rename onto a name another model still holds, is reported too.
		for _, change := range mp.Changes {
			if change.Refused() && sel.Fields[mp.Index][change.Field] {
				skipped := mp
				skipped.Conflict, skipped.RefusedField = change.Conflict, change.Field
				skipped.KindNotServed, skipped.UnservedKind = change.KindNotServed, change.UnservedKind
				result.Skipped = append(result.Skipped, skipped)
			}
		}
	}

	// Updates go first, as the plan orders them: a model to create may take
	// the name a renamed model leaves. Among the updates, a rename onto the
	// name another updated model still holds waits for that one: names
	// move along chains. A cycle (a swap) cannot be written without a
	// temporary name; its renames fail and are reported.
	pending := make([]BundleModelPlan, 0, len(plan.Models))
	for _, mp := range plan.Models {
		if mp.Action == BundleModelUpdate {
			pending = append(pending, mp)
		}
	}
	for len(pending) > 0 {
		var blocked []BundleModelPlan
		for _, mp := range pending {
			name := bundleUpdateName(mp, sel.Fields[mp.Index])
			if name != mp.Existing.ProxyName() && slices.ContainsFunc(pending, func(o BundleModelPlan) bool {
				return o.Index != mp.Index && o.Existing.ProxyName() == name
			}) {
				blocked = append(blocked, mp)
				continue
			}
			applyBundleUpdate(ctx, store, mp, sel.Fields[mp.Index], &result)
		}
		if len(blocked) == len(pending) {
			// No rename can go first: write them in order, the store
			// refuses what it must.
			for _, mp := range blocked {
				applyBundleUpdate(ctx, store, mp, sel.Fields[mp.Index], &result)
			}
			break
		}
		pending = blocked
	}

	for _, mp := range plan.Models {
		if mp.Action != BundleModelCreate || !sel.Models[mp.Index] {
			continue
		}
		m := model.NewLLMModelFromBundle(model.NewLLMModelID(), target.ID(), target.OrgID(), time.Now(), mp.Model)
		if err := store.CreateLLMModel(ctx, m); err != nil {
			result.Failed = append(result.Failed, BundleModelFailure{Index: mp.Index, ProxyName: m.ProxyName(), Err: errors.WithStack(err)})
			continue
		}
		result.Created++
		result.written = append(result.written, m)
	}

	if updated != nil {
		if err := saveProviderSettings(ctx, store, plan, sel, updated, &result); err != nil {
			return result, err
		}
	}
	return result, nil
}

// saveProviderSettings writes the settings of an existing target once its
// models are: a type change applies only if the new API serves every model
// of the provider as written, since a model write the type change depended
// on may have failed. A failed write is reported in result, next to the
// models already written.
func saveProviderSettings(ctx context.Context, store port.ProviderStore, plan ProviderBundlePlan, sel BundleSelection, updated *model.BaseProvider, result *BundleApplyResult) error {
	t := plan.Target
	typeChange := updated.Type() != t.Type()
	if typeChange {
		all, err := store.ListLLMModels(ctx, t.OrgID())
		if err != nil {
			// The type change cannot be checked against the models as
			// written: it waits for a new import, the other settings do
			// not.
			result.TypeUnchecked = errors.Wrap(err, "could not list provider models")
			updated.SetType(t.Type())
		}
		if unserved := unservedModels(all, t.ID(), updated.Type()); err == nil && len(unserved) > 0 {
			result.SkippedSettings = append(result.SkippedSettings, BundleFieldChange{
				Field: BundleFieldType, From: t.Type(), To: updated.Type(), Unserved: unserved,
			})
			updated.SetType(t.Type())
		}
	}
	others := sel.Billing && plan.Billing != nil
	for _, change := range plan.Settings {
		others = others || (change.Field != BundleFieldType && sel.Settings[change.Field] && !change.Refused())
	}
	if updated.Type() != t.Type() || others {
		updated.SetUpdatedAt(time.Now())
		if err := store.SaveProvider(ctx, updated); err != nil {
			// Reported next to the models written, a lock included: the
			// report says which one.
			result.SettingsFailed = errors.Wrap(err, "could not update provider")
		} else {
			result.Provider, result.ProviderChanged = updated, true
		}
	}
	// The models this import wrote for a type the provider did not take
	// are named: a new import completes them once it can be written.
	if typeChange && result.Provider.Type() == t.Type() {
		result.Stranded = unservedModels(result.written, t.ID(), t.Type())
	}
	return nil
}

// unservedModels returns the sorted proxy names of the models of provider
// on its own API that providerType does not serve.
func unservedModels(models []model.LLMModel, provider model.ProviderID, providerType string) []string {
	var names []string
	for _, m := range models {
		if m.ProviderID() == provider && m.APIType() == "" && !model.ProviderTypeServes(providerType, m.Kind()) {
			names = append(names, m.ProxyName())
		}
	}
	slices.Sort(names)
	return names
}

// applyProviderSettings creates the provider a plan without target imports
// into. For an existing target, it returns the provider with the selected
// settings, validated but not written yet, or nil when none changes:
// saveProviderSettings writes it after the models.
func applyProviderSettings(ctx context.Context, store port.ProviderStore, plan ProviderBundlePlan, sel BundleSelection, newProvider *NewProviderTarget) (model.Provider, *model.BaseProvider, error) {
	s := plan.Bundle.Provider
	if plan.Target == nil {
		if newProvider == nil {
			return nil, nil, errors.New("no provider to import into")
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
		if err := validateBundleProvider(p); err != nil {
			return nil, nil, err
		}
		if err := store.CreateProvider(ctx, p); err != nil {
			return nil, nil, errors.Wrap(err, "could not create provider")
		}
		return p, nil, nil
	}

	t := plan.Target
	updated := model.CopyProvider(t)
	changed := false
	for _, change := range plan.Settings {
		if !sel.Settings[change.Field] || change.Refused() {
			continue
		}
		changed = true
		switch change.Field {
		case BundleFieldType:
			updated.SetType(s.Type)
		case BundleFieldBaseURL:
			updated.SetBaseURL(s.BaseURL)
		case BundleFieldCloudTier:
			updated.SetCloudTier(s.CloudTier)
		case BundleFieldRetry:
			updated.SetRetryConfig(s.Retry)
		case BundleFieldRateLimit:
			updated.SetRateLimitConfig(s.RateLimit)
		case BundleFieldRequestHeaders:
			updated.SetRequestHeaders(model.MergeImportedHeaders(t.RequestHeaders(), s.RequestHeaders))
		}
	}
	if sel.Billing && plan.Billing != nil {
		changed = true
		updated.SetBillingMode(billingModeOrPayg(s.BillingMode))
		updated.SetSubscriptionPlan(s.SubscriptionPlan)
		// The keys' adjustments follow their window by label: those the new
		// plan has no window for go with the old plan.
		keys, _ := model.FitKeyOverrides(updated.ExtraAPIKeys(), s.SubscriptionPlan)
		updated.SetExtraAPIKeys(keys)
	}
	if !changed {
		return t, nil, nil
	}
	if err := validateBundleProvider(updated); err != nil {
		return nil, nil, err
	}
	return t, updated, nil
}

// ValidateBundleSettings checks the provider settings of a bundle with the
// rules of every provider write path (validateProvider), its name aside: the
// importer names a new provider. A dry run checked with it offers nothing
// the apply step refuses.
func ValidateBundleSettings(s model.ProviderBundleSettings) error {
	err := validateProvider(model.ProviderSettings{
		Name:             "bundle",
		Type:             s.Type,
		BaseURL:          s.BaseURL,
		Active:           true,
		Currency:         s.Currency,
		CloudTier:        s.CloudTier,
		BillingMode:      billingModeOrPayg(s.BillingMode),
		SubscriptionPlan: s.SubscriptionPlan,
		RetryConfig:      s.Retry,
		RateLimitConfig:  s.RateLimit,
		RequestHeaders:   s.RequestHeaders,
	})
	return errors.Wrap(err, "invalid provider settings")
}

// billingModeOrPayg reads an empty billing mode as pay-as-you-go, the mode of
// a provider created without one.
func billingModeOrPayg(m model.BillingMode) model.BillingMode {
	if m == "" {
		return model.BillingModePayg
	}
	return m
}

// validateBundleProvider checks the provider an import is about to write
// with the rules of every other provider write path (validateProvider): the
// store validates nothing of its own.
func validateBundleProvider(p model.Provider) error {
	err := validateProvider(model.ProviderSettings{
		Name:             p.Name(),
		Type:             p.Type(),
		BaseURL:          p.BaseURL(),
		Active:           p.Active(),
		Currency:         p.Currency(),
		CloudTier:        p.CloudTier(),
		BillingMode:      p.BillingMode(),
		SubscriptionPlan: p.SubscriptionPlan(),
		RetryConfig:      p.RetryConfig(),
		RateLimitConfig:  p.RateLimitConfig(),
		RequestHeaders:   p.RequestHeaders(),
	})
	return errors.Wrap(err, "invalid provider settings")
}

// bundleUpdateName is the proxy name an updated model ends up with.
func bundleUpdateName(mp BundleModelPlan, fields map[string]bool) string {
	for _, change := range mp.Changes {
		if change.Field == BundleFieldProxyName && fields[change.Field] && !change.Refused() {
			return mp.Model.ProxyName
		}
	}
	return mp.Existing.ProxyName()
}

// applyBundleUpdate writes the selected, applicable changes of one model.
func applyBundleUpdate(ctx context.Context, store port.ProviderStore, mp BundleModelPlan, fields map[string]bool, result *BundleApplyResult) {
	updated := model.BundleModelOf(mp.Existing)
	changed := false
	for _, change := range mp.Changes {
		if !fields[change.Field] || change.Refused() {
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
		return
	}
	m := model.NewLLMModelFromBundle(mp.Existing.ID(), mp.Existing.ProviderID(), mp.Existing.OrgID(), mp.Existing.CreatedAt(), updated)
	if err := store.SaveLLMModel(ctx, m); err != nil {
		result.Failed = append(result.Failed, BundleModelFailure{Index: mp.Index, ProxyName: m.ProxyName(), ModelID: m.ID(), Err: errors.WithStack(err)})
		return
	}
	result.Updated++
	result.written = append(result.written, m)
}
