package org

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/a-h/templ"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/core/secretcleanup"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/org/component"
)

func (h *Handler) getVirtualModelsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	vms, err := h.virtualModelStore.ListVirtualModels(ctx, org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not list virtual models", slog.Any("error", err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	baseURL := httpCtx.BaseURL(ctx)

	vmodel := component.VirtualModelsPageVModel{
		Org:           org,
		VirtualModels: vms,
		Selected:      selectedVirtualModel(vms, r.URL.Query().Get("vm")),
		BaseURL:       baseURL.String(),
		Success:       r.URL.Query().Get("success"),
		Error:         r.URL.Query().Get("error"),
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + orgSlug + "-virtual-models",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: "Modèles virtuels", Href: ""},
			},
		},
	}

	templ.Handler(component.VirtualModelsPage(vmodel)).ServeHTTP(w, r)
}

// selectedVirtualModel resolves the model the detail pane previews. An unknown
// or absent `?vm=` falls back to the first one, so the pane is never empty while
// the list is not — a stale bookmark shows the list rather than a blank frame.
func selectedVirtualModel(vms []model.VirtualModel, id string) model.VirtualModel {
	if len(vms) == 0 {
		return nil
	}

	for _, vm := range vms {
		if string(vm.ID()) == id {
			return vm
		}
	}

	return vms[0]
}

func (h *Handler) getNewVirtualModelPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	vmodel := component.VirtualModelFormVModel{
		Org:   org,
		IsNew: true,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + orgSlug + "-virtual-models",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: "Modèles virtuels", Href: "/orgs/" + orgSlug + "/admin/virtual-models"},
				{Label: "Nouveau", Href: ""},
			},
		},
	}

	templ.Handler(component.VirtualModelFormPage(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) createVirtualModel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	description := r.FormValue("description")

	// A blank name is rejected here even though the input is marked `required`
	// in the form (the attribute is bypassable, and we want the same response
	// as the update path and the personal create path). The flash code
	// `?error=name_required` is mapped in common/flash.templ.
	if name == "" {
		http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/virtual-models?error=name_required", http.StatusSeeOther)
		return
	}

	// Uniqueness pre-check. ErrNotFound means "no collision", any other
	// error is a lookup failure we surface explicitly rather than silently
	// skipping — the unique index would still reject, but with a misleading
	// `?error=create_failed` instead of a clear diagnostic.
	existing, err := h.virtualModelStore.GetVirtualModelByName(ctx, org.ID(), name)
	switch {
	case err == nil:
		if existing != nil {
			http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/virtual-models?error=exists", http.StatusSeeOther)
			return
		}
	case errors.Is(err, port.ErrNotFound):
		// No collision, proceed.
	default:
		slog.ErrorContext(ctx, "could not check virtual model name uniqueness", slog.Any("error", err))
		http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/virtual-models?error=create_failed", http.StatusSeeOther)
		return
	}

	vm := model.NewVirtualModel(org.ID(), name, description)

	if err := h.virtualModelStore.CreateVirtualModel(ctx, vm); err != nil {
		slog.ErrorContext(ctx, "could not create virtual model", slog.Any("error", err))
		http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/virtual-models?error=create_failed", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/virtual-models?success=created", http.StatusSeeOther)
}

func (h *Handler) getEditVirtualModelPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")
	modelID := r.PathValue("modelID")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	vm, err := h.virtualModelStore.GetVirtualModelByID(ctx, model.VirtualModelID(modelID))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		slog.ErrorContext(ctx, "could not get virtual model", slog.Any("error", err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	// The store loads by ID only, so a {modelID} belonging to another org is
	// resolvable through this route. Treat the mismatch as a 404 rather than
	// surfacing a model that does not belong to the org the request came in
	// for. Mirrors the personal handler's user-ownership check.
	if vm.OrgID() != org.ID() {
		http.NotFound(w, r)
		return
	}

	vmodel := component.VirtualModelFormVModel{
		Org:          org,
		VirtualModel: vm,
		IsNew:        false,
		Name:         vm.Name(),
		Description:  vm.Description(),
		Error:        r.URL.Query().Get("error"),
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + orgSlug + "-virtual-models",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: "Modèles virtuels", Href: "/orgs/" + orgSlug + "/admin/virtual-models"},
				{Label: vm.Name(), Href: ""},
			},
		},
	}

	templ.Handler(component.VirtualModelFormPage(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) updateVirtualModel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")
	modelID := r.PathValue("modelID")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	vm, err := h.virtualModelStore.GetVirtualModelByID(ctx, model.VirtualModelID(modelID))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		slog.ErrorContext(ctx, "could not get virtual model", slog.Any("error", err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	// See getEditVirtualModelPage — reject cross-org access through this route.
	if vm.OrgID() != org.ID() {
		http.NotFound(w, r)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	description := r.FormValue("description")

	if name == "" {
		http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/virtual-models/"+modelID+"/edit?error=name_required", http.StatusSeeOther)
		return
	}

	if name != vm.Name() {
		existing, err := h.virtualModelStore.GetVirtualModelByName(ctx, org.ID(), name)
		switch {
		case err == nil:
			// The outer `name != vm.Name()` guard makes it impossible for
			// the returned row to be the current one: GetVirtualModelByName
			// matches on (org_id, name), so a hit is by definition another row.
			if existing != nil {
				http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/virtual-models/"+modelID+"/edit?error=exists", http.StatusSeeOther)
				return
			}
		case errors.Is(err, port.ErrNotFound):
			// No collision, proceed.
		default:
			// A store error other than not-found is not a name collision.
			// The handler surfaces the lookup failure here; a concurrent
			// rename that slips past the pre-check will still be caught at
			// SaveVirtualModel (mapped to ErrAlreadyExists in the store).
			slog.ErrorContext(ctx, "could not check virtual model name uniqueness", slog.Any("error", err))
			http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/virtual-models/"+modelID+"/edit?error=update_failed", http.StatusSeeOther)
			return
		}
	}

	type mutable interface {
		SetName(string)
		SetDescription(string)
		SetUpdatedAt(time.Time)
	}

	v, ok := vm.(mutable)
	if !ok {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	v.SetName(name)
	v.SetDescription(description)
	v.SetUpdatedAt(time.Now())

	if err := h.virtualModelStore.SaveVirtualModel(ctx, vm); err != nil {
		if errors.Is(err, port.ErrAlreadyExists) {
			// A concurrent rename slipped past the pre-check and the
			// idx_org_name unique index caught it. Surface the same
			// user-facing error the pre-check would have produced.
			http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/virtual-models/"+modelID+"/edit?error=exists", http.StatusSeeOther)
			return
		}
		slog.ErrorContext(ctx, "could not save virtual model", slog.Any("error", err))
		http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/virtual-models/"+modelID+"/edit?error=update_failed", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/virtual-models?success=updated", http.StatusSeeOther)
}

func (h *Handler) deleteVirtualModel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")
	modelID := r.PathValue("modelID")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	vm, err := h.virtualModelStore.GetVirtualModelByID(ctx, model.VirtualModelID(modelID))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		slog.ErrorContext(ctx, "could not get virtual model", slog.Any("error", err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	// See getEditVirtualModelPage — reject cross-org access through this route.
	if vm.OrgID() != org.ID() {
		http.NotFound(w, r)
		return
	}

	if err := h.virtualModelStore.DeleteVirtualModel(ctx, model.VirtualModelID(modelID)); err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		slog.ErrorContext(ctx, "could not delete virtual model", slog.Any("error", err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if err := secretcleanup.PruneRemovedNodes(ctx, h.secretStore, vm.Graph(), nil); err != nil {
		slog.ErrorContext(ctx, "could not prune secrets for deleted virtual model", slog.Any("error", err))
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/virtual-models?success=deleted", http.StatusSeeOther)
}

func (h *Handler) getPipelineEditorPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")
	modelID := r.PathValue("modelID")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	vm, err := h.virtualModelStore.GetVirtualModelByID(ctx, model.VirtualModelID(modelID))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		slog.ErrorContext(ctx, "could not get virtual model", slog.Any("error", err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	// See getEditVirtualModelPage — reject cross-org access through this route.
	if vm.OrgID() != org.ID() {
		http.NotFound(w, r)
		return
	}

	baseURL := httpCtx.BaseURL(ctx)
	readonly := !common.HasPermission(ctx, org.ID(), rbac.PermVirtualModelsWrite)

	vmodel := component.PipelineEditorVModel{
		OrgSlug:    org.Slug(),
		EntityID:   string(vm.ID()),
		EntityName: vm.Name(),
		APIBase:    baseURL.String(),
		Readonly:   readonly,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + orgSlug + "-virtual-models",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			FullBleed:    true,
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: "Modèles virtuels", Href: "/orgs/" + orgSlug + "/admin/virtual-models"},
				{Label: vm.Name(), Href: ""},
			},
		},
	}

	templ.Handler(component.PipelineEditorPage(vmodel)).ServeHTTP(w, r)
}
