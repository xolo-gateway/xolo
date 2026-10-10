package org

import (
	"log/slog"
	"net/http"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
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
	scope, ok := h.inviteScope(w, r, org)
	if !ok {
		return
	}
	if !scope.readAll && len(scope.readTeams) == 0 {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	all, err := h.inviteStore.ListInvites(ctx, org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not list invites", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	var invites []model.InviteToken
	for _, inv := range all {
		if scope.readAll || scope.readTeams[inv.TeamID()] != nil {
			invites = append(invites, inv)
		}
	}
	teamNames := map[model.TeamID]string{}
	for _, team := range scope.teams {
		teamNames[team.ID()] = team.Name()
	}
	writable := map[model.TeamID]bool{}
	for id := range scope.writeTeams {
		writable[id] = true
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
		Org:           org,
		Invites:       invites,
		RoleNames:     roleNames,
		TeamNames:     teamNames,
		CanCreate:     scope.writeAll || len(scope.writeTeams) > 0,
		CanManageAll:  scope.writeAll,
		WritableTeams: writable,
		BaseURL:       baseURL.String(),
		Success:       r.URL.Query().Get("success"),
		NewURL:        r.URL.Query().Get("new_url"),
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
	scope, ok := h.inviteScope(w, r, org)
	if !ok {
		return
	}
	if !scope.writeAll && len(scope.writeTeams) == 0 {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	orgRoles, err := h.roleStore.ListOrgRoles(ctx, org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not list org roles", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	var teams []model.Team
	for _, team := range scope.teams {
		if scope.writeAll || scope.writeTeams[team.ID()] != nil {
			teams = append(teams, team)
		}
	}

	vmodel := component.InviteFormVModel{
		Org:      org,
		OrgRoles: orgRoles,
		Teams:    teams,
		TeamOnly: !scope.writeAll,
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
	scope, ok := h.inviteScope(w, r, org)
	if !ok {
		return
	}
	teamID := model.TeamID(r.FormValue("team_id"))
	role := r.FormValue("role")
	if !scope.writeAll {
		// On its teams only: a team is required, and the invitee joins as a
		// member, never with a role the inviter could not grant itself.
		if scope.writeTeams[teamID] == nil {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		role = model.RoleMember
	}

	invite, err := h.invitationService.Create(ctx, httpCtx.TenantID(ctx), org.ID(), user.ID(), service.CreateInvitationInput{
		Role: role, Email: r.FormValue("invitee_email"),
		ExpiresAt: r.FormValue("expires_at"), MaxUses: r.FormValue("max_uses"),
		TeamID: teamID, Perms: scope.perms,
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

	if !h.canManageInvite(w, r, orgSlug, inviteID) {
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

	if !h.canManageInvite(w, r, orgSlug, inviteID) {
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

// inviteScopeOf is what the current user may do with the invitations of an
// organization: all of them with the permissions on the organization, those
// of the teams it holds them on otherwise.
type inviteScopeOf struct {
	perms                 rbac.PermissionSet
	readAll, writeAll     bool
	teams                 []model.Team
	readTeams, writeTeams map[model.TeamID]model.Team
}

// inviteScope resolves the invitation permissions of the current user. It
// writes the response and returns false on failure.
func (h *Handler) inviteScope(w http.ResponseWriter, r *http.Request, org model.Organization) (inviteScopeOf, bool) {
	ctx := r.Context()
	perms, err := httpCtx.ResolvePermissions(ctx, org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not resolve permissions", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return inviteScopeOf{}, false
	}
	scope := inviteScopeOf{
		perms:      perms,
		readAll:    perms.Has(rbac.PermInvitesRead),
		writeAll:   perms.Has(rbac.PermInvitesWrite),
		readTeams:  map[model.TeamID]model.Team{},
		writeTeams: map[model.TeamID]model.Team{},
	}
	if h.teamStore == nil {
		return scope, true
	}
	teams, err := h.teamStore.ListOrgTeams(ctx, org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not list teams", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return inviteScopeOf{}, false
	}
	scope.teams = teams
	for _, team := range teams {
		if perms.HasInTeam(string(team.ID()), rbac.PermInvitesRead) {
			scope.readTeams[team.ID()] = team
		}
		if perms.HasInTeam(string(team.ID()), rbac.PermInvitesWrite) {
			scope.writeTeams[team.ID()] = team
		}
	}
	return scope, true
}

// canManageInvite resolves the invitation of the URL, which must belong to the
// organization, and checks the user may revoke or delete it. It writes the
// response and returns false otherwise.
func (h *Handler) canManageInvite(w http.ResponseWriter, r *http.Request, orgSlug, inviteID string) bool {
	ctx := r.Context()
	org, invite, err := h.resolveOrgAndInvite(ctx, orgSlug, inviteID)
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return false
	}
	scope, ok := h.inviteScope(w, r, org)
	if !ok {
		return false
	}
	if !scope.writeAll && (invite.TeamID() == "" || scope.writeTeams[invite.TeamID()] == nil) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return false
	}
	return true
}
