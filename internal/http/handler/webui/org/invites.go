package org

import (
	"log/slog"
	"net/http"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	webcommon "github.com/xolo-gateway/xolo/internal/http/handler/webui/common"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/org/component"
)

func (h *Handler) getInvitesPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}

	invites, err := h.inviteStore.ListInvites(ctx, org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not list invites", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	orgRoles, err := h.roleStore.ListOrgRoles(ctx, org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not list org roles", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	roleNames := make(map[string]string, len(orgRoles))
	for _, r := range orgRoles {
		roleNames[string(r.ID())] = r.Name()
		if r.Builtin() {
			switch r.BuiltinKind() {
			case model.BuiltinKindMember:
				roleNames[model.RoleMember] = r.Name()
			case model.BuiltinKindAdmin:
				roleNames[model.RoleOrgAdmin] = r.Name()
			case model.BuiltinKindOwner:
				roleNames[model.RoleOrgOwner] = r.Name()
			}
		}
	}

	baseURL := httpCtx.BaseURL(ctx)

	vmodel := component.InvitesPageVModel{
		Org:       org,
		Invites:   invites,
		RoleNames: roleNames,
		BaseURL:   baseURL.String(),
		Success:   r.URL.Query().Get("success"),
		NewURL:    r.URL.Query().Get("new_url"),
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + orgSlug + "-invites",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: "Invitations", Href: ""},
			},
		},
	}

	templ.Handler(component.InvitesPage(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) getNewInvitePage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}

	orgRoles, err := h.roleStore.ListOrgRoles(ctx, org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not list org roles", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	vmodel := component.InviteFormVModel{
		Org:      org,
		OrgRoles: orgRoles,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + orgSlug + "-invites",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: "Invitations", Href: "/orgs/" + orgSlug + "/admin/invites"},
			},
		},
	}

	templ.Handler(component.InviteForm(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) createInvite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")
	user := httpCtx.User(ctx)

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	invite, err := h.invitationService.Create(ctx, httpCtx.TenantID(ctx), org.ID(), user.ID(), service.CreateInvitationInput{
		Role: r.FormValue("role"), Email: r.FormValue("invitee_email"),
		ExpiresAt: r.FormValue("expires_at"), MaxUses: r.FormValue("max_uses"),
	})
	if err != nil {
		if webcommon.RejectLocked(w, r, err) {
			return
		}
		webcommon.WriteInvitationError(w, r, err)
		return
	}

	baseURL := httpCtx.BaseURL(ctx)
	joinURL := baseURL.JoinPath("/join/" + string(invite.ID())).String()

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/invites?success=created&new_url="+joinURL, http.StatusSeeOther)
}

func (h *Handler) deleteInvite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")
	inviteID := r.PathValue("inviteID")

	if _, _, err := h.resolveOrgAndInvite(ctx, orgSlug, inviteID); err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}

	if err := h.inviteStore.DeleteInvite(ctx, model.InviteTokenID(inviteID)); err != nil {
		if webcommon.RejectLocked(w, r, err) {
			return
		}
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "Invite not found", http.StatusNotFound)
			return
		}
		slog.ErrorContext(ctx, "could not delete invite", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/invites?success=deleted", http.StatusSeeOther)
}

func (h *Handler) revokeInvite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")
	inviteID := r.PathValue("inviteID")

	if _, _, err := h.resolveOrgAndInvite(ctx, orgSlug, inviteID); err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}

	if err := h.inviteStore.RevokeInvite(ctx, model.InviteTokenID(inviteID)); err != nil {
		if webcommon.RejectLocked(w, r, err) {
			return
		}
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "Invite not found", http.StatusNotFound)
			return
		}
		slog.ErrorContext(ctx, "could not revoke invite", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/invites?success=revoked", http.StatusSeeOther)
}
