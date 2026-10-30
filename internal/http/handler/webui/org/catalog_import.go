package org

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/catalog"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/service"
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
	vmodel := h.catalogImportVModel(ctx, org, p)
	c, err := h.catalog.Catalog(ctx)
	if err != nil {
		h.renderCatalogUnavailable(w, r, vmodel)
		return
	}
	templ.Handler(component.CatalogImportPage(withCatalog(vmodel, c))).ServeHTTP(w, r)
}

// withCatalog lists the providers of c on the catalog page, the one the
// target provider most likely is first.
func withCatalog(vmodel component.CatalogImportVModel, c *catalog.Catalog) component.CatalogImportVModel {
	vmodel.Catalog = c
	if p := vmodel.Provider; p != nil {
		if match := c.ProviderAt(p.BaseURL()); match != nil {
			vmodel.Suggested = match.ID
		}
	}
	return vmodel
}

func (h *Handler) catalogImportVModel(ctx context.Context, org model.Organization, p model.Provider) component.CatalogImportVModel {
	return component.CatalogImportVModel{
		AppLayoutVModel: h.providerBundleImportVModel(ctx, org, p).AppLayoutVModel,
		Org:             org,
		Provider:        p,
	}
}

// renderCatalogUnavailable says the catalog cannot be read on the catalog
// page, which a reload reads again, whether the form was shown or posted.
func (h *Handler) renderCatalogUnavailable(w http.ResponseWriter, r *http.Request, vmodel component.CatalogImportVModel) {
	vmodel.Error = i18n.T(r.Context(), "org.errors.catalog_import.unavailable")
	w.WriteHeader(http.StatusServiceUnavailable)
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

	c, err := h.catalog.Catalog(ctx)
	if err != nil {
		h.renderCatalogUnavailable(w, r, h.catalogImportVModel(ctx, org, p))
		return
	}
	// A choice the import refuses is said on the catalog page, where the
	// operator chooses again.
	refuse := func(msg string) {
		page := withCatalog(h.catalogImportVModel(ctx, org, p), c)
		page.Error = msg
		w.WriteHeader(http.StatusUnprocessableEntity)
		templ.Handler(component.CatalogImportPage(page)).ServeHTTP(w, r)
	}
	tb, err := c.BundleFor(r.FormValue("catalog_provider"), plan, p)
	if err != nil {
		// A provider or plan the release lacks is the operator's to fix; a
		// release that does not convert is the catalog's, and is logged.
		if errors.Is(err, catalog.ErrNotFound) {
			refuse(i18n.T(ctx, "org.errors.catalog_import.unknown"))
			return
		}
		// The entry at fault is named, for the operator to report it.
		slog.ErrorContext(ctx, "could not convert the catalog provider", slog.String("version", c.Version), slogx.Error(err))
		refuse(i18n.T(ctx, "org.errors.catalog_import.invalid_entry", c.Version, err.Error()))
		return
	}
	// The values as the release gives them, before any conversion: the
	// models the import writes remember them, to follow the catalog.
	origin, err := json.Marshal(service.NewCatalogImport(r.FormValue("catalog_provider"), c.Version, tb))
	if err != nil {
		slog.ErrorContext(ctx, "could not encode catalog origin", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	bundle := tb.Bundle
	// The catalog publishes in each provider's currency: imported into a
	// provider billing in another one, its prices and budgets are converted,
	// so that they never land under a currency they are not in.
	if p != nil && p.Currency() == "" {
		refuse(i18n.T(ctx, "org.errors.providers.no_currency"))
		return
	}
	if p != nil && bundle.Provider.Currency != p.Currency() {
		if err := h.convertBundleCurrency(ctx, &bundle, p.Currency()); err != nil {
			slog.WarnContext(ctx, "could not convert catalog prices", slogx.Error(err))
			refuse(i18n.T(ctx, "org.errors.catalog_import.no_rate", bundle.Provider.Currency, p.Currency()))
			return
		}
	}
	// The catalog describes no cloud tier, retry nor rate limit, and no
	// billing without a plan: an existing provider keeps its own, rather
	// than a dry run offering to clear them. Set once the prices are
	// converted: its plan is already in its currency.
	if p != nil {
		bundle.Provider.CloudTier, bundle.Provider.Retry, bundle.Provider.RateLimit = p.CloudTier(), p.RetryConfig(), p.RateLimitConfig()
		if plan == "" {
			bundle.Provider.BillingMode, bundle.Provider.SubscriptionPlan = p.BillingMode(), p.SubscriptionPlan()
		}
	}
	// The apply step parses the bundle posted back and checks its plan: a
	// release it would refuse is refused here, before the operator chooses
	// from it.
	err = bundle.Validate()
	if err == nil {
		err = service.ValidateBundleSettings(bundle.Provider)
	}
	if err != nil {
		slog.ErrorContext(ctx, "the catalog provider gives an invalid bundle", slog.String("version", c.Version), slogx.Error(err))
		refuse(i18n.T(ctx, "org.errors.catalog_import.invalid_entry", c.Version, err.Error()))
		return
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
	vmodel.CatalogImport = string(origin)
	vmodel.Name = bundle.Provider.Name
	vmodel.Notice = i18n.T(ctx, "org.catalog_import.from", c.Version)
	if tb.OtherEndpoint {
		vmodel.Notice += " " + i18n.T(ctx, "org.catalog_import.other_endpoint", p.Name())
	}
	if tb.LeftOut > 0 {
		vmodel.Notice += " " + i18n.T(ctx, "org.catalog_import.unserved_left_out", p.Type(), tb.LeftOut)
	}
	if tb.BudgetsDropped {
		vmodel.Notice += " " + i18n.T(ctx, "org.catalog_import.budgets_dropped")
	}
	templ.Handler(component.ProviderBundleImportPage(vmodel)).ServeHTTP(w, r)
}
