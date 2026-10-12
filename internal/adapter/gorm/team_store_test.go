package gorm_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	gormpkg "gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// teamFixture is an organization with a member and a custom role.
type teamFixture struct {
	org        model.Organization
	user       model.User
	membership model.Membership
	role       model.Role
}

func newTeamFixture(t *testing.T, store *xologorm.Store) teamFixture {
	t.Helper()
	ctx := context.Background()
	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	require.NoError(t, store.CreateOrg(ctx, org))
	require.NoError(t, store.EnsureBuiltinRoles(ctx, org.ID()))
	user := model.NewUser(testTenantID, "oidc", "alice", "alice@acme.test", "Alice", true, model.PlatformRoleUser)
	require.NoError(t, store.SaveUser(ctx, user))
	membership := model.NewMembership(user.ID(), org.ID())
	require.NoError(t, store.AddMember(ctx, membership))
	role := model.NewRole(org.ID(), "Team lead", "")
	role.SetPermissions([]string{string(rbac.PermMembersWrite), string(rbac.PermProvidersWrite)})
	require.NoError(t, store.CreateRole(ctx, role))
	return teamFixture{org: org, user: user, membership: membership, role: role}
}

func TestTeamStore(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := context.Background()
		f := newTeamFixture(t, store)

		team := model.NewTeam(f.org.ID(), "legal", "Juridique", "")
		team.SetModelGrants([]model.ModelGrant{{ModelID: "m1", Kind: rbac.ModelKindLLM}, {ModelID: "v1", Kind: rbac.ModelKindVirtual}})
		team.SetMiddlewareIDs([]model.MiddlewareID{"mw1"})
		require.NoError(t, store.CreateTeam(ctx, team))
		require.ErrorIs(t, store.CreateTeam(ctx, model.NewTeam(f.org.ID(), "legal", "Doublon", "")), port.ErrAlreadyExists)

		got, err := store.GetTeamBySlug(ctx, f.org.ID(), "legal")
		require.NoError(t, err)
		require.Equal(t, team.ID(), got.ID())
		require.Len(t, got.ModelGrants(), 2)
		require.Equal(t, []model.MiddlewareID{"mw1"}, got.MiddlewareIDs())

		updated := model.CopyTeam(got)
		updated.SetName("Legal")
		updated.SetModelGrants([]model.ModelGrant{{ModelID: "m2", Kind: rbac.ModelKindLLM}})
		updated.SetMiddlewareIDs(nil)
		require.NoError(t, store.SaveTeam(ctx, updated))
		got, err = store.GetTeamByID(ctx, team.ID())
		require.NoError(t, err)
		require.Equal(t, "Legal", got.Name())
		require.Equal(t, []model.ModelGrant{{ModelID: "m2", Kind: rbac.ModelKindLLM}}, got.ModelGrants())
		require.Empty(t, got.MiddlewareIDs())

		require.NoError(t, store.SetTeamMember(ctx, team.ID(), f.membership.ID(), []model.RoleID{f.role.ID()}))
		members, err := store.ListTeamMembers(ctx, team.ID())
		require.NoError(t, err)
		require.Len(t, members, 1)
		require.Equal(t, f.user.ID(), members[0].UserID())
		require.Equal(t, []model.RoleID{f.role.ID()}, members[0].RoleIDs())
		require.NotNil(t, members[0].User())

		// A role given twice is held once.
		require.NoError(t, store.SetTeamMember(ctx, team.ID(), f.membership.ID(), []model.RoleID{f.role.ID(), f.role.ID()}))
		members, err = store.ListTeamMembers(ctx, team.ID())
		require.NoError(t, err)
		require.Equal(t, []model.RoleID{f.role.ID()}, members[0].RoleIDs())

		// A builtin role is never held on a team, whoever the caller.
		roles, err := store.ListOrgRoles(ctx, f.org.ID())
		require.NoError(t, err)
		builtin := slices.IndexFunc(roles, func(r model.Role) bool { return r.Builtin() })
		require.NotEqual(t, -1, builtin)
		require.ErrorIs(t, store.SetTeamMember(ctx, team.ID(), f.membership.ID(), []model.RoleID{roles[builtin].ID()}), port.ErrInvalid)
		members, err = store.ListTeamMembers(ctx, team.ID())
		require.NoError(t, err)
		require.Equal(t, []model.RoleID{f.role.ID()}, members[0].RoleIDs(), "a refused write changes nothing")

		teams, err := store.ListMembershipTeams(ctx, f.membership.ID())
		require.NoError(t, err)
		require.Len(t, teams, 1)

		// Parents are checked: a membership or a role of another organization
		// is refused.
		other := model.NewOrganization(testTenantID, "other", "Other", "")
		require.NoError(t, store.CreateOrg(ctx, other))
		foreign := model.NewMembership(f.user.ID(), other.ID())
		require.NoError(t, store.AddMember(ctx, foreign))
		require.ErrorIs(t, store.SetTeamMember(ctx, team.ID(), foreign.ID(), nil), port.ErrParentNotFound)
		foreignRole := model.NewRole(other.ID(), "Foreign", "")
		require.NoError(t, store.CreateRole(ctx, foreignRole))
		require.ErrorIs(t, store.SetTeamMember(ctx, team.ID(), f.membership.ID(), []model.RoleID{foreignRole.ID()}), port.ErrParentNotFound)

		taken, err := store.ModelNamePrefixTaken(ctx, f.org.ID(), "meta")
		require.NoError(t, err)
		require.False(t, taken)
	})
}

func TestTeamPermissions(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := context.Background()
		f := newTeamFixture(t, store)
		team := model.NewTeam(f.org.ID(), "legal", "Legal", "")
		team.SetModelGrants([]model.ModelGrant{{ModelID: "m1", Kind: rbac.ModelKindLLM}})
		require.NoError(t, store.CreateTeam(ctx, team))
		require.NoError(t, store.SetTeamMember(ctx, team.ID(), f.membership.ID(), []model.RoleID{f.role.ID()}))

		perms, err := store.ResolveEffectivePermissions(ctx, f.user.ID(), f.org.ID())
		require.NoError(t, err)
		require.True(t, perms.CanUseModel(string(team.ID()), "m1", rbac.ModelKindLLM))
		require.False(t, perms.CanUseModel("", "m1", rbac.ModelKindLLM), "a team grant is not an organization-wide access")
		require.True(t, perms.HasInTeam(string(team.ID()), rbac.PermMembersWrite), "the team role grants its team-scopable permission")
		require.False(t, perms.HasInTeam(string(team.ID()), rbac.PermProvidersWrite), "providers can not be managed on a team")
		require.False(t, perms.Has(rbac.PermMembersWrite))
		require.Equal(t, []rbac.TeamRef{{ID: string(team.ID()), Slug: "legal"}}, perms.Teams())

		// An application bound to the team uses its grants.
		app := model.NewApplication(f.org.ID(), "Batch", "", true)
		app.SetTeamID(team.ID())
		require.NoError(t, store.CreateApplication(ctx, app))
		appPerms, err := store.ResolveApplicationPermissions(ctx, app.ID(), f.org.ID())
		require.NoError(t, err)
		require.True(t, appPerms.CanUseModel(string(team.ID()), "m1", rbac.ModelKindLLM))
		require.False(t, appPerms.HasInTeam(string(team.ID()), rbac.PermMembersWrite), "an application holds no team role")

		// The team can not go while an application is bound to it.
		require.ErrorIs(t, store.DeleteTeam(ctx, team.ID()), port.ErrNotAllowed)
	})
}

func TestTeamMembershipFollowsTheMember(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := context.Background()
		f := newTeamFixture(t, store)
		team := model.NewTeam(f.org.ID(), "legal", "Legal", "")
		require.NoError(t, store.CreateTeam(ctx, team))
		require.NoError(t, store.SetTeamMember(ctx, team.ID(), f.membership.ID(), []model.RoleID{f.role.ID()}))
		budget := model.NewQuota(model.QuotaScopeTeamMember, model.TeamMemberScopeID(team.ID(), f.user.ID()), "EUR", nil, ptrTo(int64(10)), nil)
		require.NoError(t, store.SetQuota(ctx, budget))

		cursor, err := store.CaptureEventCursor(ctx)
		require.NoError(t, err)
		require.NoError(t, store.RemoveMember(ctx, f.membership.ID()))

		members, err := store.ListTeamMembers(ctx, team.ID())
		require.NoError(t, err)
		require.Empty(t, members)
		_, err = store.GetQuota(ctx, model.QuotaScopeTeamMember, model.TeamMemberScopeID(team.ID(), f.user.ID()))
		require.ErrorIs(t, err, port.ErrNotFound, "the member budget goes with the membership")

		events, _ := feedSince(t, store, cursor)
		require.Contains(t, eventTypes(events), "team.updated.v1", "the team projection lists its members")
		require.Contains(t, eventTypes(events), "quota.deleted.v1")
	})
}

// Leaving a team takes the member's team roles and budget with it, and only
// that member's.
func TestRemoveTeamMember(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := context.Background()
		store := newSeededStore(t, db)
		f := newTeamFixture(t, store)
		bob := model.NewUser(testTenantID, "oidc", "bob", "bob@acme.test", "Bob", true, model.PlatformRoleUser)
		require.NoError(t, store.SaveUser(ctx, bob))
		bobMembership := model.NewMembership(bob.ID(), f.org.ID())
		require.NoError(t, store.AddMember(ctx, bobMembership))

		team := model.NewTeam(f.org.ID(), "legal", "Legal", "")
		require.NoError(t, store.CreateTeam(ctx, team))
		for _, m := range []struct {
			membership model.MembershipID
			user       model.UserID
		}{{f.membership.ID(), f.user.ID()}, {bobMembership.ID(), bob.ID()}} {
			require.NoError(t, store.SetTeamMember(ctx, team.ID(), m.membership, []model.RoleID{f.role.ID()}))
			require.NoError(t, store.SetQuota(ctx, model.NewQuota(model.QuotaScopeTeamMember, model.TeamMemberScopeID(team.ID(), m.user), "EUR", nil, ptrTo(int64(10)), nil)))
		}

		cursor, err := store.CaptureEventCursor(ctx)
		require.NoError(t, err)
		require.NoError(t, store.RemoveTeamMember(ctx, team.ID(), f.membership.ID()))

		members, err := store.ListTeamMembers(ctx, team.ID())
		require.NoError(t, err)
		require.Len(t, members, 1)
		require.Equal(t, bob.ID(), members[0].UserID())
		require.Equal(t, []model.RoleID{f.role.ID()}, members[0].RoleIDs())
		var roles int64
		require.NoError(t, db.Table("team_member_roles").Where("team_id = ? AND membership_id = ?", string(team.ID()), string(f.membership.ID())).Count(&roles).Error)
		require.Zero(t, roles, "the team roles go with the member")
		_, err = store.GetQuota(ctx, model.QuotaScopeTeamMember, model.TeamMemberScopeID(team.ID(), f.user.ID()))
		require.ErrorIs(t, err, port.ErrNotFound, "the member budget goes with the member")
		_, err = store.GetQuota(ctx, model.QuotaScopeTeamMember, model.TeamMemberScopeID(team.ID(), bob.ID()))
		require.NoError(t, err, "another member keeps its budget")

		events, _ := feedSince(t, store, cursor)
		require.Contains(t, eventTypes(events), "team.updated.v1")
		require.Contains(t, eventTypes(events), "quota.deleted.v1")

		require.ErrorIs(t, store.RemoveTeamMember(ctx, team.ID(), f.membership.ID()), port.ErrNotFound)
		members, err = store.ListTeamMembers(ctx, team.ID())
		require.NoError(t, err)
		require.Len(t, members, 1, "a second removal changes nothing")
	})
}

func TestDeleteTeam(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := context.Background()
		f := newTeamFixture(t, store)
		team := model.NewTeam(f.org.ID(), "legal", "Legal", "")
		require.NoError(t, store.CreateTeam(ctx, team))
		require.NoError(t, store.SetTeamMember(ctx, team.ID(), f.membership.ID(), []model.RoleID{f.role.ID()}))
		require.NoError(t, store.SetQuota(ctx, model.NewQuota(model.QuotaScopeTeam, string(team.ID()), "EUR", nil, ptrTo(int64(100)), nil)))
		require.NoError(t, store.SetQuota(ctx, model.NewQuota(model.QuotaScopeTeamMember, model.TeamMemberScopeID(team.ID(), f.user.ID()), "EUR", nil, ptrTo(int64(10)), nil)))

		// Deleting the role removes it from the team members.
		require.NoError(t, store.DeleteRole(ctx, f.role.ID()))
		members, err := store.ListTeamMembers(ctx, team.ID())
		require.NoError(t, err)
		require.Empty(t, members[0].RoleIDs())

		cursor, err := store.CaptureEventCursor(ctx)
		require.NoError(t, err)
		require.NoError(t, store.DeleteTeam(ctx, team.ID()))
		_, err = store.GetTeamByID(ctx, team.ID())
		require.ErrorIs(t, err, port.ErrNotFound)
		for _, scope := range []struct {
			scope model.QuotaScope
			id    string
		}{{model.QuotaScopeTeam, string(team.ID())}, {model.QuotaScopeTeamMember, model.TeamMemberScopeID(team.ID(), f.user.ID())}} {
			_, err = store.GetQuota(ctx, scope.scope, scope.id)
			require.ErrorIs(t, err, port.ErrNotFound, scope.scope)
		}
		events, _ := feedSince(t, store, cursor)
		require.Equal(t, []string{"quota.deleted.v1", "quota.deleted.v1", "team.deleted.v1"}, eventTypes(events))

		// Deleting the organization removes its teams.
		other := model.NewTeam(f.org.ID(), "rd", "R&D", "")
		require.NoError(t, store.CreateTeam(ctx, other))
		require.NoError(t, store.DeleteOrg(ctx, f.org.ID()))
		_, err = store.GetTeamByID(ctx, other.ID())
		require.ErrorIs(t, err, port.ErrNotFound)
	})
}

// A team can change its slug, but not to one another team of the
// organization holds.
func TestRenameTeam(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := context.Background()
		f := newTeamFixture(t, store)
		legal := model.NewTeam(f.org.ID(), "legal", "Legal", "")
		require.NoError(t, store.CreateTeam(ctx, legal))
		require.NoError(t, store.CreateTeam(ctx, model.NewTeam(f.org.ID(), "rd", "R&D", "")))

		renamed := model.CopyTeam(legal)
		renamed.SetSlug("juridique")
		require.NoError(t, store.SaveTeam(ctx, renamed))
		got, err := store.GetTeamBySlug(ctx, f.org.ID(), "juridique")
		require.NoError(t, err)
		require.Equal(t, legal.ID(), got.ID())
		_, err = store.GetTeamBySlug(ctx, f.org.ID(), "legal")
		require.ErrorIs(t, err, port.ErrNotFound)

		taken := model.CopyTeam(got)
		taken.SetSlug("rd")
		require.ErrorIs(t, store.SaveTeam(ctx, taken), port.ErrAlreadyExists)
		got, err = store.GetTeamByID(ctx, legal.ID())
		require.NoError(t, err)
		require.Equal(t, "juridique", got.Slug())
	})
}

// A middleware attached to no team runs for the whole organization: deleting
// the only team it is attached to would widen it silently.
func TestDeleteTeamHoldingAMiddleware(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := context.Background()
		f := newTeamFixture(t, store)
		mw := model.NewMiddleware(f.org.ID(), "legal-prompt", "")
		require.NoError(t, store.CreateMiddleware(ctx, mw))
		legal := model.NewTeam(f.org.ID(), "legal", "Legal", "")
		legal.SetMiddlewareIDs([]model.MiddlewareID{mw.ID(), "deleted-since"})
		require.NoError(t, store.CreateTeam(ctx, legal))

		require.ErrorIs(t, store.DeleteTeam(ctx, legal.ID()), port.ErrTeamHasMiddlewares)
		require.ErrorIs(t, store.DeleteTeam(ctx, legal.ID()), port.ErrNotAllowed)

		// Nor can the team drop it. A middleware deleted since can go.
		dropped := model.CopyTeam(legal)
		dropped.SetMiddlewareIDs(nil)
		require.ErrorIs(t, store.SaveTeam(ctx, dropped), port.ErrTeamHasMiddlewares)
		kept := model.CopyTeam(legal)
		kept.SetMiddlewareIDs([]model.MiddlewareID{mw.ID()})
		require.NoError(t, store.SaveTeam(ctx, kept))
		got, err := store.GetTeamByID(ctx, legal.ID())
		require.NoError(t, err)
		require.Equal(t, []model.MiddlewareID{mw.ID()}, got.MiddlewareIDs())

		// Still attached to another team, it stays a team middleware.
		rd := model.NewTeam(f.org.ID(), "rd", "R&D", "")
		rd.SetMiddlewareIDs([]model.MiddlewareID{mw.ID()})
		require.NoError(t, store.CreateTeam(ctx, rd))
		require.NoError(t, store.SaveTeam(ctx, dropped))
		rdDropped := model.CopyTeam(rd)
		rdDropped.SetMiddlewareIDs(nil)
		require.ErrorIs(t, store.SaveTeam(ctx, rdDropped), port.ErrTeamHasMiddlewares, "rd holds it alone now")
		require.NoError(t, store.DeleteTeam(ctx, legal.ID()))
	})
}

func TestTeamUsageCounters(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := context.Background()
		f := newTeamFixture(t, store)
		team := model.NewTeam(f.org.ID(), "legal", "Legal", "")
		require.NoError(t, store.CreateTeam(ctx, team))

		record := model.NewUsageRecord(f.user.ID(), "", f.org.ID(), team.ID(), "p", "m", "acme/gpt", "", 10, 0, 10, 250, "EUR", model.CostSourceComputed, "")
		require.NoError(t, store.RecordUsage(ctx, record))
		without := model.NewUsageRecord(f.user.ID(), "", f.org.ID(), "", "p", "m", "acme/gpt", "", 10, 0, 10, 100, "EUR", model.CostSourceComputed, "")
		require.NoError(t, store.RecordUsage(ctx, without))

		since := model.StartOfDay(time.Now())
		for _, c := range []struct {
			scope model.QuotaScope
			id    string
			want  int64
		}{
			{model.QuotaScopeOrg, string(f.org.ID()), 350},
			{model.QuotaScopeUser, string(f.user.ID()), 350},
			{model.QuotaScopeTeam, string(team.ID()), 250},
			{model.QuotaScopeTeamMember, model.TeamMemberScopeID(team.ID(), f.user.ID()), 250},
		} {
			got, err := store.SumQuotaCostSince(ctx, c.scope, c.id, f.org.ID(), since)
			require.NoError(t, err)
			require.Equal(t, c.want, got, c.scope)
		}

		teamID := team.ID()
		records, err := store.QueryUsage(ctx, port.UsageFilter{TeamID: &teamID})
		require.NoError(t, err)
		require.Len(t, records, 1)
		require.Equal(t, team.ID(), records[0].TeamID())
	})
}

// The teams migration turns the model grants of the custom roles into teams,
// so that nobody loses access.
func TestMigrateTeamsConvertsRoleGrants(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := context.Background()
		store := newSeededStore(t, db)
		f := newTeamFixture(t, store)
		require.NoError(t, db.AutoMigrate(&xologorm.LegacyRoleModel{}))

		analyst := model.NewRole(f.org.ID(), "Analyste juridique", "")
		analyst.SetPermissions([]string{string(rbac.PermUsageRead)})
		require.NoError(t, store.CreateRole(ctx, analyst))
		require.NoError(t, store.SetMembershipRoles(ctx, f.membership.ID(), []model.RoleID{analyst.ID()}))
		other := model.NewRole(f.org.ID(), "Lecteur", "")
		require.NoError(t, store.CreateRole(ctx, other))
		for _, g := range []xologorm.LegacyRoleModel{
			{RoleID: string(analyst.ID()), ModelID: "m1", ModelKind: rbac.ModelKindLLM},
			{RoleID: string(analyst.ID()), ModelID: "v1", ModelKind: rbac.ModelKindVirtual},
			{RoleID: string(other.ID()), ModelID: "m2", ModelKind: rbac.ModelKindLLM},
		} {
			require.NoError(t, db.Create(&g).Error)
		}
		single := model.NewApplication(f.org.ID(), "Single", "", true)
		require.NoError(t, store.CreateApplication(ctx, single))
		require.NoError(t, store.SetApplicationRoles(ctx, single.ID(), []model.RoleID{analyst.ID()}))
		double := model.NewApplication(f.org.ID(), "Double", "", true)
		require.NoError(t, store.CreateApplication(ctx, double))
		require.NoError(t, store.SetApplicationRoles(ctx, double.ID(), []model.RoleID{analyst.ID(), other.ID()}))

		// Grants and an application role left by a role deleted since.
		deleted := string(model.NewRoleID())
		require.NoError(t, db.Create(&xologorm.LegacyRoleModel{RoleID: deleted, ModelID: "m3", ModelKind: rbac.ModelKindLLM}).Error)
		orphan := model.NewApplication(f.org.ID(), "Orphan", "", true)
		require.NoError(t, store.CreateApplication(ctx, orphan))
		require.NoError(t, db.Create(&xologorm.ApplicationRole{ApplicationID: string(orphan.ID()), RoleID: deleted}).Error)

		cursor, err := store.CaptureEventCursor(ctx)
		require.NoError(t, err)
		require.NoError(t, db.Transaction(func(tx *gormpkg.DB) error { return xologorm.MigrateTeams(tx) }))
		require.False(t, db.Migrator().HasTable("role_models"))

		teams, err := store.ListOrgTeams(ctx, f.org.ID())
		require.NoError(t, err)
		bySlug := map[string]model.Team{}
		for _, team := range teams {
			bySlug[team.Slug()] = team
		}
		require.Len(t, bySlug, 3)
		legal := bySlug["analyste-juridique"]
		require.NotNil(t, legal)
		require.ElementsMatch(t, []model.ModelGrant{{ModelID: "m1", Kind: rbac.ModelKindLLM}, {ModelID: "v1", Kind: rbac.ModelKindVirtual}}, legal.ModelGrants())
		members, err := store.ListTeamMembers(ctx, legal.ID())
		require.NoError(t, err)
		require.Len(t, members, 1, "the members holding the role join the team")
		require.NotNil(t, bySlug["lecteur"])
		joint := bySlug["application-double"]
		require.NotNil(t, joint, "an application holding several such roles gets a team of its own")
		require.Len(t, joint.ModelGrants(), 3)

		got, err := store.GetApplication(ctx, single.ID())
		require.NoError(t, err)
		require.Equal(t, legal.ID(), got.TeamID())
		got, err = store.GetApplication(ctx, double.ID())
		require.NoError(t, err)
		require.Equal(t, joint.ID(), got.TeamID())
		got, err = store.GetApplication(ctx, orphan.ID())
		require.NoError(t, err)
		require.Empty(t, got.TeamID(), "a deleted role gives no team")
		for _, team := range teams {
			for _, g := range team.ModelGrants() {
				require.NotEqual(t, "m3", g.ModelID, "the grants of a deleted role are dropped")
			}
		}

		perms, err := store.ResolveEffectivePermissions(ctx, f.user.ID(), f.org.ID())
		require.NoError(t, err)
		require.True(t, perms.CanUseModel(string(legal.ID()), "m1", rbac.ModelKindLLM), "nobody loses access")

		var admin model.Role
		roles, err := store.ListOrgRoles(ctx, f.org.ID())
		require.NoError(t, err)
		for _, role := range roles {
			if role.BuiltinKind() == model.BuiltinKindAdmin {
				admin = role
			}
		}
		require.Contains(t, admin.Permissions(), string(rbac.PermTeamsWrite), "the builtin admin gains the teams permissions")

		events, _ := feedSince(t, store, cursor)
		types := eventTypes(events)
		require.Contains(t, types, "team.created.v1")
		require.Contains(t, types, "application.updated.v1")
		require.NotContains(t, types, "custom_role.updated.v1", "a projection that does not change is not published again")
	})
}

// Two teams sharing a middleware deleted at once: the second deletion sees the
// first one and is refused, instead of leaving the middleware running for the
// whole organization. SQLite serializes writers, so only PostgreSQL can run
// both checks at once.
func TestDeleteTeamWaitsForAConcurrentDetach(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		if db.Dialector.Name() != "postgres" {
			t.Skip("SQLite serializes writers")
		}
		ctx := context.Background()
		store := newSeededStore(t, db)
		f := newTeamFixture(t, store)
		mw := model.NewMiddleware(f.org.ID(), "legal-prompt", "")
		require.NoError(t, store.CreateMiddleware(ctx, mw))
		legal := model.NewTeam(f.org.ID(), "legal", "Legal", "")
		legal.SetMiddlewareIDs([]model.MiddlewareID{mw.ID()})
		require.NoError(t, store.CreateTeam(ctx, legal))
		rd := model.NewTeam(f.org.ID(), "rd", "R&D", "")
		rd.SetMiddlewareIDs([]model.MiddlewareID{mw.ID()})
		require.NoError(t, store.CreateTeam(ctx, rd))

		// Another writer detaches the middleware from rd, holding its lock.
		other := db.Begin()
		require.NoError(t, other.Error)
		t.Cleanup(func() { other.Rollback() })
		var locked xologorm.Middleware
		require.NoError(t, other.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id = ?", string(mw.ID())).Error)
		require.NoError(t, other.Where("team_id = ?", string(rd.ID())).Delete(&xologorm.TeamMiddleware{}).Error)

		done := make(chan error, 1)
		go func() { done <- store.DeleteTeam(ctx, legal.ID()) }()
		select {
		case err := <-done:
			t.Fatalf("the deletion did not wait for the detach: %v", err)
		case <-time.After(300 * time.Millisecond):
		}

		require.NoError(t, other.Commit().Error)
		require.ErrorIs(t, <-done, port.ErrTeamHasMiddlewares)
	})
}
