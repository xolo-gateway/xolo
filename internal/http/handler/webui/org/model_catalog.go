package org

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	webcommon "github.com/xolo-gateway/xolo/internal/http/handler/webui/common"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/org/component"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

// withModelCatalogOrigin shows on the model form the catalog entry the model
// follows, the fields that differ from the catalog and the estimates it
// holds. A model without origin, or an origin that cannot be read, shows
// none.
func (h *Handler) withModelCatalogOrigin(ctx context.Context, vmodel *component.ModelFormVModel) {
	if h.catalogOrigins == nil || vmodel.Model == nil || vmodel.IsNew {
		return
	}
	origin, err := h.catalogOrigins.GetCatalogOrigin(ctx, vmodel.Model.ID())
	if err != nil {
		slog.WarnContext(ctx, "could not read the catalog origin of a model", slogx.Error(err))
		return
	}
	if origin == nil {
		return
	}
	vmodel.CatalogOrigin = origin
	vmodel.CatalogDivergences = service.CatalogDivergences(vmodel.Model, *origin)
	vmodel.CatalogEstimated = map[string]bool{}
	for _, field := range origin.Estimated {
		if service.CatalogEstimated(vmodel.Model, *origin, field) {
			vmodel.CatalogEstimated[field] = true
		}
	}
}

// acceptCatalogValue writes the catalog's value of one field to a model that
// follows the catalog, through the checks of a bundle import.
func (h *Handler) acceptCatalogValue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	org, p, m, origin, ok := h.resolveModelCatalogOrigin(w, r)
	if !ok {
		return
	}
	err := service.AcceptCatalogValue(ctx, h.providerStore, p, m, *origin, r.FormValue("field"))
	if err != nil {
		if webcommon.RejectLocked(w, r, err) {
			return
		}
		if errors.Is(err, port.ErrInvalid) {
			h.renderModelFormError(w, r, ctx, httpCtx.User(ctx), org.Slug(), org, p, m, false, i18n.T(ctx, "org.model.catalog.refused"))
			return
		}
		slog.ErrorContext(ctx, "could not write the catalog value of a model", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, modelEditURL(org, p, m)+"?success=catalog_accepted", http.StatusSeeOther)
}

// detachCatalogOrigin stops following the catalog for a model: it keeps its
// values, and no release changes them any more.
func (h *Handler) detachCatalogOrigin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	org, p, m, _, ok := h.resolveModelCatalogOrigin(w, r)
	if !ok {
		return
	}
	if err := h.catalogOrigins.SaveCatalogOrigin(ctx, m.ID(), nil); err != nil {
		if webcommon.RejectLocked(w, r, err) {
			return
		}
		if errors.Is(err, port.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		slog.ErrorContext(ctx, "could not forget the catalog origin of a model", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, modelEditURL(org, p, m)+"?success=catalog_detached", http.StatusSeeOther)
}

// resolveModelCatalogOrigin resolves the model of the path, checked against
// the organization and provider, and its catalog origin. A model without
// origin answers 404, like a missing one.
func (h *Handler) resolveModelCatalogOrigin(w http.ResponseWriter, r *http.Request) (model.Organization, model.Provider, model.LLMModel, *model.CatalogOrigin, bool) {
	ctx := r.Context()
	if h.catalogOrigins == nil {
		http.NotFound(w, r)
		return nil, nil, nil, nil, false
	}
	org, p, m, err := h.resolveOrgAndProviderModel(ctx, r.PathValue("orgSlug"), r.PathValue("providerID"), r.PathValue("modelID"))
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return nil, nil, nil, nil, false
	}
	origin, err := h.catalogOrigins.GetCatalogOrigin(ctx, m.ID())
	if err != nil && !errors.Is(err, port.ErrNotFound) {
		slog.ErrorContext(ctx, "could not read the catalog origin of a model", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return nil, nil, nil, nil, false
	}
	if origin == nil {
		http.NotFound(w, r)
		return nil, nil, nil, nil, false
	}
	return org, p, m, origin, true
}

func modelEditURL(org model.Organization, p model.Provider, m model.LLMModel) string {
	return "/orgs/" + org.Slug() + "/admin/providers/" + string(p.ID()) + "/models/" + string(m.ID()) + "/edit"
}
