package org

import (
	"context"
	webcommon "github.com/xolo-gateway/xolo/internal/http/handler/webui/common"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/org/component"
)

func (h *Handler) getMembersPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	const membersPageSize = 20

	page := 0
	if p := r.URL.Query().Get("page"); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 1 {
			page = n - 1
		}
	}

	opts := port.ListOrgMembersOptions{
		Page:  &page,
		Limit: func() *int { l := membersPageSize; return &l }(),
	}

	members, total, err := h.orgStore.ListOrgMembers(ctx, org.ID(), opts)
	if err != nil {
		slog.ErrorContext(ctx, "could not list members", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	vmodel := component.MembersPageVModel{
		Org:          org,
		Members:      members,
		Success:      r.URL.Query().Get("success"),
		CurrentPage:  page + 1,
		PageSize:     membersPageSize,
		TotalMembers: int(total),
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + orgSlug + "-members",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: "Membres", Href: ""},
			},
		},
	}

	templ.Handler(component.MembersPage(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) deleteMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")
	membershipID := r.PathValue("membershipID")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	membership, err := h.orgStore.GetMembership(ctx, model.MembershipID(membershipID))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "Membership not found", http.StatusNotFound)
			return
		}
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if membership.OrgID() != org.ID() {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if err := h.orgStore.RemoveMember(ctx, model.MembershipID(membershipID)); err != nil {
		if webcommon.RejectOwnership(w, r, err) {
			return
		}
		slog.ErrorContext(ctx, "could not remove member", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/members?success=removed", http.StatusSeeOther)
}

func (h *Handler) getEditMemberPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")
	membershipID := r.PathValue("membershipID")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	membership, err := h.orgStore.GetMembership(ctx, model.MembershipID(membershipID))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "Membership not found", http.StatusNotFound)
			return
		}
		slog.ErrorContext(ctx, "could not get membership", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if membership.OrgID() != org.ID() {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	orgRoles, err := h.roleStore.ListOrgRoles(ctx, org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not list org roles", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	assignedRoleIDs := map[model.RoleID]bool{}
	for _, role := range membership.Roles() {
		assignedRoleIDs[role.ID()] = true
	}

	vmodel := component.EditMemberPageVModel{
		Membership:      membership,
		Org:             org,
		OrgRoles:        orgRoles,
		AssignedRoleIDs: assignedRoleIDs,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + orgSlug + "-members",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: "Membres", Href: "/orgs/" + orgSlug + "/admin/members"},
				{Label: membership.User().Email(), Href: ""},
			},
		},
	}

	templ.Handler(component.EditMemberPage(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) postEditMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")
	membershipID := r.PathValue("membershipID")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	membership, err := h.orgStore.GetMembership(ctx, model.MembershipID(membershipID))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "Membership not found", http.StatusNotFound)
			return
		}
		slog.ErrorContext(ctx, "could not get membership", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if membership.OrgID() != org.ID() {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	orgRoles, err := h.roleStore.ListOrgRoles(ctx, org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not list org roles", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	// Keep only role IDs that belong to this organization.
	validRoleIDs := map[model.RoleID]model.Role{}
	for _, role := range orgRoles {
		validRoleIDs[role.ID()] = role
	}

	var selected []model.RoleID
	selectsOwner := false
	for _, raw := range r.Form["roles"] {
		roleID := model.RoleID(raw)
		role, ok := validRoleIDs[roleID]
		if !ok {
			continue
		}
		selected = append(selected, roleID)
		if role.BuiltinKind() == model.BuiltinKindOwner {
			selectsOwner = true
		}
	}

	// Guard: never leave the organization without an owner.
	wasOwner := false
	for _, role := range membership.Roles() {
		if role.BuiltinKind() == model.BuiltinKindOwner {
			wasOwner = true
			break
		}
	}
	if wasOwner && !selectsOwner {
		lastOwner, err := h.isLastOwner(ctx, org.ID(), membership.ID())
		if err != nil {
			slog.ErrorContext(ctx, "could not check owners", slogx.Error(err))
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		if lastOwner {
			http.Error(w, "Impossible de retirer le dernier propriétaire de l'organisation", http.StatusBadRequest)
			return
		}
	}

	if err := h.roleStore.SetMembershipRoles(ctx, model.MembershipID(membershipID), selected); err != nil {
		if webcommon.RejectOwnership(w, r, err) {
			return
		}
		slog.ErrorContext(ctx, "could not update membership roles", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/members?success=updated", http.StatusSeeOther)
}

// isLastOwner reports whether the given membership is the only one holding a
// builtin owner role within the organization.
func (h *Handler) isLastOwner(ctx context.Context, orgID model.OrgID, exclude model.MembershipID) (bool, error) {
	members, _, err := h.orgStore.ListOrgMembers(ctx, orgID, port.ListOrgMembersOptions{})
	if err != nil {
		return false, errors.WithStack(err)
	}
	return service.IsLastOwner(members, exclude), nil
}
