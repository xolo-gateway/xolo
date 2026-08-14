package gorm_test

import (
	"context"
	"testing"

	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
)

func TestRoleStore_BuiltinRolesAndResolution(t *testing.T) {
	eachBackend(t, scenarioRoleStore_BuiltinRolesAndResolution)
}

func scenarioRoleStore_BuiltinRolesAndResolution(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	// EnsureBuiltinRoles is idempotent.
	for range 2 {
		if err := store.EnsureBuiltinRoles(ctx, org.ID()); err != nil {
			t.Fatalf("EnsureBuiltinRoles: %v", err)
		}
	}

	roles, err := store.ListOrgRoles(ctx, org.ID())
	if err != nil {
		t.Fatalf("ListOrgRoles: %v", err)
	}
	if len(roles) != 3 {
		t.Fatalf("expected 3 builtin roles, got %d", len(roles))
	}

	var ownerID, memberID model.RoleID
	for _, r := range roles {
		switch r.BuiltinKind() {
		case model.BuiltinKindOwner:
			ownerID = r.ID()
		case model.BuiltinKindMember:
			memberID = r.ID()
		}
	}
	if ownerID == "" || memberID == "" {
		t.Fatal("missing builtin owner/member role")
	}

	// Member with a member role: usage permission but no admin permission.
	user := model.NewUser(testTenantID, "test", "u1", "u1@example.com", "U1", true, "user")
	user.SetPreferences(model.NewUserPreferences())
	if err := store.SaveUser(ctx, user); err != nil {
		t.Fatalf("SaveUser: %v", err)
	}
	userID := user.ID()
	membership := model.NewMembership(userID, org.ID())
	if err := store.AddMember(ctx, membership); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if err := store.SetMembershipRoles(ctx, membership.ID(), []model.RoleID{memberID}); err != nil {
		t.Fatalf("SetMembershipRoles: %v", err)
	}

	perms, err := store.ResolveEffectivePermissions(ctx, userID, org.ID())
	if err != nil {
		t.Fatalf("ResolveEffectivePermissions: %v", err)
	}
	if perms.IsOwner() {
		t.Error("member should not be owner")
	}
	if !perms.Has(rbac.PermModelUseOrg) {
		t.Error("member should have model:use:org")
	}
	if perms.Has(rbac.PermMembersWrite) {
		t.Error("member should not have members:write")
	}

	// Assigning the owner role grants the owner bypass.
	if err := store.SetMembershipRoles(ctx, membership.ID(), []model.RoleID{ownerID}); err != nil {
		t.Fatalf("SetMembershipRoles owner: %v", err)
	}
	perms, err = store.ResolveEffectivePermissions(ctx, userID, org.ID())
	if err != nil {
		t.Fatalf("ResolveEffectivePermissions owner: %v", err)
	}
	if !perms.IsOwner() {
		t.Error("expected owner bypass")
	}
}

func TestRoleStore_CustomRoleAndModelGrant(t *testing.T) {
	eachBackend(t, scenarioRoleStore_CustomRoleAndModelGrant)
}

func scenarioRoleStore_CustomRoleAndModelGrant(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	role := model.NewRole(org.ID(), "Lecteur", "Accès lecture")
	role.SetPermissions([]string{string(rbac.PermUsageRead), string(rbac.PermMembersWrite)})
	role.SetModelGrants([]model.ModelGrant{{ModelID: "m1", Kind: rbac.ModelKindLLM}})
	if err := store.CreateRole(ctx, role); err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	got, err := store.GetRoleByID(ctx, role.ID())
	if err != nil {
		t.Fatalf("GetRoleByID: %v", err)
	}
	if len(got.Permissions()) != 2 {
		t.Errorf("expected 2 permissions, got %d", len(got.Permissions()))
	}
	if len(got.ModelGrants()) != 1 {
		t.Errorf("expected 1 model grant, got %d", len(got.ModelGrants()))
	}

	// SaveRole fully replaces permissions and grants.
	updated := model.UpdateRole(got, model.WithRolePermissions([]string{string(rbac.PermUsageRead)}), model.WithRoleModelGrants(nil))
	if err := store.SaveRole(ctx, updated); err != nil {
		t.Fatalf("SaveRole: %v", err)
	}
	got, err = store.GetRoleByID(ctx, role.ID())
	if err != nil {
		t.Fatalf("GetRoleByID after save: %v", err)
	}
	if len(got.Permissions()) != 1 || len(got.ModelGrants()) != 0 {
		t.Errorf("expected 1 perm / 0 grants after save, got %d / %d", len(got.Permissions()), len(got.ModelGrants()))
	}

	// Custom role is deletable.
	if err := store.DeleteRole(ctx, role.ID()); err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}
}

// Applications hold roles directly instead of going through a membership, so
// permission resolution has to reach them through the application join table.
func TestRoleStore_ApplicationRoleResolution(t *testing.T) {
	eachBackend(t, scenarioRoleStore_ApplicationRoleResolution)
}

func scenarioRoleStore_ApplicationRoleResolution(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	if err := store.EnsureBuiltinRoles(ctx, org.ID()); err != nil {
		t.Fatalf("EnsureBuiltinRoles: %v", err)
	}

	roles, err := store.ListOrgRoles(ctx, org.ID())
	if err != nil {
		t.Fatalf("ListOrgRoles: %v", err)
	}
	var ownerID, memberID model.RoleID
	for _, r := range roles {
		switch r.BuiltinKind() {
		case model.BuiltinKindOwner:
			ownerID = r.ID()
		case model.BuiltinKindMember:
			memberID = r.ID()
		}
	}

	app := model.NewApplication(org.ID(), "CI", "Pipeline", true)
	if err := store.CreateApplication(ctx, app); err != nil {
		t.Fatalf("CreateApplication: %v", err)
	}

	// No role assigned: an empty set, which is what rejects proxy calls.
	perms, err := store.ResolveApplicationPermissions(ctx, app.ID(), org.ID())
	if err != nil {
		t.Fatalf("ResolveApplicationPermissions: %v", err)
	}
	if perms.IsOwner() || perms.Has(rbac.PermModelUseOrg) {
		t.Error("application without roles should hold no permission")
	}

	if err := store.SetApplicationRoles(ctx, app.ID(), []model.RoleID{memberID}); err != nil {
		t.Fatalf("SetApplicationRoles: %v", err)
	}

	assigned, err := store.ListApplicationRoles(ctx, app.ID())
	if err != nil {
		t.Fatalf("ListApplicationRoles: %v", err)
	}
	if len(assigned) != 1 || assigned[0].ID() != memberID {
		t.Fatalf("expected the member role to be assigned, got %d role(s)", len(assigned))
	}

	perms, err = store.ResolveApplicationPermissions(ctx, app.ID(), org.ID())
	if err != nil {
		t.Fatalf("ResolveApplicationPermissions: %v", err)
	}
	if !perms.Has(rbac.PermModelUseOrg) {
		t.Error("application with the member role should have model:use:org")
	}
	if perms.Has(rbac.PermMembersWrite) {
		t.Error("application with the member role should not have members:write")
	}

	// A token scoped to another org must not resolve to the app's permissions.
	other := model.NewOrganization(testTenantID, "other", "Other", "")
	if err := store.CreateOrg(ctx, other); err != nil {
		t.Fatalf("CreateOrg other: %v", err)
	}
	perms, err = store.ResolveApplicationPermissions(ctx, app.ID(), other.ID())
	if err != nil {
		t.Fatalf("ResolveApplicationPermissions other org: %v", err)
	}
	if perms.IsOwner() || perms.Has(rbac.PermModelUseOrg) {
		t.Error("application must hold no permission outside its own org")
	}

	// The owner role grants the same bypass as it does for members.
	if err := store.SetApplicationRoles(ctx, app.ID(), []model.RoleID{ownerID}); err != nil {
		t.Fatalf("SetApplicationRoles owner: %v", err)
	}
	perms, err = store.ResolveApplicationPermissions(ctx, app.ID(), org.ID())
	if err != nil {
		t.Fatalf("ResolveApplicationPermissions owner: %v", err)
	}
	if !perms.IsOwner() {
		t.Error("expected owner bypass")
	}

	// A deactivated application keeps its roles but resolves to nothing.
	if err := store.UpdateApplication(ctx, model.UpdateApplication(app, model.WithApplicationActive(false))); err != nil {
		t.Fatalf("UpdateApplication: %v", err)
	}
	perms, err = store.ResolveApplicationPermissions(ctx, app.ID(), org.ID())
	if err != nil {
		t.Fatalf("ResolveApplicationPermissions inactive: %v", err)
	}
	if perms.IsOwner() {
		t.Error("deactivated application should hold no permission")
	}
}

// Deleting a role must drop its application assignments, not just its
// membership ones.
func TestRoleStore_DeleteRoleClearsApplicationAssignment(t *testing.T) {
	eachBackend(t, scenarioRoleStore_DeleteRoleClearsApplicationAssignment)
}

func scenarioRoleStore_DeleteRoleClearsApplicationAssignment(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	role := model.NewRole(org.ID(), "Lecteur", "")
	role.SetPermissions([]string{string(rbac.PermUsageRead)})
	if err := store.CreateRole(ctx, role); err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	app := model.NewApplication(org.ID(), "CI", "", true)
	if err := store.CreateApplication(ctx, app); err != nil {
		t.Fatalf("CreateApplication: %v", err)
	}
	if err := store.SetApplicationRoles(ctx, app.ID(), []model.RoleID{role.ID()}); err != nil {
		t.Fatalf("SetApplicationRoles: %v", err)
	}

	if err := store.DeleteRole(ctx, role.ID()); err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}

	assigned, err := store.ListApplicationRoles(ctx, app.ID())
	if err != nil {
		t.Fatalf("ListApplicationRoles: %v", err)
	}
	if len(assigned) != 0 {
		t.Errorf("expected no assignment left after role deletion, got %d", len(assigned))
	}
}

func TestRoleStore_BuiltinRoleNotDeletable(t *testing.T) {
	eachBackend(t, scenarioRoleStore_BuiltinRoleNotDeletable)
}

func scenarioRoleStore_BuiltinRoleNotDeletable(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	if err := store.EnsureBuiltinRoles(ctx, org.ID()); err != nil {
		t.Fatalf("EnsureBuiltinRoles: %v", err)
	}
	roles, err := store.ListOrgRoles(ctx, org.ID())
	if err != nil {
		t.Fatalf("ListOrgRoles: %v", err)
	}
	if err := store.DeleteRole(ctx, roles[0].ID()); err == nil {
		t.Fatal("expected error deleting builtin role")
	}
}
