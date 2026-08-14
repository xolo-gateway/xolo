package service_test

import (
	"context"
	"slices"
	"testing"

	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/ncruces/go-sqlite3/gormlite"
	"github.com/pkg/errors"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/core/service"
	gormpkg "gorm.io/gorm"
)

func newTestService(t *testing.T) (*service.ProvisioningService, *xologorm.Store, model.TenantID) {
	t.Helper()

	db, err := gormpkg.Open(gormlite.Open(":memory:"), &gormpkg.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	store := xologorm.NewStore(db)

	// The schema migration creates the default tenant; every fixture of this
	// package hangs from it, so the tests exercise organizations rather than
	// tenancy.
	tenant, err := store.GetTenantBySlug(context.Background(), model.DefaultTenantSlug)
	if err != nil {
		t.Fatalf("get default tenant: %v", err)
	}

	svc := service.NewProvisioningService(store, store, store, store)

	return svc, store, tenant.ID()
}

func strPtr(v string) *string { return &v }

func ownerParams() *service.UserIdentityParams {
	return &service.UserIdentityParams{
		Provider:    "openid-connect",
		Subject:     "sub-owner",
		Email:       strPtr("owner@acme.tld"),
		DisplayName: strPtr("Acme Owner"),
	}
}

func createOrganization(t *testing.T, svc *service.ProvisioningService, tenantID model.TenantID, slug string, owner *service.UserIdentityParams) *service.CreateOrganizationResult {
	t.Helper()

	result, err := svc.CreateOrganization(context.Background(), service.CreateOrganizationParams{
		TenantID: tenantID,
		Slug:     slug,
		Name:     slug,
		Owner:    owner,
	})
	if err != nil {
		t.Fatalf("create organization %q: %v", slug, err)
	}

	return result
}

func TestCreateOrganization(t *testing.T) {
	ctx := context.Background()

	t.Run("provisions organization, builtin roles and initial owner", func(t *testing.T) {
		svc, store, testTenantID := newTestService(t)

		result, err := svc.CreateOrganization(ctx, service.CreateOrganizationParams{
			TenantID:    testTenantID,
			Slug:        "acme",
			Name:        "Acme",
			Description: "Acme Inc.",
			Currency:    "EUR",
			Owner:       ownerParams(),
		})
		if err != nil {
			t.Fatalf("create organization: %v", err)
		}

		org, err := store.GetOrgBySlug(ctx, testTenantID, "acme")
		if err != nil {
			t.Fatalf("get org by slug: %v", err)
		}
		if org.ID() != result.Org.ID() {
			t.Errorf("persisted org id: got %q, want %q", org.ID(), result.Org.ID())
		}
		if org.Name() != "Acme" {
			t.Errorf("org name: got %q, want %q", org.Name(), "Acme")
		}
		if org.Currency() != "EUR" {
			t.Errorf("org currency: got %q, want %q", org.Currency(), "EUR")
		}

		roles, err := store.ListOrgRoles(ctx, org.ID())
		if err != nil {
			t.Fatalf("list org roles: %v", err)
		}

		kinds := map[string]bool{}
		for _, role := range roles {
			if role.Builtin() {
				kinds[role.BuiltinKind()] = true
			}
		}
		for _, kind := range []string{model.BuiltinKindOwner, model.BuiltinKindAdmin, model.BuiltinKindMember} {
			if !kinds[kind] {
				t.Errorf("missing builtin role %q", kind)
			}
		}

		if !result.OwnerCreated {
			t.Error("owner should have been reported as created")
		}
		if result.Owner == nil {
			t.Fatal("owner should not be nil")
		}
		if result.Owner.Email() != "owner@acme.tld" {
			t.Errorf("owner email: got %q", result.Owner.Email())
		}

		if result.OwnerMembership == nil {
			t.Fatal("owner membership should not be nil")
		}

		membership, err := store.GetUserOrgMembership(ctx, result.Owner.ID(), org.ID())
		if err != nil {
			t.Fatalf("get owner membership: %v", err)
		}

		hasOwnerRole := false
		for _, role := range membership.Roles() {
			if role.BuiltinKind() == model.BuiltinKindOwner {
				hasOwnerRole = true
			}
		}
		if !hasOwnerRole {
			t.Error("owner membership should hold the builtin owner role")
		}
	})

	t.Run("organization owner never becomes a platform admin", func(t *testing.T) {
		svc, store, testTenantID := newTestService(t)

		result := createOrganization(t, svc, testTenantID, "acme", ownerParams())

		user, err := store.GetUserByID(ctx, result.Owner.ID())
		if err != nil {
			t.Fatalf("get user: %v", err)
		}

		if slices.Contains(user.Roles(), model.PlatformRoleAdmin) {
			t.Fatalf("organization owner must not hold the %q platform role, got %v", model.PlatformRoleAdmin, user.Roles())
		}
		if len(user.Roles()) != 1 || user.Roles()[0] != model.PlatformRoleUser {
			t.Errorf("platform roles: got %v, want [%q]", user.Roles(), model.PlatformRoleUser)
		}
	})

	t.Run("existing user keeps its platform roles", func(t *testing.T) {
		svc, store, testTenantID := newTestService(t)

		existing, err := store.FindOrCreateUser(ctx, testTenantID, "openid-connect", "sub-owner")
		if err != nil {
			t.Fatalf("find or create user: %v", err)
		}

		admin := model.CopyUser(existing)
		admin.SetRoles(model.PlatformRoleUser, model.PlatformRoleAdmin)
		admin.SetEmail("admin@xolo.tld")
		if err := store.SaveUser(ctx, admin); err != nil {
			t.Fatalf("save user: %v", err)
		}

		result, err := svc.CreateOrganization(ctx, service.CreateOrganizationParams{
			TenantID: testTenantID,
			Slug:     "acme",
			Name:     "Acme",
			Owner:    &service.UserIdentityParams{Provider: "openid-connect", Subject: "sub-owner"},
		})
		if err != nil {
			t.Fatalf("create organization: %v", err)
		}

		if result.OwnerCreated {
			t.Error("owner should have been reported as reused")
		}
		if result.Owner.ID() != existing.ID() {
			t.Errorf("owner id: got %q, want %q", result.Owner.ID(), existing.ID())
		}

		reloaded, err := store.GetUserByID(ctx, existing.ID())
		if err != nil {
			t.Fatalf("get user: %v", err)
		}
		if !slices.Contains(reloaded.Roles(), model.PlatformRoleAdmin) {
			t.Errorf("existing platform roles should be preserved, got %v", reloaded.Roles())
		}
	})

	t.Run("duplicate slug is refused", func(t *testing.T) {
		svc, _, testTenantID := newTestService(t)

		createOrganization(t, svc, testTenantID, "acme", nil)

		_, err := svc.CreateOrganization(ctx, service.CreateOrganizationParams{TenantID: testTenantID, Slug: "acme", Name: "Acme again"})
		if !errors.Is(err, port.ErrAlreadyExists) {
			t.Errorf("error: got %v, want %v", err, port.ErrAlreadyExists)
		}
	})

	t.Run("invalid input is refused", func(t *testing.T) {
		svc, _, testTenantID := newTestService(t)

		for name, params := range map[string]service.CreateOrganizationParams{
			"malformed slug":   {Slug: "Acme Corp", Name: "Acme"},
			"empty slug":       {Slug: "", Name: "Acme"},
			"trailing dash":    {Slug: "acme-", Name: "Acme"},
			"empty name":       {Slug: "acme", Name: "   "},
			"bad currency":     {Slug: "acme", Name: "Acme", Currency: "XXX"},
			"owner no subject": {Slug: "acme", Name: "Acme", Owner: &service.UserIdentityParams{Provider: "openid-connect"}},
		} {
			t.Run(name, func(t *testing.T) {
				params.TenantID = testTenantID
				if _, err := svc.CreateOrganization(ctx, params); !errors.Is(err, port.ErrInvalid) {
					t.Errorf("error: got %v, want %v", err, port.ErrInvalid)
				}
			})
		}
	})

	t.Run("invalid owner leaves no organization behind", func(t *testing.T) {
		svc, store, testTenantID := newTestService(t)

		_, err := svc.CreateOrganization(ctx, service.CreateOrganizationParams{
			TenantID: testTenantID,
			Slug:     "acme",
			Name:     "Acme",
			Owner:    &service.UserIdentityParams{Provider: "openid-connect", Subject: "sub-1", Email: strPtr("dup@acme.tld")},
		})
		if err != nil {
			t.Fatalf("create first organization: %v", err)
		}

		// The email is already taken by the first owner: provisioning must fail
		// and the half-created organization must be rolled back.
		_, err = svc.CreateOrganization(ctx, service.CreateOrganizationParams{
			TenantID: testTenantID,
			Slug:     "other",
			Name:     "Other",
			Owner:    &service.UserIdentityParams{Provider: "openid-connect", Subject: "sub-2", Email: strPtr("dup@acme.tld")},
		})
		if !errors.Is(err, port.ErrAlreadyExists) {
			t.Fatalf("error: got %v, want %v", err, port.ErrAlreadyExists)
		}

		if _, err := store.GetOrgBySlug(ctx, testTenantID, "other"); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("organization should have been rolled back, got %v", err)
		}
		if _, err := store.GetUserByIdentity(ctx, testTenantID, "openid-connect", "sub-2"); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("user should have been rolled back, got %v", err)
		}
	})
}

func TestOrganizationLifecycle(t *testing.T) {
	ctx := context.Background()

	t.Run("update applies only the provided fields", func(t *testing.T) {
		svc, _, testTenantID := newTestService(t)

		org := createOrganization(t, svc, testTenantID, "acme", nil)

		active := false
		updated, err := svc.UpdateOrganization(ctx, testTenantID, org.Org.ID(), service.UpdateOrganizationParams{
			Name:   strPtr("Acme Corporation"),
			Active: &active,
		})
		if err != nil {
			t.Fatalf("update organization: %v", err)
		}

		if updated.Name() != "Acme Corporation" {
			t.Errorf("name: got %q", updated.Name())
		}
		if updated.Active() {
			t.Error("organization should be inactive")
		}
		if updated.Slug() != "acme" {
			t.Errorf("slug must be immutable, got %q", updated.Slug())
		}
	})

	t.Run("delete removes the organization and its dependents", func(t *testing.T) {
		svc, store, testTenantID := newTestService(t)

		org := createOrganization(t, svc, testTenantID, "acme", ownerParams())

		if err := svc.DeleteOrganization(ctx, testTenantID, org.Org.ID()); err != nil {
			t.Fatalf("delete organization: %v", err)
		}

		if _, err := store.GetOrgByID(ctx, org.Org.ID()); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("organization: got %v, want %v", err, port.ErrNotFound)
		}
		if _, err := store.GetMembership(ctx, org.OwnerMembership.ID()); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("membership: got %v, want %v", err, port.ErrNotFound)
		}

		roles, err := store.ListOrgRoles(ctx, org.Org.ID())
		if err != nil {
			t.Fatalf("list org roles: %v", err)
		}
		if len(roles) != 0 {
			t.Errorf("roles: got %d, want 0", len(roles))
		}
	})

	t.Run("unknown organization is reported as not found", func(t *testing.T) {
		svc, _, testTenantID := newTestService(t)

		if _, err := svc.GetOrganization(ctx, testTenantID, model.OrgID("does-not-exist")); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("get: got %v, want %v", err, port.ErrNotFound)
		}
		if _, err := svc.UpdateOrganization(ctx, testTenantID, model.OrgID("does-not-exist"), service.UpdateOrganizationParams{}); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("update: got %v, want %v", err, port.ErrNotFound)
		}
		if err := svc.DeleteOrganization(ctx, testTenantID, model.OrgID("does-not-exist")); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("delete: got %v, want %v", err, port.ErrNotFound)
		}
	})
}

func TestMembers(t *testing.T) {
	ctx := context.Background()

	memberParams := func(subject string) *service.UserIdentityParams {
		return &service.UserIdentityParams{
			Provider:    "openid-connect",
			Subject:     subject,
			Email:       strPtr(subject + "@acme.tld"),
			DisplayName: strPtr(subject),
		}
	}

	t.Run("adds a member with builtin roles", func(t *testing.T) {
		svc, _, testTenantID := newTestService(t)

		org := createOrganization(t, svc, testTenantID, "acme", ownerParams())

		membership, err := svc.AddMember(ctx, org.Org.ID(), service.AddMemberParams{
			User:         memberParams("sub-member"),
			BuiltinRoles: []string{model.BuiltinKindMember},
		})
		if err != nil {
			t.Fatalf("add member: %v", err)
		}

		if len(membership.Roles()) != 1 || membership.Roles()[0].BuiltinKind() != model.BuiltinKindMember {
			t.Errorf("member roles: got %v", membership.Roles())
		}
	})

	t.Run("refuses a duplicate membership", func(t *testing.T) {
		svc, _, testTenantID := newTestService(t)

		org := createOrganization(t, svc, testTenantID, "acme", ownerParams())

		if _, err := svc.AddMember(ctx, org.Org.ID(), service.AddMemberParams{User: memberParams("sub-member")}); err != nil {
			t.Fatalf("add member: %v", err)
		}

		_, err := svc.AddMember(ctx, org.Org.ID(), service.AddMemberParams{User: memberParams("sub-member")})
		if !errors.Is(err, port.ErrAlreadyExists) {
			t.Errorf("error: got %v, want %v", err, port.ErrAlreadyExists)
		}
	})

	t.Run("refuses an unknown organization or user", func(t *testing.T) {
		svc, _, testTenantID := newTestService(t)

		org := createOrganization(t, svc, testTenantID, "acme", ownerParams())

		if _, err := svc.AddMember(ctx, model.OrgID("nope"), service.AddMemberParams{User: memberParams("sub-member")}); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("unknown organization: got %v, want %v", err, port.ErrNotFound)
		}
		if _, err := svc.AddMember(ctx, org.Org.ID(), service.AddMemberParams{UserID: model.UserID("nope")}); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("unknown user: got %v, want %v", err, port.ErrNotFound)
		}
		if _, err := svc.AddMember(ctx, org.Org.ID(), service.AddMemberParams{}); !errors.Is(err, port.ErrInvalid) {
			t.Errorf("missing identity: got %v, want %v", err, port.ErrInvalid)
		}
	})

	t.Run("replaces member roles", func(t *testing.T) {
		svc, _, testTenantID := newTestService(t)

		org := createOrganization(t, svc, testTenantID, "acme", ownerParams())

		membership, err := svc.AddMember(ctx, org.Org.ID(), service.AddMemberParams{
			User:         memberParams("sub-member"),
			BuiltinRoles: []string{model.BuiltinKindMember},
		})
		if err != nil {
			t.Fatalf("add member: %v", err)
		}

		updated, err := svc.SetMemberRoles(ctx, org.Org.ID(), membership.ID(), nil, []string{model.BuiltinKindAdmin})
		if err != nil {
			t.Fatalf("set member roles: %v", err)
		}

		if len(updated.Roles()) != 1 || updated.Roles()[0].BuiltinKind() != model.BuiltinKindAdmin {
			t.Errorf("roles: got %v", updated.Roles())
		}
	})

	t.Run("refuses a role belonging to another organization", func(t *testing.T) {
		svc, _, testTenantID := newTestService(t)

		acme := createOrganization(t, svc, testTenantID, "acme", ownerParams())
		other := createOrganization(t, svc, testTenantID, "other", nil)

		otherRoles, err := svc.ListRoles(ctx, other.Org.ID())
		if err != nil {
			t.Fatalf("list other roles: %v", err)
		}

		membership, err := svc.AddMember(ctx, acme.Org.ID(), service.AddMemberParams{
			User:         memberParams("sub-member"),
			BuiltinRoles: []string{model.BuiltinKindMember},
		})
		if err != nil {
			t.Fatalf("add member: %v", err)
		}

		_, err = svc.SetMemberRoles(ctx, acme.Org.ID(), membership.ID(), []model.RoleID{otherRoles[0].ID()}, nil)
		if !errors.Is(err, port.ErrInvalid) {
			t.Fatalf("error: got %v, want %v", err, port.ErrInvalid)
		}

		unchanged, err := svc.GetMember(ctx, acme.Org.ID(), membership.ID())
		if err != nil {
			t.Fatalf("get member: %v", err)
		}
		if len(unchanged.Roles()) != 1 || unchanged.Roles()[0].BuiltinKind() != model.BuiltinKindMember {
			t.Errorf("roles must be left untouched, got %v", unchanged.Roles())
		}
	})

	t.Run("refuses to drop the last owner", func(t *testing.T) {
		svc, _, testTenantID := newTestService(t)

		org := createOrganization(t, svc, testTenantID, "acme", ownerParams())
		ownerMembership := org.OwnerMembership

		_, err := svc.SetMemberRoles(ctx, org.Org.ID(), ownerMembership.ID(), nil, []string{model.BuiltinKindMember})
		if !errors.Is(err, port.ErrNotAllowed) {
			t.Errorf("downgrade: got %v, want %v", err, port.ErrNotAllowed)
		}

		if err := svc.RemoveMember(ctx, org.Org.ID(), ownerMembership.ID()); !errors.Is(err, port.ErrNotAllowed) {
			t.Errorf("removal: got %v, want %v", err, port.ErrNotAllowed)
		}
	})

	t.Run("allows dropping an owner when another one remains", func(t *testing.T) {
		svc, _, testTenantID := newTestService(t)

		org := createOrganization(t, svc, testTenantID, "acme", ownerParams())

		if _, err := svc.AddMember(ctx, org.Org.ID(), service.AddMemberParams{
			User:         memberParams("sub-second-owner"),
			BuiltinRoles: []string{model.BuiltinKindOwner},
		}); err != nil {
			t.Fatalf("add second owner: %v", err)
		}

		if err := svc.RemoveMember(ctx, org.Org.ID(), org.OwnerMembership.ID()); err != nil {
			t.Fatalf("remove first owner: %v", err)
		}
	})

	t.Run("hides a membership belonging to another organization", func(t *testing.T) {
		svc, _, testTenantID := newTestService(t)

		acme := createOrganization(t, svc, testTenantID, "acme", ownerParams())
		other := createOrganization(t, svc, testTenantID, "other", nil)

		if _, err := svc.GetMember(ctx, other.Org.ID(), acme.OwnerMembership.ID()); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("get: got %v, want %v", err, port.ErrNotFound)
		}
		if err := svc.RemoveMember(ctx, other.Org.ID(), acme.OwnerMembership.ID()); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("remove: got %v, want %v", err, port.ErrNotFound)
		}
		if _, err := svc.GetMember(ctx, acme.Org.ID(), model.MembershipID("nope")); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("unknown membership: got %v, want %v", err, port.ErrNotFound)
		}
	})
}

func TestRoles(t *testing.T) {
	ctx := context.Background()

	t.Run("creates and updates a custom role", func(t *testing.T) {
		svc, _, testTenantID := newTestService(t)

		org := createOrganization(t, svc, testTenantID, "acme", nil)

		role, err := svc.CreateRole(ctx, org.Org.ID(), service.RoleParams{
			Name:        strPtr("auditor"),
			Description: strPtr("Read-only access"),
			Permissions: []string{string(rbac.PermUsageRead)},
		})
		if err != nil {
			t.Fatalf("create role: %v", err)
		}

		updated, err := svc.UpdateRole(ctx, org.Org.ID(), role.ID(), service.RoleParams{
			Permissions: []string{string(rbac.PermUsageRead), string(rbac.PermMembersRead)},
		})
		if err != nil {
			t.Fatalf("update role: %v", err)
		}
		if len(updated.Permissions()) != 2 {
			t.Errorf("permissions: got %v", updated.Permissions())
		}

		if err := svc.DeleteRole(ctx, org.Org.ID(), role.ID()); err != nil {
			t.Fatalf("delete role: %v", err)
		}
	})

	t.Run("refuses an unknown permission or grant kind", func(t *testing.T) {
		svc, _, testTenantID := newTestService(t)

		org := createOrganization(t, svc, testTenantID, "acme", nil)

		_, err := svc.CreateRole(ctx, org.Org.ID(), service.RoleParams{
			Name:        strPtr("bogus"),
			Permissions: []string{"not:a:permission"},
		})
		if !errors.Is(err, port.ErrInvalid) {
			t.Errorf("permission: got %v, want %v", err, port.ErrInvalid)
		}

		_, err = svc.CreateRole(ctx, org.Org.ID(), service.RoleParams{
			Name:        strPtr("bogus"),
			ModelGrants: []model.ModelGrant{{ModelID: "m1", Kind: "wat"}},
		})
		if !errors.Is(err, port.ErrInvalid) {
			t.Errorf("grant kind: got %v, want %v", err, port.ErrInvalid)
		}
	})

	t.Run("refuses a duplicate role name", func(t *testing.T) {
		svc, _, testTenantID := newTestService(t)

		org := createOrganization(t, svc, testTenantID, "acme", nil)

		if _, err := svc.CreateRole(ctx, org.Org.ID(), service.RoleParams{Name: strPtr("auditor")}); err != nil {
			t.Fatalf("create role: %v", err)
		}

		_, err := svc.CreateRole(ctx, org.Org.ID(), service.RoleParams{Name: strPtr("auditor")})
		if !errors.Is(err, port.ErrAlreadyExists) {
			t.Errorf("error: got %v, want %v", err, port.ErrAlreadyExists)
		}
	})

	t.Run("refuses to modify or delete a builtin role", func(t *testing.T) {
		svc, _, testTenantID := newTestService(t)

		org := createOrganization(t, svc, testTenantID, "acme", nil)

		roles, err := svc.ListRoles(ctx, org.Org.ID())
		if err != nil {
			t.Fatalf("list roles: %v", err)
		}

		var builtin model.Role
		for _, role := range roles {
			if role.BuiltinKind() == model.BuiltinKindOwner {
				builtin = role
			}
		}
		if builtin == nil {
			t.Fatal("no builtin owner role found")
		}

		if _, err := svc.UpdateRole(ctx, org.Org.ID(), builtin.ID(), service.RoleParams{Name: strPtr("hacked")}); !errors.Is(err, port.ErrNotAllowed) {
			t.Errorf("update: got %v, want %v", err, port.ErrNotAllowed)
		}
		if err := svc.DeleteRole(ctx, org.Org.ID(), builtin.ID()); !errors.Is(err, port.ErrNotAllowed) {
			t.Errorf("delete: got %v, want %v", err, port.ErrNotAllowed)
		}
	})

	t.Run("hides a role belonging to another tenant", func(t *testing.T) {
		svc, _, testTenantID := newTestService(t)

		acme := createOrganization(t, svc, testTenantID, "acme", nil)
		other := createOrganization(t, svc, testTenantID, "other", nil)

		otherRoles, err := svc.ListRoles(ctx, other.Org.ID())
		if err != nil {
			t.Fatalf("list roles: %v", err)
		}

		if _, err := svc.GetRole(ctx, acme.Org.ID(), otherRoles[0].ID()); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("get: got %v, want %v", err, port.ErrNotFound)
		}
	})
}

func TestProvisionUser(t *testing.T) {
	ctx := context.Background()

	t.Run("is idempotent on the provider/subject tuple", func(t *testing.T) {
		svc, _, testTenantID := newTestService(t)

		first, created, err := svc.ProvisionUser(ctx, testTenantID, *ownerParams())
		if err != nil {
			t.Fatalf("provision: %v", err)
		}
		if !created {
			t.Error("first call should report a creation")
		}

		second, created, err := svc.ProvisionUser(ctx, testTenantID, *ownerParams())
		if err != nil {
			t.Fatalf("provision again: %v", err)
		}
		if created {
			t.Error("second call should not report a creation")
		}
		if first.ID() != second.ID() {
			t.Errorf("user id: got %q, want %q", second.ID(), first.ID())
		}
	})

	t.Run("reconciles profile fields without touching platform roles", func(t *testing.T) {
		svc, store, testTenantID := newTestService(t)

		user, _, err := svc.ProvisionUser(ctx, testTenantID, *ownerParams())
		if err != nil {
			t.Fatalf("provision: %v", err)
		}

		active := false
		if _, _, err := svc.ProvisionUser(ctx, testTenantID, service.UserIdentityParams{
			Provider:    "openid-connect",
			Subject:     "sub-owner",
			DisplayName: strPtr("Renamed"),
			Active:      &active,
		}); err != nil {
			t.Fatalf("provision again: %v", err)
		}

		reloaded, err := store.GetUserByID(ctx, user.ID())
		if err != nil {
			t.Fatalf("get user: %v", err)
		}
		if reloaded.DisplayName() != "Renamed" {
			t.Errorf("display name: got %q", reloaded.DisplayName())
		}
		if reloaded.Active() {
			t.Error("user should be inactive")
		}
		if len(reloaded.Roles()) != 1 || reloaded.Roles()[0] != model.PlatformRoleUser {
			t.Errorf("platform roles: got %v", reloaded.Roles())
		}
	})

	t.Run("refuses an email reserved for the instance administrators", func(t *testing.T) {
		db, err := gormpkg.Open(gormlite.Open(":memory:"), &gormpkg.Config{})
		if err != nil {
			t.Fatalf("open db: %v", err)
		}

		store := xologorm.NewStore(db)
		svc := service.NewProvisioningService(store, store, store, store,
			service.WithReservedEmails("boss@corp.tld"),
		)

		tenant, err := store.GetTenantBySlug(ctx, model.DefaultTenantSlug)
		if err != nil {
			t.Fatalf("get default tenant: %v", err)
		}
		testTenantID := tenant.ID()

		params := *ownerParams()
		params.Email = strPtr("Boss@Corp.tld")

		if _, _, err := svc.ProvisionUser(ctx, testTenantID, params); !errors.Is(err, port.ErrInvalid) {
			t.Errorf("provision with reserved email: got %v, want port.ErrInvalid", err)
		}

		user, _, err := svc.ProvisionUser(ctx, testTenantID, *ownerParams())
		if err != nil {
			t.Fatalf("provision: %v", err)
		}

		if _, err := svc.UpdateUser(ctx, testTenantID, user.ID(), service.UpdateUserParams{
			Email: strPtr("boss@corp.tld"),
		}); !errors.Is(err, port.ErrInvalid) {
			t.Errorf("update with reserved email: got %v, want port.ErrInvalid", err)
		}
	})
}
