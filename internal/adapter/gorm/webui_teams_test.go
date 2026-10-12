package gorm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/core/service"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui"
	"github.com/xolo-gateway/xolo/internal/http/middleware/memberships"
)

// teamPagesFixture is an organization with a team led by a member holding a
// team role, and a team of another organization.
type teamPagesFixture struct {
	store              *xologorm.Store
	handler            http.Handler
	tenant             model.Tenant
	org                model.Organization
	team, foreignTeam  model.Team
	lead, owner, plain model.User
	plainMembership    model.Membership
	leadRole, wideRole model.Role
}

func newTeamPagesFixture(t *testing.T, store *xologorm.Store) teamPagesFixture {
	t.Helper()
	ctx := context.Background()
	tenant, err := store.GetTenantByID(ctx, testTenantID)
	require.NoError(t, err)
	f := teamPagesFixture{store: store, tenant: tenant}
	f.org = model.NewOrganization(testTenantID, "acme", "Acme", "")
	require.NoError(t, store.CreateOrg(ctx, f.org))
	require.NoError(t, store.EnsureBuiltinRoles(ctx, f.org.ID()))
	other := model.NewOrganization(testTenantID, "other", "Other", "")
	require.NoError(t, store.CreateOrg(ctx, other))
	f.foreignTeam = model.NewTeam(other.ID(), "spies", "Spies", "")
	require.NoError(t, store.CreateTeam(ctx, f.foreignTeam))

	roles, err := store.ListOrgRoles(ctx, f.org.ID())
	require.NoError(t, err)
	builtin := map[string]model.RoleID{}
	for _, role := range roles {
		builtin[role.BuiltinKind()] = role.ID()
	}
	member := func(name, kind string) (model.User, model.Membership) {
		user := model.NewUser(testTenantID, "test", name, name+"@acme.test", name, true, model.PlatformRoleUser)
		require.NoError(t, store.SaveUser(ctx, user))
		m := model.NewMembership(user.ID(), f.org.ID())
		require.NoError(t, store.AddMember(ctx, m))
		require.NoError(t, store.SetMembershipRoles(ctx, m.ID(), []model.RoleID{builtin[kind]}))
		return user, m
	}
	var leadMembership model.Membership
	f.owner, _ = member("owner", model.BuiltinKindOwner)
	f.lead, leadMembership = member("lead", model.BuiltinKindMember)
	f.plain, f.plainMembership = member("plain", model.BuiltinKindMember)

	f.leadRole = model.NewRole(f.org.ID(), "Team lead", "")
	f.leadRole.(*model.BaseRole).SetPermissions([]string{string(rbac.PermMembersWrite), string(rbac.PermQuotaWrite)})
	require.NoError(t, store.CreateRole(ctx, f.leadRole))
	f.wideRole = model.NewRole(f.org.ID(), "Auditor", "")
	f.wideRole.(*model.BaseRole).SetPermissions([]string{string(rbac.PermEventsReadAll)})
	require.NoError(t, store.CreateRole(ctx, f.wideRole))

	f.team = model.NewTeam(f.org.ID(), "legal", "Legal", "")
	require.NoError(t, store.CreateTeam(ctx, f.team))
	require.NoError(t, store.SetTeamMember(ctx, f.team.ID(), leadMembership.ID(), []model.RoleID{f.leadRole.ID()}))

	f.handler = memberships.Middleware(store, store)(webui.NewHandler(
		nil, store, store, store, store, store, store, store, store, store,
		service.NewInvitationService(store), store, store, nil, nil, store, scopeTestSecretKey,
		nil, nil, nil, store, store, store, store, 100, 100,
	).WithTeams(store))
	return f
}

func (f teamPagesFixture) do(t *testing.T, user model.User, method, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx := httpCtx.SetTenant(request.Context(), f.tenant)
	ctx = httpCtx.SetUser(ctx, user)
	ctx = httpCtx.SetBaseURL(ctx, "http://gateway.example.test")
	rr := httptest.NewRecorder()
	f.handler.ServeHTTP(rr, request.WithContext(ctx))
	return rr
}

func TestTeamPages(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := context.Background()
		f := newTeamPagesFixture(t, store)
		teamPath := "/orgs/acme/admin/teams/" + string(f.team.ID())

		// A team of another organization answers like a missing one.
		rr := f.do(t, f.owner, http.MethodGet, "/orgs/acme/admin/teams/"+string(f.foreignTeam.ID()), nil)
		require.Equal(t, http.StatusNotFound, rr.Code)

		// The owner sees and creates teams.
		require.Equal(t, http.StatusOK, f.do(t, f.owner, http.MethodGet, "/orgs/acme/admin/teams", nil).Code)
		rr = f.do(t, f.owner, http.MethodPost, "/orgs/acme/admin/teams", url.Values{"slug": {"rd"}, "name": {"R&D"}})
		require.Equal(t, http.StatusSeeOther, rr.Code, rr.Body.String())
		_, err := store.GetTeamBySlug(ctx, f.org.ID(), "rd")
		require.NoError(t, err)

		// A plain member sees no team.
		require.Equal(t, http.StatusForbidden, f.do(t, f.plain, http.MethodGet, "/orgs/acme/admin/teams", nil).Code)
		require.Equal(t, http.StatusForbidden, f.do(t, f.plain, http.MethodGet, teamPath, nil).Code)

		// The lead manages the members of its team, not what the team uses.
		require.Equal(t, http.StatusOK, f.do(t, f.lead, http.MethodGet, teamPath, nil).Code)
		rr = f.do(t, f.lead, http.MethodPost, teamPath+"/members", url.Values{"membership_id": {string(f.plainMembership.ID())}})
		require.Equal(t, http.StatusSeeOther, rr.Code, rr.Body.String())
		members, err := store.ListTeamMembers(ctx, f.team.ID())
		require.NoError(t, err)
		require.Len(t, members, 2)
		require.Equal(t, http.StatusForbidden, f.do(t, f.lead, http.MethodPost, teamPath+"/edit", url.Values{"name": {"Pwned"}}).Code)
		require.Equal(t, http.StatusForbidden, f.do(t, f.lead, http.MethodPost, "/orgs/acme/admin/teams", url.Values{"slug": {"x"}, "name": {"X"}}).Code)

		// The lead hands out its own role, never one granting more than it holds.
		memberPath := teamPath + "/members/" + string(f.plainMembership.ID())
		rr = f.do(t, f.lead, http.MethodPost, memberPath, url.Values{"roles": {string(f.leadRole.ID()), string(f.wideRole.ID())}})
		require.Equal(t, http.StatusSeeOther, rr.Code, rr.Body.String())
		members, err = store.ListTeamMembers(ctx, f.team.ID())
		require.NoError(t, err)
		for _, m := range members {
			if m.MembershipID() == f.plainMembership.ID() {
				require.Equal(t, []model.RoleID{f.leadRole.ID()}, m.RoleIDs(), "the auditor role widens the lead's access")
			}
		}

		// The lead splits the budget among members, but does not set the
		// envelope of the team.
		rr = f.do(t, f.lead, http.MethodPost, teamPath+"/budget", url.Values{"monthly_budget": {"500"}, "cascade": {"capped"}})
		require.Equal(t, http.StatusSeeOther, rr.Code, rr.Body.String())
		quota, err := store.GetQuota(ctx, model.QuotaScopeTeam, string(f.team.ID()))
		require.NoError(t, err)
		require.Nil(t, quota.MonthlyBudget(), "the envelope is the organization's to set")
		require.Equal(t, model.QuotaCascadeCapped, quota.Cascade())

		rr = f.do(t, f.owner, http.MethodPost, teamPath+"/budget", url.Values{"monthly_budget": {"100"}})
		require.Equal(t, http.StatusSeeOther, rr.Code, rr.Body.String())

		// An invalid amount is refused on the page, the budget left as it was.
		rr = f.do(t, f.owner, http.MethodPost, teamPath+"/budget", url.Values{"monthly_budget": {"-5"}})
		require.Equal(t, http.StatusOK, rr.Code)
		require.Contains(t, rr.Body.String(), "le plafond doit être positif ou nul")
		quota, err = store.GetQuota(ctx, model.QuotaScopeTeam, string(f.team.ID()))
		require.NoError(t, err)
		require.Equal(t, int64(100_000_000), *quota.MonthlyBudget())
		rr = f.do(t, f.lead, http.MethodPost, memberPath+"/budget", url.Values{"monthly_budget": {"150"}})
		require.Equal(t, http.StatusOK, rr.Code, "a member budget above a capped envelope is refused on the page")
		require.Contains(t, rr.Body.String(), "plafonnée")

		// The lead leaves its team: its team role goes with it.
		lead := slices.IndexFunc(members, func(m model.TeamMember) bool { return m.UserID() == f.lead.ID() })
		require.NotEqual(t, -1, lead)
		rr = f.do(t, f.owner, http.MethodDelete, teamPath+"/members/"+string(members[lead].MembershipID()), nil)
		require.Equal(t, http.StatusSeeOther, rr.Code, rr.Body.String())
		members, err = store.ListTeamMembers(ctx, f.team.ID())
		require.NoError(t, err)
		require.Len(t, members, 1)
		require.Equal(t, f.plain.ID(), members[0].UserID())
		perms, err := store.ResolveEffectivePermissions(ctx, f.lead.ID(), f.org.ID())
		require.NoError(t, err)
		require.False(t, perms.InTeam(string(f.team.ID())))
		require.False(t, perms.HasInTeam(string(f.team.ID()), rbac.PermMembersWrite))
	})
}
