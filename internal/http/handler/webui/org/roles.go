package org

import (
	"context"
	webcommon "github.com/xolo-gateway/xolo/internal/http/handler/webui/common"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/org/component"
)

func (h *Handler) getRolesPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	roles, err := h.roleStore.ListOrgRoles(ctx, org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not list org roles", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	vmodel := component.RolesPageVModel{
		Org:     org,
		Roles:   roles,
		Query:   strings.TrimSpace(r.URL.Query().Get("q")),
		RoleID:  r.URL.Query().Get("role"),
		Success: r.URL.Query().Get("success"),
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + orgSlug + "-roles",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: "Rôles", Href: ""},
			},
		},
	}

	templ.Handler(component.RolesPage(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) getNewRolePage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	h.renderRoleForm(w, r, org, nil, true, "")
}

func (h *Handler) getEditRolePage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")
	roleID := r.PathValue("roleID")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	role, err := h.roleStore.GetRoleByID(ctx, model.RoleID(roleID))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "Role not found", http.StatusNotFound)
			return
		}
		slog.ErrorContext(ctx, "could not get role", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if role.OrgID() != org.ID() {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	h.renderRoleForm(w, r, org, role, false, "")
}

func (h *Handler) createRole(w http.ResponseWriter, r *http.Request) {
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

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		h.renderRoleForm(w, r, org, nil, true, "Le nom du rôle est requis.")
		return
	}

	role := model.NewRole(org.ID(), name, strings.TrimSpace(r.FormValue("description")))
	role.SetPermissions(parsePermissions(r))
	role.SetModelGrants(parseModelGrants(r))

	if err := h.roleStore.CreateRole(ctx, role); err != nil {
		if webcommon.RejectOwnership(w, r, err) {
			return
		}
		slog.ErrorContext(ctx, "could not create role", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/roles?success=created", http.StatusSeeOther)
}

func (h *Handler) updateRole(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")
	roleID := r.PathValue("roleID")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	role, err := h.roleStore.GetRoleByID(ctx, model.RoleID(roleID))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "Role not found", http.StatusNotFound)
			return
		}
		slog.ErrorContext(ctx, "could not get role", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if role.OrgID() != org.ID() {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	// The owner role bypasses all permission checks and is not editable.
	if role.BuiltinKind() == model.BuiltinKindOwner {
		http.Error(w, "Ce rôle ne peut pas être modifié", http.StatusBadRequest)
		return
	}

	opts := []model.RoleOption{
		model.WithRolePermissions(parsePermissions(r)),
		model.WithRoleModelGrants(parseModelGrants(r)),
	}
	// Builtin role names/descriptions stay fixed; only custom roles can be renamed.
	if !role.Builtin() {
		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" {
			h.renderRoleForm(w, r, org, role, false, "Le nom du rôle est requis.")
			return
		}
		opts = append(opts, model.WithRoleName(name), model.WithRoleDescription(strings.TrimSpace(r.FormValue("description"))))
	}

	updated := model.UpdateRole(role, opts...)
	if err := h.roleStore.SaveRole(ctx, updated); err != nil {
		if webcommon.RejectOwnership(w, r, err) {
			return
		}
		slog.ErrorContext(ctx, "could not save role", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/roles?success=updated", http.StatusSeeOther)
}

// toggleRolePermission grants or revokes a single permission on a role, from the
// permission matrix.
//
// It is a separate endpoint from updateRole rather than a reuse of it: the
// matrix submits one checkbox, and updateRole rebuilds a role from the whole
// form — reusing it would wipe every other permission and every model grant of
// the role on each click.
func (h *Handler) toggleRolePermission(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")
	roleID := r.PathValue("roleID")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	role, err := h.roleStore.GetRoleByID(ctx, model.RoleID(roleID))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "Role not found", http.StatusNotFound)
			return
		}
		slog.ErrorContext(ctx, "could not get role", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if role.OrgID() != org.ID() {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// The owner role bypasses all permission checks; its permission list is not
	// what makes it powerful, so editing it would be misleading.
	if role.BuiltinKind() == model.BuiltinKindOwner {
		http.Error(w, "Ce rôle ne peut pas être modifié", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	code := rbac.Permission(r.FormValue("permission"))
	if !rbac.IsKnown(string(code)) {
		http.Error(w, "Permission inconnue", http.StatusBadRequest)
		return
	}

	// An unchecked box submits nothing, which is what revokes the permission.
	granted := r.FormValue("granted") != ""

	updated := model.UpdateRole(role, model.WithRolePermissions(togglePermission(role.Permissions(), code, granted)))
	if err := h.roleStore.SaveRole(ctx, updated); err != nil {
		if webcommon.RejectOwnership(w, r, err) {
			return
		}
		slog.ErrorContext(ctx, "could not save role", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	// The matrix is a filtered view; sending the user back to an unfiltered one
	// would lose the rows they were working on.
	target := url.URL{Path: "/orgs/" + orgSlug + "/admin/roles"}
	query := url.Values{"success": {"updated"}}
	if q := strings.TrimSpace(r.FormValue("q")); q != "" {
		query.Set("q", q)
	}
	if filter := r.FormValue("role"); filter != "" {
		query.Set("role", filter)
	}
	target.RawQuery = query.Encode()

	http.Redirect(w, r, target.String(), http.StatusSeeOther)
}

// togglePermission adds or removes a code from a role's permission list,
// preserving the order of the others so a save does not reshuffle the record.
func togglePermission(current []string, code rbac.Permission, granted bool) []string {
	out := make([]string, 0, len(current)+1)
	var found bool

	for _, existing := range current {
		if existing == string(code) {
			found = true
			if !granted {
				continue
			}
		}
		out = append(out, existing)
	}

	if granted && !found {
		out = append(out, string(code))
	}

	return out
}

func (h *Handler) deleteRole(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")
	roleID := r.PathValue("roleID")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	role, err := h.roleStore.GetRoleByID(ctx, model.RoleID(roleID))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "Role not found", http.StatusNotFound)
			return
		}
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if role.OrgID() != org.ID() {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if err := h.roleStore.DeleteRole(ctx, role.ID()); err != nil {
		if webcommon.RejectOwnership(w, r, err) {
			return
		}
		if errors.Is(err, port.ErrNotAllowed) {
			http.Error(w, "Ce rôle ne peut pas être supprimé", http.StatusBadRequest)
			return
		}
		slog.ErrorContext(ctx, "could not delete role", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/roles?success=deleted", http.StatusSeeOther)
}

// renderRoleForm builds and renders the role create/edit form.
func (h *Handler) renderRoleForm(w http.ResponseWriter, r *http.Request, org model.Organization, role model.Role, isNew bool, formErr string) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := org.Slug()

	selected := map[string]bool{}
	selectedModels := map[string]bool{}
	if role != nil {
		for _, code := range role.Permissions() {
			selected[code] = true
		}
		for _, grant := range role.ModelGrants() {
			selectedModels[modelGrantKey(grant.Kind, grant.ModelID)] = true
		}
	}

	modelOptions, err := h.roleModelOptions(ctx, org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not list models for role form", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	title := "Nouveau rôle"
	crumb := "Nouveau"
	if !isNew && role != nil {
		title = role.Name()
		crumb = role.Name()
	}

	vmodel := component.RoleFormVModel{
		Org:          org,
		Role:         role,
		IsNew:        isNew,
		Error:        formErr,
		Selected:     selected,
		SelectedMode: selectedModels,
		ModelOptions: modelOptions,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + orgSlug + "-roles",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: "Rôles", Href: "/orgs/" + orgSlug + "/admin/roles"},
				{Label: crumb, Href: ""},
			},
		},
	}
	_ = title

	templ.Handler(component.RoleForm(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) roleModelOptions(ctx context.Context, orgID model.OrgID) ([]component.RoleModelOption, error) {
	var options []component.RoleModelOption

	llmModels, err := h.providerStore.ListLLMModels(ctx, orgID)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	for _, m := range llmModels {
		options = append(options, component.RoleModelOption{
			ID:    string(m.ID()),
			Kind:  rbac.ModelKindLLM,
			Label: m.ProxyName(),
		})
	}

	virtualModels, err := h.virtualModelStore.ListVirtualModels(ctx, orgID)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	for _, m := range virtualModels {
		options = append(options, component.RoleModelOption{
			ID:    string(m.ID()),
			Kind:  rbac.ModelKindVirtual,
			Label: m.Name(),
		})
	}

	return options, nil
}

// parsePermissions extracts the known permission codes from the submitted form.
func parsePermissions(r *http.Request) []string {
	var codes []string
	for _, raw := range r.Form["permissions"] {
		if rbac.IsKnown(raw) {
			codes = append(codes, raw)
		}
	}
	return codes
}

// parseModelGrants extracts the model grants ("kind:id") from the submitted form.
func parseModelGrants(r *http.Request) []model.ModelGrant {
	var grants []model.ModelGrant
	for _, raw := range r.Form["models"] {
		kind, id, ok := strings.Cut(raw, ":")
		if !ok {
			continue
		}
		if kind != rbac.ModelKindLLM && kind != rbac.ModelKindVirtual {
			continue
		}
		grants = append(grants, model.ModelGrant{ModelID: id, Kind: kind})
	}
	return grants
}

func modelGrantKey(kind, id string) string {
	return kind + ":" + id
}
