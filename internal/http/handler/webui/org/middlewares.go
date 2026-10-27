package org

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/core/secretcleanup"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	webcommon "github.com/xolo-gateway/xolo/internal/http/handler/webui/common"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/org/component"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

func (h *Handler) getMiddlewaresPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	mws, err := h.middlewareStore.ListMiddlewares(ctx, org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not list middlewares", slog.Any("error", err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	vmodel := component.MiddlewaresPageVModel{
		Org:         org,
		Middlewares: mws,
		Success:     r.URL.Query().Get("success"),
		Error:       r.URL.Query().Get("error"),
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + orgSlug + "-middlewares",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: i18n.T(ctx, "org.nav.middlewares"), Href: ""},
			},
		},
	}

	templ.Handler(component.MiddlewaresPage(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) getNewMiddlewarePage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	vmodel := component.MiddlewareFormVModel{
		Org:     org,
		IsNew:   true,
		Enabled: true,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + orgSlug + "-middlewares",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: i18n.T(ctx, "org.nav.middlewares"), Href: "/orgs/" + orgSlug + "/admin/middlewares"},
				{Label: i18n.T(ctx, "org.nav.new"), Href: ""},
			},
		},
	}

	templ.Handler(component.MiddlewareFormPage(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) createMiddleware(w http.ResponseWriter, r *http.Request) {
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
	if name == "" {
		http.Error(w, "Name is required", http.StatusBadRequest)
		return
	}

	mw := model.NewMiddleware(org.ID(), name, description)
	if err := h.middlewareStore.CreateMiddleware(ctx, mw); err != nil {
		if webcommon.RejectLocked(w, r, err) {
			return
		}
		if errors.Is(err, port.ErrAlreadyExists) {
			http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/middlewares?error=exists", http.StatusSeeOther)
			return
		}
		slog.ErrorContext(ctx, "could not create middleware", slog.Any("error", err))
		http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/middlewares?error=create_failed", http.StatusSeeOther)
		return
	}

	// Send the user to the edit page so they can define scope and pipeline.
	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/middlewares/"+string(mw.ID())+"/edit", http.StatusSeeOther)
}

func (h *Handler) getEditMiddlewarePage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")
	middlewareID := r.PathValue("middlewareID")

	org, mw, err := h.resolveOrgAndMiddleware(ctx, orgSlug, middlewareID)
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}

	options := h.middlewareTargetOptions(ctx, org)
	selected := make(map[string]bool, len(mw.Targets()))
	for _, t := range mw.Targets() {
		selected[component.TargetKey(string(t.Kind), t.ID)] = true
	}

	vmodel := component.MiddlewareFormVModel{
		Org:          org,
		Middleware:   mw,
		IsNew:        false,
		Name:         mw.Name(),
		Description:  mw.Description(),
		Enabled:      mw.Enabled(),
		Priority:     mw.Priority(),
		AppliesToAll: mw.AppliesToAll(),
		Options:      options,
		Selected:     selected,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + orgSlug + "-middlewares",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: i18n.T(ctx, "org.nav.middlewares"), Href: "/orgs/" + orgSlug + "/admin/middlewares"},
				{Label: mw.Name(), Href: ""},
			},
		},
	}

	templ.Handler(component.MiddlewareFormPage(vmodel)).ServeHTTP(w, r)
}

// middlewareMutable is the set of setters shared by all middleware implementations.
type middlewareMutable interface {
	SetName(string)
	SetDescription(string)
	SetEnabled(bool)
	SetPriority(int)
	SetAppliesToAll(bool)
	SetTargets([]model.ModelRef)
}

func (h *Handler) updateMiddleware(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")
	middlewareID := r.PathValue("middlewareID")

	_, mw, err := h.resolveOrgAndMiddleware(ctx, orgSlug, middlewareID)
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	m, ok := mw.(middlewareMutable)
	if !ok {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	m.SetName(r.FormValue("name"))
	m.SetDescription(r.FormValue("description"))
	m.SetEnabled(r.FormValue("enabled") != "")
	m.SetAppliesToAll(r.FormValue("applies_to_all") != "")

	priority, _ := strconv.Atoi(r.FormValue("priority"))
	m.SetPriority(priority)

	// The SelectBox submits its selected values as a single comma-joined field.
	var targets []model.ModelRef
	for _, raw := range strings.Split(r.FormValue("targets"), ",") {
		kind, id, ok := splitTargetKey(strings.TrimSpace(raw))
		if !ok {
			continue
		}
		targets = append(targets, model.ModelRef{Kind: model.ModelRefKind(kind), ID: id})
	}
	m.SetTargets(targets)

	if err := h.middlewareStore.SaveMiddleware(ctx, mw); err != nil {
		if webcommon.RejectLocked(w, r, err) {
			return
		}
		slog.ErrorContext(ctx, "could not save middleware", slog.Any("error", err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/middlewares?success=updated", http.StatusSeeOther)
}

// toggleMiddleware flips the enabled flag from the list screen.
//
// It is a separate endpoint rather than a call to updateMiddleware: the list row
// only submits `enabled`, so reusing the full update would blank the name, the
// description and the targets of every middleware toggled from there.
func (h *Handler) toggleMiddleware(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")
	middlewareID := r.PathValue("middlewareID")

	_, mw, err := h.resolveOrgAndMiddleware(ctx, orgSlug, middlewareID)
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	m, ok := mw.(middlewareMutable)
	if !ok {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	// An unchecked switch submits nothing, which is what turns the middleware off.
	m.SetEnabled(r.FormValue("enabled") != "")

	if err := h.middlewareStore.SaveMiddleware(ctx, mw); err != nil {
		if webcommon.RejectLocked(w, r, err) {
			return
		}
		slog.ErrorContext(ctx, "could not save middleware", slog.Any("error", err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/middlewares?success=updated", http.StatusSeeOther)
}

func (h *Handler) deleteMiddleware(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")
	middlewareID := r.PathValue("middlewareID")

	_, mw, err := h.resolveOrgAndMiddleware(ctx, orgSlug, middlewareID)
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}

	if err := h.middlewareStore.DeleteMiddleware(ctx, model.MiddlewareID(middlewareID)); err != nil {
		if webcommon.RejectLocked(w, r, err) {
			return
		}
		if errors.Is(err, port.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		slog.ErrorContext(ctx, "could not delete middleware", slog.Any("error", err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if err := secretcleanup.PruneRemovedNodes(ctx, h.secretStore, string(mw.OrgID()), mw.Graph(), nil); err != nil {
		slog.ErrorContext(ctx, "could not prune secrets for deleted middleware", slog.Any("error", err))
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/middlewares?success=deleted", http.StatusSeeOther)
}

func (h *Handler) getMiddlewarePipelineEditorPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")
	middlewareID := r.PathValue("middlewareID")

	org, mw, err := h.resolveOrgAndMiddleware(ctx, orgSlug, middlewareID)
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}

	baseURL := httpCtx.BaseURL(ctx)
	readonly := !common.HasPermission(ctx, org.ID(), rbac.PermMiddlewaresWrite)

	vmodel := component.PipelineEditorVModel{
		OrgSlug:     org.Slug(),
		EntityID:    string(mw.ID()),
		EntityName:  mw.Name(),
		APIBase:     baseURL.String(),
		ContextType: "middleware",
		Readonly:    readonly,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + orgSlug + "-middlewares",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			FullBleed:    true,
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: i18n.T(ctx, "org.nav.middlewares"), Href: "/orgs/" + orgSlug + "/admin/middlewares"},
				{Label: mw.Name(), Href: ""},
			},
		},
	}

	templ.Handler(component.PipelineEditorPage(vmodel)).ServeHTTP(w, r)
}

// middlewareTargetOptions returns the selectable models (real + virtual) a
// middleware can target within the organization.
func (h *Handler) middlewareTargetOptions(ctx context.Context, org model.Organization) []component.MiddlewareTargetOption {
	var opts []component.MiddlewareTargetOption

	if models, err := h.providerStore.ListEnabledLLMModels(ctx, org.ID()); err == nil {
		for _, m := range models {
			// A middleware serves completion models only, for now.
			if m.Kind() != model.ModelKindCompletion {
				continue
			}
			opts = append(opts, component.MiddlewareTargetOption{
				Kind:  string(model.ModelRefKindLLM),
				ID:    string(m.ID()),
				Label: org.Slug() + "/" + m.ProxyName(),
			})
		}
	}

	if vms, err := h.virtualModelStore.ListVirtualModels(ctx, org.ID()); err == nil {
		for _, vm := range vms {
			opts = append(opts, component.MiddlewareTargetOption{
				Kind:  string(model.ModelRefKindVirtual),
				ID:    string(vm.ID()),
				Label: i18n.T(ctx, "org.flash.middlewares.virtual_target", org.Slug()+"/"+vm.Name()),
			})
		}
	}

	return opts
}

func splitTargetKey(raw string) (string, string, bool) {
	i := strings.IndexByte(raw, ':')
	if i <= 0 || i == len(raw)-1 {
		return "", "", false
	}
	return raw[:i], raw[i+1:], true
}
