package catalog

import (
	"slices"
	"strings"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// TargetBundle is a catalog provider converted for one Xolo provider, as the
// catalog import offers it and following the catalog compares it. Its prices
// are in the catalog provider's currency.
type TargetBundle struct {
	Bundle model.ProviderBundle
	// OtherEndpoint reports that the target is not at the catalog provider's
	// address: the bundle then gives the catalog's models, prices and plan,
	// not its API nor its endpoints.
	OtherEndpoint bool
	// LeftOut counts the offerings the target cannot serve.
	LeftOut int
	// BudgetsDropped reports that per-model budgets of the plan went with
	// the offerings left out.
	BudgetsDropped bool
	// Estimated names, per bundle model RealModel, the model form fields
	// whose catalog value is an estimate.
	Estimated map[string][]string
}

// BundleFor converts the provider providerID, with the plan planID when it
// is not empty, for the provider target, nil when the import creates one.
// Imported into a provider at another endpoint than its own, a catalog
// provider gives its models, prices and plan, not its API nor its endpoints:
// they would receive this provider's key. Per offering, by the rule of the
// model form's pre-fill: one served at the provider's address keeps the API
// served there, one served elsewhere through another API is left out, the
// others give their figures. So is a model of a kind the API it ends on does
// not serve.
func (c *Catalog) BundleFor(providerID, planID string, target model.Provider) (TargetBundle, error) {
	bundle, err := c.Bundle(providerID, planID)
	if err != nil {
		return TargetBundle{}, err
	}
	p := c.Provider(providerID)
	tb := TargetBundle{Bundle: bundle}
	tb.OtherEndpoint = target != nil && strings.TrimRight(p.Xolo.BaseURL, "/") != strings.TrimRight(target.BaseURL(), "/")
	if tb.OtherEndpoint {
		tb.forEndpoint(target)
	}
	tb.Estimated = make(map[string][]string, len(tb.Bundle.Models))
	for _, bm := range tb.Bundle.Models {
		if o := p.Offering(bm.RealModel); o != nil {
			if fields := c.Estimated(c.Model(o.Model), o); len(fields) > 0 {
				tb.Estimated[bm.RealModel] = fields
			}
		}
	}
	return tb, nil
}

// forEndpoint keeps the offerings target reaches at its own address, see
// BundleFor.
func (tb *TargetBundle) forEndpoint(target model.Provider) {
	address, catalogType := strings.TrimRight(target.BaseURL(), "/"), tb.Bundle.Provider.Type
	tb.Bundle.Provider.Type, tb.Bundle.Provider.BaseURL = target.Type(), target.BaseURL()
	models := tb.Bundle.Models[:0]
	for _, bm := range tb.Bundle.Models {
		api := bm.APIType
		switch {
		case bm.BaseURL != "" && strings.TrimRight(bm.BaseURL, "/") == address:
			// The API served there: the offering's, else its catalog
			// provider's, which a bundle model leaves implicit.
			if api == "" {
				api = catalogType
			}
			bm.BaseURL = ""
		case api != "" && api != target.Type():
			tb.LeftOut++
			continue
		default:
			api, bm.BaseURL = "", ""
		}
		if api == target.Type() {
			api = ""
		}
		served := api
		if served == "" {
			served = target.Type()
		}
		if bm.Kind != "" && !model.ProviderTypeServes(served, bm.Kind) {
			tb.LeftOut++
			continue
		}
		bm.APIType = api
		models = append(models, bm)
	}
	tb.Bundle.Models = models
	// A budget of a model left out would apply to none. A shared constraint
	// keeps it: its smallest budget weighs the models it does not list,
	// which the published plan sets.
	if plan := tb.Bundle.Provider.SubscriptionPlan; plan != nil && tb.LeftOut > 0 {
		for i := range plan.Constraints {
			if plan.Constraints[i].Shared() {
				continue
			}
			for name := range plan.Constraints[i].ModelBudgets {
				if !slices.ContainsFunc(models, func(bm model.ProviderBundleModel) bool { return bm.RealModel == name }) {
					delete(plan.Constraints[i].ModelBudgets, name)
					tb.BudgetsDropped = true
				}
			}
		}
	}
}

// Estimated names the model form fields whose catalog value is an estimate,
// for the model m served as the offering o; either may be nil. The offering's
// own windows, when it narrows the model's, carry their own provenance.
func (c *Catalog) Estimated(m *Model, o *Offering) []string {
	var fields []string
	estimated := func(field string, p Provenance) {
		if p.Status == StatusEstimated {
			fields = append(fields, field)
		}
	}
	contextWindow, outputWindow := (*Count)(nil), (*Count)(nil)
	if m != nil {
		contextWindow, outputWindow = m.ContextWindow, m.OutputWindow
		if m.Params != nil && m.Params.Active != nil {
			estimated("active_params", m.Params.Active.Provenance)
		}
	}
	if o != nil {
		estimated("prompt_cost", o.Prices.Provenance)
		estimated("completion_cost", o.Prices.Provenance)
		if o.Prices.CachedInput > 0 {
			estimated("cached_prompt_cost", o.Prices.Provenance)
		}
		if o.ContextWindow != nil {
			contextWindow = o.ContextWindow
		}
		if o.OutputWindow != nil {
			outputWindow = o.OutputWindow
		}
		if o.Throughput != nil {
			estimated("tokens_per_sec_low", o.Throughput.Provenance)
			estimated("tokens_per_sec_high", o.Throughput.Provenance)
		}
	}
	if contextWindow != nil {
		estimated("context_window", contextWindow.Provenance)
	}
	if outputWindow != nil {
		estimated("output_window", outputWindow.Provenance)
	}
	return fields
}
