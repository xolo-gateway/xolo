package service_test

import (
	"context"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/pkg/errors"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

func newCommonService(t *testing.T, opts ...service.ProvisioningServiceOptionFunc) (*service.ProvisioningService, *xologorm.Store, model.TenantID) {
	t.Helper()
	_, store, tenantID := newTestService(t)
	opts = append([]service.ProvisioningServiceOptionFunc{service.WithProvisioningTransaction(store)}, opts...)
	return service.NewProvisioningService(store, store, store, store, opts...), store, tenantID
}

func assertErrorIs(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error: got %v, want %v", err, want)
	}
}

func mustNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func active(slug, name string) service.CommonResource {
	return service.CommonResource{Slug: slug, Name: name, Status: model.StatusActive}
}

func TestPutTenant(t *testing.T) {
	ctx := context.Background()

	t.Run("single-tenant instance keeps its default tenant", func(t *testing.T) {
		svc, _, defaultID := newCommonService(t)

		_, err := svc.PutTenant(ctx, model.NewTenantID(), active("acme", "Acme"))
		assertErrorIs(t, err, port.ErrNotAllowed)
		_, err = svc.PutTenant(ctx, defaultID, active("renamed", "Default"))
		assertErrorIs(t, err, port.ErrNotAllowed)
		_, err = svc.PutTenant(ctx, defaultID, service.CommonResource{Slug: model.DefaultTenantSlug, Name: "Default", Status: model.StatusSuspended})
		assertErrorIs(t, err, port.ErrNotAllowed)

		_, err = svc.PutTenant(ctx, defaultID, active(model.DefaultTenantSlug, "Renamed instance"))
		mustNoError(t, err)
		tenant, err := svc.GetTenant(ctx, defaultID)
		mustNoError(t, err)
		if tenant.Name() != "Renamed instance" {
			t.Errorf("name: got %q", tenant.Name())
		}
	})

	t.Run("creates, renames and refuses slug conflicts", func(t *testing.T) {
		svc, _, _ := newCommonService(t, service.WithMultiTenant(true))
		acmeID, globexID := model.NewTenantID(), model.NewTenantID()

		for range 2 {
			_, err := svc.PutTenant(ctx, acmeID, active("ACME ", "Acme"))
			mustNoError(t, err)
		}
		_, err := svc.PutTenant(ctx, globexID, active("globex", "Globex"))
		mustNoError(t, err)
		_, err = svc.PutTenant(ctx, acmeID, active("acme-corp", "Acme Corp"))
		mustNoError(t, err)
		tenant, err := svc.GetTenant(ctx, acmeID)
		mustNoError(t, err)
		if tenant.Slug() != "acme-corp" || tenant.Name() != "Acme Corp" {
			t.Errorf("tenant: got %q/%q", tenant.Slug(), tenant.Name())
		}

		_, err = svc.PutTenant(ctx, globexID, active("acme-corp", "Globex"))
		assertErrorIs(t, err, port.ErrAlreadyExists)
	})

	t.Run("refuses invalid representations", func(t *testing.T) {
		svc, _, defaultID := newCommonService(t)
		for _, p := range []service.CommonResource{
			{Slug: "", Name: "x", Status: model.StatusActive},
			{Slug: "ok", Name: " ", Status: model.StatusActive},
			{Slug: "ok", Name: "bad\nname", Status: model.StatusActive},
			{Slug: "ok", Name: "x", Status: "disabled"},
		} {
			_, err := svc.PutTenant(ctx, defaultID, p)
			assertErrorIs(t, err, port.ErrInvalid)
		}
	})
}

func TestPutDomain(t *testing.T) {
	ctx := context.Background()
	svc, store, defaultID := newCommonService(t, service.WithMultiTenant(true))
	otherID := model.NewTenantID()
	_, err := svc.PutTenant(ctx, otherID, active("other", "Other"))
	mustNoError(t, err)

	_, err = svc.PutDomain(ctx, defaultID, "llm.acme.example", model.StatusActive)
	mustNoError(t, err)
	_, err = svc.PutDomain(ctx, defaultID, "llm.acme.example", model.StatusSuspended)
	mustNoError(t, err)
	domain, err := store.GetDomain(ctx, "llm.acme.example")
	mustNoError(t, err)
	if domain.Status != model.StatusSuspended {
		t.Errorf("status: got %q", domain.Status)
	}

	_, err = svc.PutDomain(ctx, otherID, "llm.acme.example", model.StatusActive)
	assertErrorIs(t, err, port.ErrAlreadyExists)
	_, err = svc.PutDomain(ctx, model.NewTenantID(), "new.acme.example", model.StatusActive)
	assertErrorIs(t, err, port.ErrParentNotFound)
	for _, host := range []string{"LLM.acme.example", "10.0.0.1", "bad_host.example"} {
		_, err = svc.PutDomain(ctx, defaultID, host, model.StatusActive)
		assertErrorIs(t, err, port.ErrInvalidHostname)
	}
}

func TestPutOrganization(t *testing.T) {
	ctx := context.Background()
	svc, _, defaultID := newCommonService(t, service.WithMultiTenant(true))
	orgID := model.NewOrgID()

	for range 2 {
		_, err := svc.PutOrganization(ctx, defaultID, orgID, active("acme", "Acme"))
		mustNoError(t, err)
	}
	roles, err := svc.ListRoles(ctx, defaultID, orgID)
	mustNoError(t, err)
	if len(roles) != 3 {
		t.Errorf("builtin roles: got %d, want 3", len(roles))
	}

	_, err = svc.PutOrganization(ctx, defaultID, orgID, service.CommonResource{Slug: "acme-corp", Name: "Acme Corp", Status: model.StatusSuspended})
	mustNoError(t, err)
	org, err := svc.GetOrganization(ctx, defaultID, orgID)
	mustNoError(t, err)
	if org.Slug() != "acme-corp" || org.Active() {
		t.Errorf("organization: got %q active=%v", org.Slug(), org.Active())
	}

	otherID := model.NewTenantID()
	_, err = svc.PutTenant(ctx, otherID, active("other", "Other"))
	mustNoError(t, err)
	_, err = svc.PutOrganization(ctx, otherID, orgID, active("acme", "Acme"))
	assertErrorIs(t, err, port.ErrAlreadyExists)
	_, err = svc.PutOrganization(ctx, model.NewTenantID(), model.NewOrgID(), active("acme", "Acme"))
	assertErrorIs(t, err, port.ErrParentNotFound)
}

// signedIn creates the account the authentication bridge creates on a first
// sign-in: provisioning only manages members that already have an identity.
func signedIn(t *testing.T, store *xologorm.Store, tenantID model.TenantID) model.UserID {
	t.Helper()
	user, err := store.FindOrCreateUser(context.Background(), tenantID, "oidc", "sub-"+uuid.NewString())
	mustNoError(t, err)
	return user.ID()
}

func member(email string) service.CommonMember {
	return service.CommonMember{Email: email, DisplayName: "Member", TenantRole: model.TenantRoleMember, Status: model.StatusActive}
}

func TestPutTenantMember(t *testing.T) {
	ctx := context.Background()

	t.Run("creates an unknown member without a sign-in link", func(t *testing.T) {
		svc, store, tenantID := newCommonService(t)
		userID := model.NewUserID()
		_, err := svc.PutTenantMember(ctx, tenantID, userID, member("jane@acme.example"))
		mustNoError(t, err)
		user, err := store.GetUserByID(ctx, userID)
		mustNoError(t, err)
		if user.TenantID() != tenantID || user.Email() != "jane@acme.example" || user.Provider() != "" || user.Subject() != "" ||
			user.DeclaredIdentity() != nil || !slices.Equal(user.Roles(), []string{model.PlatformRoleUser}) {
			t.Errorf("created member: %+v", user)
		}
	})

	t.Run("updates a member and repeats as a no-op", func(t *testing.T) {
		svc, store, tenantID := newCommonService(t)
		userID := signedIn(t, store, tenantID)
		for range 2 {
			_, err := svc.PutTenantMember(ctx, tenantID, userID, member("jane@acme.example"))
			mustNoError(t, err)
		}
		user, err := svc.GetUser(ctx, tenantID, userID)
		mustNoError(t, err)
		if user.Email() != "jane@acme.example" || user.DisplayName() != "Member" {
			t.Errorf("user: got %q/%q", user.Email(), user.DisplayName())
		}
	})

	t.Run("keeps the identity of a linked user", func(t *testing.T) {
		svc, store, tenantID := newCommonService(t)
		linked, err := store.FindOrCreateUser(ctx, tenantID, "oidc", "sub-jane")
		mustNoError(t, err)

		_, err = svc.PutTenantMember(ctx, tenantID, linked.ID(), service.CommonMember{Email: "jane@acme.example", TenantRole: model.TenantRoleOwner, Status: model.StatusActive})
		mustNoError(t, err)
		user, err := svc.GetUser(ctx, tenantID, linked.ID())
		mustNoError(t, err)
		if user.Provider() != "oidc" || user.Subject() != "sub-jane" || user.TenantRole() != model.TenantRoleOwner {
			t.Errorf("user: got %q/%q role %q", user.Provider(), user.Subject(), user.TenantRole())
		}
	})

	t.Run("refuses the last tenant owner, reserved emails and foreign ids", func(t *testing.T) {
		svc, store, tenantID := newCommonService(t, service.WithReservedEmails("boss@corp.example"), service.WithMultiTenant(true))
		ownerID := signedIn(t, store, tenantID)
		owner := member("owner@acme.example")
		owner.TenantRole = model.TenantRoleOwner
		_, err := svc.PutTenantMember(ctx, tenantID, ownerID, owner)
		mustNoError(t, err)

		_, err = svc.PutTenantMember(ctx, tenantID, ownerID, member("owner@acme.example"))
		assertErrorIs(t, err, port.ErrLastOwner)

		_, err = svc.PutTenantMember(ctx, tenantID, signedIn(t, store, tenantID), member("Boss@Corp.example"))
		assertErrorIs(t, err, port.ErrInvalid)

		otherID := model.NewTenantID()
		_, err = svc.PutTenant(ctx, otherID, active("other", "Other"))
		mustNoError(t, err)
		_, err = svc.PutTenantMember(ctx, otherID, ownerID, owner)
		assertErrorIs(t, err, port.ErrAlreadyExists)
	})
}

// TestPlatformAdminTakeover replays the account takeover reported on PR 124:
// a provisioning client must not change anything on a platform administrator,
// whatever the route, so the account can never be relinked or demoted.
func TestPlatformAdminTakeover(t *testing.T) {
	ctx := context.Background()
	svc, store, tenantID := newCommonService(t, service.WithReservedEmails("admin@corp.example"))

	// The default administrator signs in once: the bridge creates the user and
	// grants the platform admin role.
	signedIn, err := store.FindOrCreateUser(ctx, tenantID, "oidc", "sub-admin")
	mustNoError(t, err)
	admin := model.CopyUser(signedIn)
	admin.SetEmail("admin@corp.example")
	admin.SetDisplayName("Admin")
	admin.SetRoles(model.PlatformRoleUser, model.PlatformRoleAdmin)
	mustNoError(t, store.SaveUser(ctx, admin))

	unchanged := service.CommonMember{Email: "admin@corp.example", DisplayName: "Admin", TenantRole: model.TenantRoleMember, Status: model.StatusActive}
	_, err = svc.PutTenantMember(ctx, tenantID, admin.ID(), unchanged)
	mustNoError(t, err)

	for name, change := range map[string]func(*service.CommonMember){
		"email":        func(m *service.CommonMember) { m.Email = "attacker@evil.example" },
		"display name": func(m *service.CommonMember) { m.DisplayName = "Attacker" },
		"status":       func(m *service.CommonMember) { m.Status = model.StatusSuspended },
		"tenant role":  func(m *service.CommonMember) { m.TenantRole = model.TenantRoleOwner },
	} {
		t.Run(name, func(t *testing.T) {
			p := unchanged
			change(&p)
			_, err := svc.PutTenantMember(ctx, tenantID, admin.ID(), p)
			assertErrorIs(t, err, port.ErrPlatformAdminProtected)
		})
	}

	t.Run("through the identity upsert", func(t *testing.T) {
		email := "attacker@evil.example"
		_, _, err := svc.ProvisionUser(ctx, tenantID, service.UserIdentityParams{Provider: "oidc", Subject: "sub-admin", Email: &email})
		assertErrorIs(t, err, port.ErrPlatformAdminProtected)
		name := "Attacker"
		_, _, err = svc.ProvisionUser(ctx, tenantID, service.UserIdentityParams{Provider: "oidc", Subject: "sub-admin", DisplayName: &name})
		assertErrorIs(t, err, port.ErrPlatformAdminProtected)
	})

	stored, err := svc.GetUser(ctx, tenantID, admin.ID())
	mustNoError(t, err)
	if stored.Email() != "admin@corp.example" || stored.DisplayName() != "Admin" || !stored.Active() ||
		stored.Provider() != "oidc" || stored.Subject() != "sub-admin" || !hasRole(stored, model.PlatformRoleAdmin) {
		t.Errorf("administrator changed: %q %q active=%v %q/%q roles=%v",
			stored.Email(), stored.DisplayName(), stored.Active(), stored.Provider(), stored.Subject(), stored.Roles())
	}
}

func hasRole(user model.User, role string) bool {
	for _, r := range user.Roles() {
		if r == role {
			return true
		}
	}
	return false
}

func TestPutOrgMember(t *testing.T) {
	ctx := context.Background()
	svc, store, tenantID := newCommonService(t, service.WithMultiTenant(true))
	orgID := model.NewOrgID()
	_, err := svc.PutOrganization(ctx, tenantID, orgID, active("acme", "Acme"))
	mustNoError(t, err)
	ownerID, memberID := signedIn(t, store, tenantID), signedIn(t, store, tenantID)

	put := func(userID model.UserID, role model.MembershipRole, status model.Status) error {
		_, err := svc.PutOrgMember(ctx, tenantID, orgID, userID, service.CommonMembership{Role: role, Status: status})
		return err
	}
	mustNoError(t, put(ownerID, model.MembershipRoleOwner, model.StatusActive))
	mustNoError(t, put(ownerID, model.MembershipRoleOwner, model.StatusActive))
	mustNoError(t, put(memberID, model.MembershipRoleAdmin, model.StatusActive))

	// A custom role survives a change of common role.
	roleKey := uuid.NewString()
	_, err = svc.PutCustomRole(ctx, tenantID, orgID, roleKey, model.MatchCondition{}, model.CustomRoleSettings{Name: "Auditor", Permissions: []string{}, ModelGrants: []model.ModelGrantSettings{}})
	mustNoError(t, err)
	membership, err := store.GetUserOrgMembership(ctx, memberID, orgID)
	mustNoError(t, err)
	_, err = svc.SetMemberRoles(ctx, tenantID, orgID, membership.ID(), []model.RoleID{model.RoleID(roleKey)}, []string{model.BuiltinKindAdmin})
	mustNoError(t, err)
	mustNoError(t, put(memberID, model.MembershipRoleMember, model.StatusSuspended))
	membership, err = store.GetUserOrgMembership(ctx, memberID, orgID)
	mustNoError(t, err)
	if model.CommonRoleOf(membership) != model.MembershipRoleMember || membership.Status() != model.StatusSuspended || len(membership.Roles()) != 2 {
		t.Errorf("membership: role %q status %q roles %d", model.CommonRoleOf(membership), membership.Status(), len(membership.Roles()))
	}

	assertErrorIs(t, put(ownerID, model.MembershipRoleAdmin, model.StatusActive), port.ErrLastOwner)
	assertErrorIs(t, put(ownerID, model.MembershipRoleOwner, model.StatusSuspended), port.ErrLastOwner)

	otherID := model.NewTenantID()
	_, err = svc.PutTenant(ctx, otherID, active("other", "Other"))
	mustNoError(t, err)
	foreignOrg := model.NewOrgID()
	_, err = svc.PutOrganization(ctx, otherID, foreignOrg, active("foreign", "Foreign"))
	mustNoError(t, err)
	_, err = svc.PutOrgMember(ctx, tenantID, foreignOrg, memberID, service.CommonMembership{Role: model.MembershipRoleMember, Status: model.StatusActive})
	assertErrorIs(t, err, port.ErrParentNotFound)
	_, err = svc.PutOrgMember(ctx, otherID, foreignOrg, memberID, service.CommonMembership{Role: model.MembershipRoleMember, Status: model.StatusActive})
	assertErrorIs(t, err, port.ErrParentNotFound)
	_, err = svc.PutOrgMember(ctx, tenantID, orgID, model.NewUserID(), service.CommonMembership{Role: model.MembershipRoleMember, Status: model.StatusActive})
	assertErrorIs(t, err, port.ErrParentNotFound)
}
