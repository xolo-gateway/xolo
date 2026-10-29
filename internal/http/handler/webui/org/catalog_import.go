package org

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/org/component"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

// getCatalogImportPage lists the providers of the open model catalog to
// import from, into the provider of the path or into a new one.
func (h *Handler) getCatalogImportPage(w http.ResponseWriter, r *http.Request) {
	org, p, ok := h.resolveProviderBundleTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if h.catalog == nil {
		http.NotFound(w, r)
		return
	}
	c := h.catalog.Catalog(ctx)
	vmodel := component.CatalogImportVModel{
		AppLayoutVModel: h.providerBundleImportVModel(ctx, org, p).AppLayoutVModel,
		Org:             org,
		Provider:        p,
		Catalog:         c,
	}
	if p != nil {
		if match := c.MatchProvider(p.BaseURL()); match != nil {
			vmodel.Suggested = match.ID
		}
	}
	templ.Handler(component.CatalogImportPage(vmodel)).ServeHTTP(w, r)
}

// previewCatalogImport converts the chosen catalog provider, with its plan,
// into a provider bundle and renders the bundle's dry run: the import then
// goes through the same selection and apply as an uploaded bundle.
func (h *Handler) previewCatalogImport(w http.ResponseWriter, r *http.Request) {
	org, p, ok := h.resolveProviderBundleTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if h.catalog == nil {
		http.NotFound(w, r)
		return
	}
	// The form names a catalog provider and a plan, nothing more.
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}
	plan := r.FormValue("plan")
	if plan == "none" {
		plan = ""
	}
	vmodel := h.providerBundleImportVModel(ctx, org, p)

	c := h.catalog.Catalog(ctx)
	bundle, err := c.Bundle(r.FormValue("catalog_provider"), plan)
	if err != nil {
		h.renderProviderBundleError(w, r, vmodel, i18n.T(ctx, "org.errors.catalog_import.unknown"))
		return
	}
	// The catalog publishes in each provider's currency: imported into a
	// provider billing in another one, its prices and budgets are converted,
	// so that they never land under a currency they are not in.
	if p != nil && p.Currency() != "" && bundle.Provider.Currency != p.Currency() {
		if err := h.convertBundleCurrency(ctx, &bundle, p.Currency()); err != nil {
			slog.WarnContext(ctx, "could not convert catalog prices", slogx.Error(err))
			h.renderProviderBundleError(w, r, vmodel, i18n.T(ctx, "org.errors.catalog_import.no_rate", bundle.Provider.Currency, p.Currency()))
			return
		}
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		slog.ErrorContext(ctx, "could not encode catalog bundle", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	dryRun, err := h.planProviderBundleImport(ctx, org, p, bundle, nil)
	if err != nil {
		slog.ErrorContext(ctx, "could not plan catalog import", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	vmodel.Plan = &dryRun
	vmodel.Bundle = string(raw)
	vmodel.Name = bundle.Provider.Name
	vmodel.Notice = i18n.T(ctx, "org.catalog_import.from", c.Version)
	templ.Handler(component.ProviderBundleImportPage(vmodel)).ServeHTTP(w, r)
}

// convertBundleCurrency converts the prices and the plan's value budgets of
// bundle into the currency to, which becomes the bundle's.
func (h *Handler) convertBundleCurrency(ctx context.Context, bundle *model.ProviderBundle, to string) error {
	if h.exchangeRateService == nil {
		return errors.New("no exchange rate service")
	}
	from := bundle.Provider.Currency
	convert := func(microcents int64) (int64, error) {
		return h.exchangeRateService.Convert(ctx, microcents, from, to)
	}
	for i := range bundle.Models {
		prices := &bundle.Models[i].Prices
		for _, p := range []*float64{&prices.Input, &prices.CachedInput, &prices.Output} {
			v, err := convert(model.MicrocentsPer1K(*p))
			if err != nil {
				return errors.WithStack(err)
			}
			*p = model.PricePerMillion(v)
		}
	}
	if plan := bundle.Provider.SubscriptionPlan; plan != nil {
		for ci := range plan.Constraints {
			c := &plan.Constraints[ci]
			if c.ValueBudget != nil {
				v, err := convert(*c.ValueBudget)
				if err != nil {
					return errors.WithStack(err)
				}
				c.ValueBudget = &v
			}
			for id, b := range c.ModelBudgets {
				if b.ValueBudget == nil {
					continue
				}
				v, err := convert(*b.ValueBudget)
				if err != nil {
					return errors.WithStack(err)
				}
				b.ValueBudget = &v
				c.ModelBudgets[id] = b
			}
		}
	}
	bundle.Provider.Currency = to
	return nil
}
