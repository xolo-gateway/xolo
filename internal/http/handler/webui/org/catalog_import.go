package org

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/slogx"
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
