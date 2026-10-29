package org

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/catalog"
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
	c, err := h.catalog.Catalog(ctx)
	if err != nil {
		h.renderProviderBundleErrorStatus(w, r, h.providerBundleImportVModel(ctx, org, p), i18n.T(ctx, "org.errors.catalog_import.unavailable"), http.StatusServiceUnavailable)
		return
	}
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

	c, err := h.catalog.Catalog(ctx)
	if err != nil {
		h.renderProviderBundleErrorStatus(w, r, vmodel, i18n.T(ctx, "org.errors.catalog_import.unavailable"), http.StatusServiceUnavailable)
		return
	}
	bundle, err := c.Bundle(r.FormValue("catalog_provider"), plan)
	if err != nil {
		// A provider or plan the release lacks is the operator's to fix; a
		// release that does not convert is the catalog's, and is logged.
		if errors.Is(err, catalog.ErrNotFound) {
			h.renderProviderBundleError(w, r, vmodel, i18n.T(ctx, "org.errors.catalog_import.unknown"))
			return
		}
		slog.ErrorContext(ctx, "could not convert the catalog provider", slog.String("version", c.Version), slogx.Error(err))
		h.renderProviderBundleError(w, r, vmodel, i18n.T(ctx, "org.errors.catalog_import.invalid", c.Version))
		return
	}
	// The catalog publishes in each provider's currency: imported into a
	// provider billing in another one, its prices and budgets are converted,
	// so that they never land under a currency they are not in.
	if p != nil && bundle.Provider.Currency != p.Currency() {
		if err := h.convertBundleCurrency(ctx, &bundle, p.Currency()); err != nil {
			slog.WarnContext(ctx, "could not convert catalog prices", slogx.Error(err))
			h.renderProviderBundleError(w, r, vmodel, i18n.T(ctx, "org.errors.catalog_import.no_rate", bundle.Provider.Currency, p.Currency()))
			return
		}
	}
	// The apply step parses the bundle posted back: a release it would
	// refuse is refused here, before the operator chooses from it.
	if err := bundle.Validate(); err != nil {
		slog.ErrorContext(ctx, "the catalog provider gives an invalid bundle", slog.String("version", c.Version), slogx.Error(err))
		h.renderProviderBundleError(w, r, vmodel, i18n.T(ctx, "org.errors.catalog_import.invalid", c.Version))
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
	vmodel.Name = bundle.Provider.Name
	vmodel.Notice = i18n.T(ctx, "org.catalog_import.from", c.Version)
	templ.Handler(component.ProviderBundleImportPage(vmodel)).ServeHTTP(w, r)
}
