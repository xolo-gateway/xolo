package org

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

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

// SetTeams enables the team pages and the team-aware budget checks.
func (h *Handler) SetTeams(teams port.TeamStore) {
	h.teamStore = teams
}

// mountTeams registers the team pages. The organization decides what a team
// is and what it can use (teams:write); the members and the split of the
// budget can be managed on the team alone (members:write, quota:write held
// through a team role).
func (h *Handler) mountTeams() {
	h.mux.Handle("GET /{orgSlug}/admin/teams", http.HandlerFunc(h.getTeamsPage))
	h.mux.Handle("GET /{orgSlug}/admin/teams/new", http.HandlerFunc(h.getNewTeamPage))
	h.mux.Handle("POST /{orgSlug}/admin/teams", http.HandlerFunc(h.createTeam))
	h.mux.Handle("GET /{orgSlug}/admin/teams/{teamID}", http.HandlerFunc(h.getTeamPage))
	h.mux.Handle("POST /{orgSlug}/admin/teams/{teamID}/edit", http.HandlerFunc(h.updateTeam))
	h.mux.Handle("POST /{orgSlug}/admin/teams/{teamID}/delete", http.HandlerFunc(h.deleteTeam))
	h.mux.Handle("POST /{orgSlug}/admin/teams/{teamID}/status", http.HandlerFunc(h.setTeamStatus))
	h.mux.Handle("POST /{orgSlug}/admin/teams/{teamID}/budget", http.HandlerFunc(h.saveTeamBudget))
	h.mux.Handle("POST /{orgSlug}/admin/teams/{teamID}/members", http.HandlerFunc(h.addTeamMember))
	h.mux.Handle("GET /{orgSlug}/admin/teams/{teamID}/members/{membershipID}", http.HandlerFunc(h.getTeamMemberPage))
	h.mux.Handle("POST /{orgSlug}/admin/teams/{teamID}/members/{membershipID}", http.HandlerFunc(h.updateTeamMemberRoles))
	h.mux.Handle("POST /{orgSlug}/admin/teams/{teamID}/members/{membershipID}/budget", http.HandlerFunc(h.saveTeamMemberBudget))
	h.mux.Handle("POST /{orgSlug}/admin/teams/{teamID}/members/{membershipID}/status", http.HandlerFunc(h.setTeamMemberStatus))
	h.mux.Handle("DELETE /{orgSlug}/admin/teams/{teamID}/members/{membershipID}", http.HandlerFunc(h.removeTeamMember))
}

// teamScope is the organization of a team page, the team when the route
// names one, and the permissions of the current user in the organization.
type teamScope struct {
	org   model.Organization
	team  model.Team
	perms rbac.PermissionSet
}

func (s teamScope) can(perm rbac.Permission) bool {
	if s.team == nil {
		return s.perms.Has(perm)
	}
	return s.perms.HasInTeam(string(s.team.ID()), perm)
}

// resolveTeamScope resolves the organization of the URL and, when withTeam,
// the team of the URL, which must belong to it: a missing and a foreign team
// both answer 404. It writes the response and returns false on failure.
func (h *Handler) resolveTeamScope(w http.ResponseWriter, r *http.Request, withTeam bool) (teamScope, bool) {
	ctx := r.Context()
	if h.teamStore == nil {
		http.NotFound(w, r)
		return teamScope{}, false
	}
	org, err := h.orgFromSlug(ctx, r.PathValue("orgSlug"))
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return teamScope{}, false
	}
	perms, err := httpCtx.ResolvePermissions(ctx, org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not resolve permissions", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return teamScope{}, false
	}
	scope := teamScope{org: org, perms: perms}
	if !withTeam {
		return scope, true
	}
	team, err := h.teamStore.GetTeamByID(ctx, model.TeamID(r.PathValue("teamID")))
	if errors.Is(err, port.ErrNotFound) || (err == nil && team.OrgID() != org.ID()) {
		http.NotFound(w, r)
		return teamScope{}, false
	}
	if err != nil {
		slog.ErrorContext(ctx, "could not load team", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return teamScope{}, false
	}
	scope.team = team
	return scope, true
}

// canSeeTeam reports whether the team page is visible: with teams:read, or a
// team role reading its members or budget.
func (s teamScope) canSeeTeam() bool {
	return s.perms.Has(rbac.PermTeamsRead) || s.can(rbac.PermMembersRead) || s.can(rbac.PermQuotaRead)
}

func forbidden(w http.ResponseWriter) {
	http.Error(w, "Forbidden", http.StatusForbidden)
}

func (h *Handler) teamLayout(ctx context.Context, org model.Organization, crumbs ...common.BreadcrumbItem) common.AppLayoutVModel {
	return common.AppLayoutVModel{
		User:         httpCtx.User(ctx),
		SelectedItem: "org-" + org.Slug() + "-teams",
		Context:      common.ContextOrg,
		ContextName:  org.Name(),
		ContextSlug:  org.Slug(),
		ContextOrgID: org.ID(),
		Breadcrumbs: append([]common.BreadcrumbItem{
			{Label: org.Name(), Href: "/orgs/" + org.Slug() + "/usage"},
			{Label: "Équipes", Href: "/orgs/" + org.Slug() + "/admin/teams"},
		}, crumbs...),
	}
}

const teamSlugTakenMessage = "Une équipe de l'organisation porte déjà cet identifiant."

// teamSlugError checks the slug of a new or renamed team, and returns the
// message to show when it is refused.
func (h *Handler) teamSlugError(ctx context.Context, orgID model.OrgID, slug string) (string, error) {
	err := service.CheckTeamSlug(ctx, h.teamStore, orgID, slug)
	switch {
	case err == nil:
		return "", nil
	case errors.Is(err, port.ErrInvalid):
		return "L'identifiant ne peut contenir que des minuscules, des chiffres et des tirets, sans tiret au début ni à la fin.", nil
	case errors.Is(err, port.ErrTeamNameConflict):
		return "Un modèle de l'organisation porte déjà un nom commençant par « " + slug + "/ » : choisissez un autre identifiant.", nil
	}
	return "", err
}

func teamURL(org model.Organization, team model.Team, parts ...string) string {
	return "/orgs/" + org.Slug() + "/admin/teams/" + string(team.ID()) + strings.Join(parts, "")
}

func (h *Handler) getTeamsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	scope, ok := h.resolveTeamScope(w, r, false)
	if !ok {
		return
	}
	teams, err := h.teamStore.ListOrgTeams(ctx, scope.org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not list teams", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	var rows []component.TeamRow
	for _, team := range teams {
		s := teamScope{org: scope.org, team: team, perms: scope.perms}
		if !s.canSeeTeam() {
			continue
		}
		members, err := h.teamStore.ListTeamMembers(ctx, team.ID())
		if err != nil {
			slog.ErrorContext(ctx, "could not list team members", slogx.Error(err))
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		rows = append(rows, component.TeamRow{Team: team, Members: len(members)})
	}
	if len(rows) == 0 && !scope.perms.Has(rbac.PermTeamsRead) {
		forbidden(w)
		return
	}
	templ.Handler(component.TeamsPage(component.TeamsPageVModel{
		AppLayoutVModel: h.teamLayout(ctx, scope.org),
		Org:             scope.org,
		Teams:           rows,
		CanEdit:         scope.perms.Has(rbac.PermTeamsWrite),
		Success:         r.URL.Query().Get("success"),
	})).ServeHTTP(w, r)
}

func (h *Handler) getNewTeamPage(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.resolveTeamScope(w, r, false)
	if !ok {
		return
	}
	if !scope.perms.Has(rbac.PermTeamsWrite) {
		forbidden(w)
		return
	}
	h.renderTeamForm(w, r, scope.org, component.TeamFormVModel{})
}

func (h *Handler) renderTeamForm(w http.ResponseWriter, r *http.Request, org model.Organization, vmodel component.TeamFormVModel) {
	vmodel.Org = org
	vmodel.AppLayoutVModel = h.teamLayout(r.Context(), org, common.BreadcrumbItem{Label: "Nouvelle"})
	templ.Handler(component.TeamForm(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) createTeam(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	scope, ok := h.resolveTeamScope(w, r, false)
	if !ok {
		return
	}
	if !scope.perms.Has(rbac.PermTeamsWrite) {
		forbidden(w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}
	form := component.TeamFormVModel{
		Slug:        strings.TrimSpace(r.FormValue("slug")),
		Name:        strings.TrimSpace(r.FormValue("name")),
		Description: strings.TrimSpace(r.FormValue("description")),
	}
	if form.Name == "" {
		form.Error = "Le nom de l'équipe est requis."
		h.renderTeamForm(w, r, scope.org, form)
		return
	}
	if msg, err := h.teamSlugError(ctx, scope.org.ID(), form.Slug); err != nil {
		slog.ErrorContext(ctx, "could not check team slug", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	} else if msg != "" {
		form.Error = msg
		h.renderTeamForm(w, r, scope.org, form)
		return
	}
	team := model.NewTeam(scope.org.ID(), form.Slug, form.Name, form.Description)
	if err := h.teamStore.CreateTeam(ctx, team); err != nil {
		if webcommon.RejectLocked(w, r, err) {
			return
		}
		if errors.Is(err, port.ErrAlreadyExists) {
			form.Error = teamSlugTakenMessage
			h.renderTeamForm(w, r, scope.org, form)
			return
		}
		slog.ErrorContext(ctx, "could not create team", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, teamURL(scope.org, team)+"?success=created", http.StatusSeeOther)
}

func (h *Handler) getTeamPage(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.resolveTeamScope(w, r, true)
	if !ok {
		return
	}
	if !scope.canSeeTeam() {
		forbidden(w)
		return
	}
	h.renderTeamPage(w, r, scope, r.URL.Query().Get("success"), "")
}

func (h *Handler) renderTeamPage(w http.ResponseWriter, r *http.Request, scope teamScope, success, formErr string) {
	ctx := r.Context()
	org, team := scope.org, scope.team
	fail := func(what string, err error) {
		slog.ErrorContext(ctx, "could not load team page: "+what, slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	}

	options, err := h.modelGrantOptions(ctx, org.ID())
	if err != nil {
		fail("models", err)
		return
	}
	selectedModels := map[string]bool{}
	for _, g := range team.ModelGrants() {
		selectedModels[modelGrantKey(g.Kind, g.ModelID)] = true
	}

	mws, err := h.middlewareStore.ListMiddlewares(ctx, org.ID())
	if err != nil {
		fail("middlewares", err)
		return
	}
	var mwOptions []component.TeamMiddlewareOption
	for _, mw := range mws {
		mwOptions = append(mwOptions, component.TeamMiddlewareOption{ID: string(mw.ID()), Name: mw.Name(), AppliesToAll: mw.AppliesToAll(), Enabled: mw.Enabled()})
	}
	selectedMws := map[string]bool{}
	for _, id := range team.MiddlewareIDs() {
		selectedMws[string(id)] = true
	}

	members, err := h.teamStore.ListTeamMembers(ctx, team.ID())
	if err != nil {
		fail("members", err)
		return
	}
	roles, err := h.roleStore.ListOrgRoles(ctx, org.ID())
	if err != nil {
		fail("roles", err)
		return
	}
	roleNames := map[model.RoleID]string{}
	for _, role := range roles {
		roleNames[role.ID()] = role.Name()
	}
	now := time.Now()
	var rows []component.TeamMemberRow
	inTeam := map[model.MembershipID]bool{}
	for _, m := range members {
		inTeam[m.MembershipID()] = true
		row := component.TeamMemberRow{Member: m}
		for _, id := range m.RoleIDs() {
			row.RoleNames = append(row.RoleNames, roleNames[id])
		}
		scopeID := model.TeamMemberScopeID(team.ID(), m.UserID())
		if row.Budget, err = h.optionalQuota(ctx, model.QuotaScopeTeamMember, scopeID); err != nil {
			fail("member budget", err)
			return
		}
		if row.MonthSpend, err = h.usageStore.SumQuotaCostSince(ctx, model.QuotaScopeTeamMember, scopeID, org.ID(), model.StartOfMonth(now)); err != nil {
			fail("member spend", err)
			return
		}
		rows = append(rows, row)
	}
	var candidates []model.Membership
	if scope.can(rbac.PermMembersWrite) {
		orgMembers, _, err := h.orgStore.ListOrgMembers(ctx, org.ID(), port.ListOrgMembersOptions{})
		if err != nil {
			fail("org members", err)
			return
		}
		for _, m := range orgMembers {
			if !inTeam[m.ID()] && m.User() != nil && m.User().Provider() != model.ApplicationProvider {
				candidates = append(candidates, m)
			}
		}
	}

	apps, err := h.applicationStore.QueryApplications(ctx, org.ID())
	if err != nil {
		fail("applications", err)
		return
	}
	var bound []model.Application
	for _, app := range apps {
		if app.TeamID() == team.ID() {
			bound = append(bound, app)
		}
	}

	teamQuota, err := h.optionalQuota(ctx, model.QuotaScopeTeam, string(team.ID()))
	if err != nil {
		fail("team budget", err)
		return
	}
	spend, err := h.scopeSpend(ctx, model.QuotaScopeTeam, string(team.ID()), org.ID(), now)
	if err != nil {
		fail("team spend", err)
		return
	}

	sample := model.ModelName{Org: org.Slug(), Team: team.Slug(), Local: "<modèle>"}.String()
	if len(options) > 0 {
		for _, opt := range options {
			if selectedModels[modelGrantKey(opt.Kind, opt.ID)] {
				sample = model.ModelName{Org: org.Slug(), Team: team.Slug(), Local: opt.Label}.String()
				break
			}
		}
	}

	vmodel := component.TeamPageVModel{
		AppLayoutVModel:     h.teamLayout(ctx, org, common.BreadcrumbItem{Label: team.Name()}),
		Org:                 org,
		Team:                team,
		Success:             success,
		Error:               formErr,
		CanEditTeam:         scope.perms.Has(rbac.PermTeamsWrite),
		CanManageMembers:    scope.can(rbac.PermMembersWrite),
		CanEditTeamBudget:   scope.perms.Has(rbac.PermQuotaWrite),
		CanSplitBudget:      scope.can(rbac.PermQuotaWrite),
		CanReadBudget:       scope.can(rbac.PermQuotaRead),
		ModelOptions:        options,
		SelectedModels:      selectedModels,
		MiddlewareOptions:   mwOptions,
		SelectedMiddlewares: selectedMws,
		Members:             rows,
		Candidates:          candidates,
		Roles:               roles,
		Applications:        bound,
		TeamQuota:           teamQuota,
		Spend:               spend,
		SampleModel:         sample,
	}
	templ.Handler(component.TeamPage(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) updateTeam(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	scope, ok := h.resolveTeamScope(w, r, true)
	if !ok {
		return
	}
	if !scope.perms.Has(rbac.PermTeamsWrite) {
		forbidden(w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		h.renderTeamPage(w, r, scope, "", "Le nom de l'équipe est requis.")
		return
	}
	slug := strings.TrimSpace(r.FormValue("slug"))
	if slug == "" {
		slug = scope.team.Slug()
	}
	if slug != scope.team.Slug() {
		if msg, err := h.teamSlugError(ctx, scope.org.ID(), slug); err != nil {
			slog.ErrorContext(ctx, "could not check team slug", slogx.Error(err))
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		} else if msg != "" {
			h.renderTeamPage(w, r, scope, "", msg)
			return
		}
	}

	// Only models and middlewares of the organization can be granted.
	options, err := h.modelGrantOptions(ctx, scope.org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not list models", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	known := map[string]bool{}
	for _, opt := range options {
		known[modelGrantKey(opt.Kind, opt.ID)] = true
	}
	var grants []model.ModelGrant
	for _, g := range parseModelGrants(r) {
		if known[modelGrantKey(g.Kind, g.ModelID)] {
			grants = append(grants, g)
		}
	}
	mws, err := h.middlewareStore.ListMiddlewares(ctx, scope.org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not list middlewares", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	var mwIDs []model.MiddlewareID
	for _, mw := range mws {
		if slices.Contains(r.Form["middlewares"], string(mw.ID())) {
			mwIDs = append(mwIDs, mw.ID())
		}
	}

	team := model.CopyTeam(scope.team)
	team.SetSlug(slug)
	team.SetName(name)
	team.SetDescription(strings.TrimSpace(r.FormValue("description")))
	team.SetModelGrants(grants)
	team.SetMiddlewareIDs(mwIDs)
	if err := h.teamStore.SaveTeam(ctx, team); err != nil {
		if webcommon.RejectLocked(w, r, err) {
			return
		}
		if errors.Is(err, port.ErrAlreadyExists) {
			h.renderTeamPage(w, r, scope, "", teamSlugTakenMessage)
			return
		}
		if errors.Is(err, port.ErrTeamHasMiddlewares) {
			h.renderTeamPage(w, r, scope, "", "Des middlewares décochés ne sont rattachés qu'à cette équipe : sans équipe, ils s'appliqueraient à toute l'organisation. Rattachez-les d'abord à une autre équipe, ou supprimez-les.")
			return
		}
		slog.ErrorContext(ctx, "could not save team", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, teamURL(scope.org, team)+"?success=saved", http.StatusSeeOther)
}

// formStatus reads the status of a suspend or reactivate form.
func formStatus(w http.ResponseWriter, r *http.Request) (model.Status, bool) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return "", false
	}
	status := model.Status(r.FormValue("status"))
	if !status.Valid() {
		http.Error(w, "Invalid status", http.StatusBadRequest)
		return "", false
	}
	return status, true
}

// setTeamStatus suspends or reactivates the team: a suspended team grants
// nothing to its members, who stay in it.
func (h *Handler) setTeamStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	scope, ok := h.resolveTeamScope(w, r, true)
	if !ok {
		return
	}
	if !scope.perms.Has(rbac.PermTeamsWrite) {
		forbidden(w)
		return
	}
	status, ok := formStatus(w, r)
	if !ok {
		return
	}
	team := model.CopyTeam(scope.team)
	team.SetStatus(status)
	if err := h.teamStore.SaveTeam(ctx, team); err != nil {
		if webcommon.RejectLocked(w, r, err) {
			return
		}
		slog.ErrorContext(ctx, "could not change team status", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, teamURL(scope.org, team)+"?success=saved", http.StatusSeeOther)
}

// setTeamMemberStatus suspends or reactivates a member in the team: a
// suspended member keeps its roles but receives nothing through the team.
func (h *Handler) setTeamMemberStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	scope, ok := h.resolveTeamScope(w, r, true)
	if !ok {
		return
	}
	if !scope.can(rbac.PermMembersWrite) {
		forbidden(w)
		return
	}
	member, ok := h.teamMember(w, r, scope)
	if !ok {
		return
	}
	status, ok := formStatus(w, r)
	if !ok {
		return
	}
	if err := h.teamStore.SetTeamMemberStatus(ctx, scope.team.ID(), member.MembershipID(), status); err != nil {
		if webcommon.RejectLocked(w, r, err) {
			return
		}
		slog.ErrorContext(ctx, "could not change team member status", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, teamURL(scope.org, scope.team)+"?success=saved", http.StatusSeeOther)
}

func (h *Handler) deleteTeam(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	scope, ok := h.resolveTeamScope(w, r, true)
	if !ok {
		return
	}
	if !scope.perms.Has(rbac.PermTeamsWrite) {
		forbidden(w)
		return
	}
	if err := h.teamStore.DeleteTeam(ctx, scope.team.ID()); err != nil {
		if webcommon.RejectLocked(w, r, err) {
			return
		}
		if errors.Is(err, port.ErrTeamHasMiddlewares) {
			h.renderTeamPage(w, r, scope, "", "Des middlewares ne sont rattachés qu'à cette équipe : sans elle, ils s'appliqueraient à toute l'organisation. Rattachez-les à une autre équipe, ou supprimez-les, avant de la supprimer.")
			return
		}
		if errors.Is(err, port.ErrNotAllowed) {
			h.renderTeamPage(w, r, scope, "", "Des applications sont rattachées à cette équipe : rattachez-les ailleurs avant de la supprimer.")
			return
		}
		slog.ErrorContext(ctx, "could not delete team", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/orgs/"+scope.org.Slug()+"/admin/teams?success=deleted", http.StatusSeeOther)
}

// saveTeamBudget saves the parts of the team budget the user may change: the
// envelope with quota:write on the organization, the split among members with
// quota:write on the team.
func (h *Handler) saveTeamBudget(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	scope, ok := h.resolveTeamScope(w, r, true)
	if !ok {
		return
	}
	canEnvelope, canSplit := scope.perms.Has(rbac.PermQuotaWrite), scope.can(rbac.PermQuotaWrite)
	if !canEnvelope && !canSplit {
		forbidden(w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}
	current, err := h.optionalQuota(ctx, model.QuotaScopeTeam, string(scope.team.ID()))
	if err != nil {
		slog.ErrorContext(ctx, "could not load team budget", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	daily, monthly, yearly := quotaBudgets(current)
	if canEnvelope {
		stored := current
		if stored == nil {
			stored = model.NewQuota(model.QuotaScopeTeam, string(scope.team.ID()), orgCurrency(scope.org), nil, nil, nil)
		}
		var fieldErrors map[string]string
		daily, monthly, yearly, fieldErrors = parseQuotaBudgetFields(r, stored)
		if len(fieldErrors) > 0 {
			h.renderTeamPage(w, r, scope, "", component.QuotaFormErrorSummary(fieldErrors))
			return
		}
	}
	cascade := model.QuotaCascadeFree
	if current != nil {
		cascade = current.Cascade()
	}
	if canSplit && r.Form.Has("cascade") {
		cascade = model.QuotaCascade(r.FormValue("cascade")).Normalize()
		if !cascade.Valid() {
			http.Error(w, "Invalid cascade", http.StatusBadRequest)
			return
		}
	}

	quota := model.NewQuota(model.QuotaScopeTeam, string(scope.team.ID()), orgCurrency(scope.org), daily, monthly, yearly)
	quota.SetCascade(cascade)
	if !h.saveQuotaChecked(w, r, quota, func(msg string) { h.renderTeamPage(w, r, scope, "", msg) }) {
		return
	}

	if canSplit {
		share := r.FormValue("share_quota_equally") != ""
		if share != scope.team.ShareQuotaEqually() {
			team := model.CopyTeam(scope.team)
			team.SetShareQuotaEqually(share)
			if err := h.teamStore.SaveTeam(ctx, team); err != nil {
				if webcommon.RejectLocked(w, r, err) {
					return
				}
				slog.ErrorContext(ctx, "could not save team", slogx.Error(err))
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				return
			}
		}
	}
	http.Redirect(w, r, teamURL(scope.org, scope.team)+"?success=saved", http.StatusSeeOther)
}

func (h *Handler) addTeamMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	scope, ok := h.resolveTeamScope(w, r, true)
	if !ok {
		return
	}
	if !scope.can(rbac.PermMembersWrite) {
		forbidden(w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}
	membership, ok := h.teamMembership(w, r, scope, model.MembershipID(r.FormValue("membership_id")))
	if !ok {
		return
	}
	if err := h.teamStore.SetTeamMember(ctx, scope.team.ID(), membership.ID(), nil); err != nil {
		if webcommon.RejectLocked(w, r, err) {
			return
		}
		slog.ErrorContext(ctx, "could not add team member", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, teamURL(scope.org, scope.team)+"?success=saved", http.StatusSeeOther)
}

// teamMembership resolves a membership of the organization of the team: a
// missing and a foreign one both answer 404.
func (h *Handler) teamMembership(w http.ResponseWriter, r *http.Request, scope teamScope, id model.MembershipID) (model.Membership, bool) {
	ctx := r.Context()
	membership, err := h.orgStore.GetMembership(ctx, id)
	if errors.Is(err, port.ErrNotFound) || (err == nil && membership.OrgID() != scope.org.ID()) {
		http.NotFound(w, r)
		return nil, false
	}
	if err != nil {
		slog.ErrorContext(ctx, "could not load membership", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return nil, false
	}
	return membership, true
}

// teamMember resolves a member of the team of the URL.
func (h *Handler) teamMember(w http.ResponseWriter, r *http.Request, scope teamScope) (model.TeamMember, bool) {
	ctx := r.Context()
	members, err := h.teamStore.ListTeamMembers(ctx, scope.team.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not list team members", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return nil, false
	}
	id := model.MembershipID(r.PathValue("membershipID"))
	for _, m := range members {
		if m.MembershipID() == id {
			return m, true
		}
	}
	http.NotFound(w, r)
	return nil, false
}

func (h *Handler) removeTeamMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	scope, ok := h.resolveTeamScope(w, r, true)
	if !ok {
		return
	}
	if !scope.can(rbac.PermMembersWrite) {
		forbidden(w)
		return
	}
	member, ok := h.teamMember(w, r, scope)
	if !ok {
		return
	}
	if err := h.teamStore.RemoveTeamMember(ctx, scope.team.ID(), member.MembershipID()); err != nil {
		if webcommon.RejectLocked(w, r, err) {
			return
		}
		slog.ErrorContext(ctx, "could not remove team member", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, teamURL(scope.org, scope.team)+"?success=saved", http.StatusSeeOther)
}

func (h *Handler) getTeamMemberPage(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.resolveTeamScope(w, r, true)
	if !ok {
		return
	}
	if !scope.canSeeTeam() {
		forbidden(w)
		return
	}
	member, ok := h.teamMember(w, r, scope)
	if !ok {
		return
	}
	h.renderTeamMemberPage(w, r, scope, member, r.URL.Query().Get("success"), "")
}

func (h *Handler) renderTeamMemberPage(w http.ResponseWriter, r *http.Request, scope teamScope, member model.TeamMember, success, formErr string) {
	ctx := r.Context()
	roles, err := h.roleStore.ListOrgRoles(ctx, scope.org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not list roles", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	// The builtin roles are the organization roles; a team role is a custom
	// one carrying team-scopable permissions.
	var teamRoles []model.Role
	assignable := map[string]bool{}
	for _, role := range roles {
		if role.Builtin() {
			continue
		}
		teamRoles = append(teamRoles, role)
		assignable[string(role.ID())] = canAssignTeamRole(scope, role)
	}
	selected := map[string]bool{}
	for _, id := range member.RoleIDs() {
		selected[string(id)] = true
	}
	scopeID := model.TeamMemberScopeID(scope.team.ID(), member.UserID())
	budget, err := h.optionalQuota(ctx, model.QuotaScopeTeamMember, scopeID)
	if err != nil {
		slog.ErrorContext(ctx, "could not load member budget", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	spend, err := h.scopeSpend(ctx, model.QuotaScopeTeamMember, scopeID, scope.org.ID(), time.Now())
	if err != nil {
		slog.ErrorContext(ctx, "could not load member spend", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	name := ""
	if member.User() != nil {
		name = member.User().DisplayName()
	}
	templ.Handler(component.TeamMemberPage(component.TeamMemberPageVModel{
		AppLayoutVModel: h.teamLayout(ctx, scope.org,
			common.BreadcrumbItem{Label: scope.team.Name(), Href: teamURL(scope.org, scope.team)},
			common.BreadcrumbItem{Label: name}),
		Org:              scope.org,
		Team:             scope.team,
		Member:           member,
		Success:          success,
		Error:            formErr,
		CanManageMembers: scope.can(rbac.PermMembersWrite),
		CanSplitBudget:   scope.can(rbac.PermQuotaWrite),
		Roles:            teamRoles,
		AssignableRoles:  assignable,
		SelectedRoles:    selected,
		Budget:           budget,
		Spend:            spend,
	})).ServeHTTP(w, r)
}

// canAssignTeamRole reports whether the current user may hand role out on the
// team: it must hold on the team every team-scopable permission of the role,
// so that a team lead never widens what the team can do.
func canAssignTeamRole(scope teamScope, role model.Role) bool {
	if !scope.can(rbac.PermMembersWrite) {
		return false
	}
	for _, code := range role.Permissions() {
		perm := rbac.Permission(code)
		if rbac.IsTeamScopable(perm) && !scope.can(perm) {
			return false
		}
	}
	return true
}

func (h *Handler) updateTeamMemberRoles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	scope, ok := h.resolveTeamScope(w, r, true)
	if !ok {
		return
	}
	if !scope.can(rbac.PermMembersWrite) {
		forbidden(w)
		return
	}
	member, ok := h.teamMember(w, r, scope)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}
	roles, err := h.roleStore.ListOrgRoles(ctx, scope.org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not list roles", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	// A role the user may not hand out keeps its current state: unchecked
	// boxes of disabled inputs are not submitted.
	var roleIDs []model.RoleID
	for _, role := range roles {
		if role.Builtin() {
			continue
		}
		held := slices.Contains(member.RoleIDs(), role.ID())
		wanted := slices.Contains(r.Form["roles"], string(role.ID()))
		if !canAssignTeamRole(scope, role) {
			wanted = held
		}
		if wanted {
			roleIDs = append(roleIDs, role.ID())
		}
	}
	if err := h.teamStore.SetTeamMember(ctx, scope.team.ID(), member.MembershipID(), roleIDs); err != nil {
		if webcommon.RejectLocked(w, r, err) {
			return
		}
		slog.ErrorContext(ctx, "could not save team member", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, teamURL(scope.org, scope.team, "/members/", string(member.MembershipID()))+"?success=saved", http.StatusSeeOther)
}

func (h *Handler) saveTeamMemberBudget(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.resolveTeamScope(w, r, true)
	if !ok {
		return
	}
	if !scope.can(rbac.PermQuotaWrite) {
		forbidden(w)
		return
	}
	member, ok := h.teamMember(w, r, scope)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}
	scopeID := model.TeamMemberScopeID(scope.team.ID(), member.UserID())
	stored, err := h.optionalQuota(r.Context(), model.QuotaScopeTeamMember, scopeID)
	if err != nil {
		slog.ErrorContext(r.Context(), "could not load team member budget", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if stored == nil {
		stored = model.NewQuota(model.QuotaScopeTeamMember, scopeID, orgCurrency(scope.org), nil, nil, nil)
	}
	daily, monthly, yearly, fieldErrors := parseQuotaBudgetFields(r, stored)
	if len(fieldErrors) > 0 {
		h.renderTeamMemberPage(w, r, scope, member, "", component.QuotaFormErrorSummary(fieldErrors))
		return
	}
	quota := model.NewQuota(model.QuotaScopeTeamMember, scopeID, orgCurrency(scope.org), daily, monthly, yearly)
	if !h.saveQuotaChecked(w, r, quota, func(msg string) { h.renderTeamMemberPage(w, r, scope, member, "", msg) }) {
		return
	}
	http.Redirect(w, r, teamURL(scope.org, scope.team, "/members/", string(member.MembershipID()))+"?success=saved", http.StatusSeeOther)
}

// saveQuotaChecked saves a budget once checked against the cascades of its
// parent and of itself. A broken cascade is explained through renderErr; it
// returns true when the budget was saved.
func (h *Handler) saveQuotaChecked(w http.ResponseWriter, r *http.Request, quota model.Quota, renderErr func(string)) bool {
	ctx := r.Context()
	if h.teamStore != nil {
		err := service.CheckQuotaCascade(ctx, cascadeReader{h.quotaStore, h.teamStore}, quota)
		var violation *service.CascadeViolation
		if errors.As(err, &violation) {
			renderErr(cascadeViolationMessage(violation, quota.Currency()))
			return false
		}
		if err != nil {
			slog.ErrorContext(ctx, "could not check budget cascade", slogx.Error(err))
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return false
		}
	}
	if err := h.quotaStore.SetQuota(ctx, quota); err != nil {
		if webcommon.RejectLocked(w, r, err) {
			return false
		}
		slog.ErrorContext(ctx, "could not save budget", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return false
	}
	return true
}

// cascadeReader joins the stores the cascade check reads.
type cascadeReader struct {
	port.QuotaStore
	port.TeamStore
}

var _ service.QuotaCascadeReader = cascadeReader{}

var cascadePeriodLabels = map[string]string{"daily": "journalier", "monthly": "mensuel", "yearly": "annuel"}

var cascadeScopeLabels = map[model.QuotaScope]string{
	model.QuotaScopeOrg:        "de l'organisation",
	model.QuotaScopeTeam:       "de l'équipe",
	model.QuotaScopeTeamMember: "du membre",
}

// cascadeViolationMessage explains a broken cascade in the words of the
// budget pages.
func cascadeViolationMessage(v *service.CascadeViolation, currency string) string {
	period := cascadePeriodLabels[v.Period]
	limit := common.FormatCost(v.Limit, currency)
	if v.Mode == model.QuotaCascadeSplit {
		return "Les budgets " + period + "s répartis sous le budget " + cascadeScopeLabels[v.Parent] + " totalisent " +
			common.FormatCost(v.Got, currency) + ", au-delà de son enveloppe de " + limit + " : la répartition est « partagée »."
	}
	return "Le budget " + period + " " + cascadeScopeLabels[v.Child] + " (" + common.FormatCost(v.Got, currency) +
		") dépasse le budget " + period + " " + cascadeScopeLabels[v.Parent] + " (" + limit + ") : la répartition est « plafonnée »."
}

func (h *Handler) optionalQuota(ctx context.Context, scope model.QuotaScope, scopeID string) (model.Quota, error) {
	q, err := h.quotaStore.GetQuota(ctx, scope, scopeID)
	if errors.Is(err, port.ErrNotFound) {
		return nil, nil
	}
	return q, errors.WithStack(err)
}

// scopeSpend returns the spend of a budget scope over the current day, month
// and year, from the counters the enforcer reads.
func (h *Handler) scopeSpend(ctx context.Context, scope model.QuotaScope, scopeID string, orgID model.OrgID, now time.Time) ([3]int64, error) {
	var spend [3]int64
	for i, since := range []time.Time{model.StartOfDay(now), model.StartOfMonth(now), model.StartOfYear(now)} {
		v, err := h.usageStore.SumQuotaCostSince(ctx, scope, scopeID, orgID, since)
		if err != nil {
			return spend, errors.WithStack(err)
		}
		spend[i] = v
	}
	return spend, nil
}

func quotaBudgets(q model.Quota) (daily, monthly, yearly *int64) {
	if q == nil {
		return nil, nil, nil
	}
	return q.DailyBudget(), q.MonthlyBudget(), q.YearlyBudget()
}

func orgCurrency(org model.Organization) string {
	if org.Currency() == "" {
		return model.DefaultCurrency
	}
	return org.Currency()
}

// teamNameConflict explains why name, the local name of a new real or virtual
// model of the organization, would be read as a team-qualified name. It is
// empty when the name is free.
func (h *Handler) teamNameConflict(ctx context.Context, orgID model.OrgID, name string) (string, error) {
	if h.teamStore == nil {
		return "", nil
	}
	err := service.CheckModelLocalName(ctx, h.teamStore, orgID, name)
	if errors.Is(err, port.ErrTeamNameConflict) {
		return teamNameConflictMessage(name), nil
	}
	return "", err
}

// teamNameConflictMessage explains that name starts with the slug of a team
// of the organization.
func teamNameConflictMessage(name string) string {
	segment, _, _ := strings.Cut(name, "/")
	return "Le nom « " + name + " » commence par l'identifiant de l'équipe « " + segment + " » : " +
		"un client l'appelant serait débité sur cette équipe. Choisissez un autre nom."
}

// orgTeamSlugs returns the slugs of the organization's teams, for checking
// many model names against them with a single query.
func (h *Handler) orgTeamSlugs(ctx context.Context, orgID model.OrgID) (map[string]bool, error) {
	slugs := map[string]bool{}
	if h.teamStore == nil {
		return slugs, nil
	}
	teams, err := h.teamStore.ListOrgTeams(ctx, orgID)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	for _, t := range teams {
		slugs[t.Slug()] = true
	}
	return slugs, nil
}
