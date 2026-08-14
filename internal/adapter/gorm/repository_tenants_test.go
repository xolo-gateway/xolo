package gorm_test

import (
	"context"
	"testing"

	"github.com/pkg/errors"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

func TestTenantCRUD(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := context.Background()

		tenant := model.NewTenant("acme", "Acme", "Acme Inc.")
		if err := store.CreateTenant(ctx, tenant); err != nil {
			t.Fatalf("create tenant: %v", err)
		}

		byID, err := store.GetTenantByID(ctx, tenant.ID())
		if err != nil {
			t.Fatalf("get tenant by id: %v", err)
		}
		if byID.Slug() != "acme" || byID.Name() != "Acme" || !byID.Active() {
			t.Errorf("tenant: got %q/%q/active=%v", byID.Slug(), byID.Name(), byID.Active())
		}

		bySlug, err := store.GetTenantBySlug(ctx, "acme")
		if err != nil {
			t.Fatalf("get tenant by slug: %v", err)
		}
		if bySlug.ID() != tenant.ID() {
			t.Errorf("id: got %q, want %q", bySlug.ID(), tenant.ID())
		}

		// A deactivation must survive the round-trip: the Active column is an int
		// precisely so GORM does not drop the zero value from the statement.
		updated := model.UpdateTenant(tenant, model.WithTenantActive(false), model.WithTenantName("Acme Corp"))
		if err := store.SaveTenant(ctx, updated); err != nil {
			t.Fatalf("save tenant: %v", err)
		}

		reloaded, err := store.GetTenantByID(ctx, tenant.ID())
		if err != nil {
			t.Fatalf("reload tenant: %v", err)
		}
		if reloaded.Active() {
			t.Error("tenant should be inactive")
		}
		if reloaded.Name() != "Acme Corp" {
			t.Errorf("name: got %q, want %q", reloaded.Name(), "Acme Corp")
		}

		if err := store.CreateTenant(ctx, model.NewTenant("acme", "Acme again", "")); !errors.Is(err, port.ErrAlreadyExists) {
			t.Errorf("duplicate slug: got %v, want %v", err, port.ErrAlreadyExists)
		}

		if _, err := store.GetTenantBySlug(ctx, "unknown"); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("unknown slug: got %v, want %v", err, port.ErrNotFound)
		}
	})
}

func TestDefaultTenantExists(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		// A fresh instance must own the tenant everything hangs from, exactly like
		// one upgraded from a pre-tenant schema.
		tenant, err := store.GetTenantBySlug(context.Background(), model.DefaultTenantSlug)
		if err != nil {
			t.Fatalf("get default tenant: %v", err)
		}
		if !tenant.Active() {
			t.Error("the default tenant should be active")
		}
	})
}

// TestTenantIsolation is the core guarantee of multi-tenancy: two tenants may
// hold an organization with the same slug and a user with the same identity,
// and neither can read the other's.
func TestTenantIsolation(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := context.Background()

		first := model.NewTenant("first", "First", "")
		second := model.NewTenant("second", "Second", "")
		for _, tenant := range []model.Tenant{first, second} {
			if err := store.CreateTenant(ctx, tenant); err != nil {
				t.Fatalf("create tenant %q: %v", tenant.Slug(), err)
			}
		}

		firstOrg := model.NewOrganization(first.ID(), "acme", "Acme", "")
		secondOrg := model.NewOrganization(second.ID(), "acme", "Acme", "")
		for _, org := range []model.Organization{firstOrg, secondOrg} {
			if err := store.CreateOrg(ctx, org); err != nil {
				t.Fatalf("create org in tenant %q: %v", org.TenantID(), err)
			}
		}

		resolved, err := store.GetOrgBySlug(ctx, first.ID(), "acme")
		if err != nil {
			t.Fatalf("get org by slug: %v", err)
		}
		if resolved.ID() != firstOrg.ID() {
			t.Errorf("slug %q resolved to the other tenant's organization", "acme")
		}

		firstUser := model.NewUser(first.ID(), "openid-connect", "sub-1", "jean@first.tld", "Jean", true)
		secondUser := model.NewUser(second.ID(), "openid-connect", "sub-1", "jean@second.tld", "Jean", true)
		for _, user := range []model.User{firstUser, secondUser} {
			if err := store.SaveUser(ctx, user); err != nil {
				t.Fatalf("save user in tenant %q: %v", user.TenantID(), err)
			}
		}

		found, err := store.GetUserByIdentity(ctx, second.ID(), "openid-connect", "sub-1")
		if err != nil {
			t.Fatalf("get user by identity: %v", err)
		}
		if found.ID() != secondUser.ID() {
			t.Errorf("identity resolved to the other tenant's user")
		}

		// The listings must not leak across the boundary either.
		firstID := first.ID()
		orgs, _, err := store.ListOrgs(ctx, port.ListOrgsOptions{TenantID: &firstID})
		if err != nil {
			t.Fatalf("list orgs: %v", err)
		}
		if len(orgs) != 1 || orgs[0].ID() != firstOrg.ID() {
			t.Errorf("orgs: got %d rows, want only the first tenant's organization", len(orgs))
		}

		users, err := store.QueryUsers(ctx, port.QueryUsersOptions{TenantID: &firstID})
		if err != nil {
			t.Fatalf("query users: %v", err)
		}
		if len(users) != 1 || users[0].ID() != firstUser.ID() {
			t.Errorf("users: got %d rows, want only the first tenant's user", len(users))
		}

		count, err := store.CountUsers(ctx, port.QueryUsersOptions{TenantID: &firstID})
		if err != nil {
			t.Fatalf("count users: %v", err)
		}
		if count != 1 {
			t.Errorf("user count: got %d, want 1", count)
		}
	})
}

// TestDeleteTenant checks the applicative cascade: no foreign key backs the
// tenant column, so everything it owns is removed explicitly.
func TestDeleteTenant(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := context.Background()

		tenant := model.NewTenant("doomed", "Doomed", "")
		if err := store.CreateTenant(ctx, tenant); err != nil {
			t.Fatalf("create tenant: %v", err)
		}

		org := model.NewOrganization(tenant.ID(), "acme", "Acme", "")
		if err := store.CreateOrg(ctx, org); err != nil {
			t.Fatalf("create org: %v", err)
		}
		if err := store.EnsureBuiltinRoles(ctx, org.ID()); err != nil {
			t.Fatalf("ensure builtin roles: %v", err)
		}

		user := model.NewUser(tenant.ID(), "openid-connect", "sub-1", "jean@doomed.tld", "Jean", true)
		if err := store.SaveUser(ctx, user); err != nil {
			t.Fatalf("save user: %v", err)
		}

		membership := model.NewMembership(user.ID(), org.ID())
		if err := store.AddMember(ctx, membership); err != nil {
			t.Fatalf("add member: %v", err)
		}

		// A neighbouring tenant must come out untouched.
		other := model.NewTenant("survivor", "Survivor", "")
		if err := store.CreateTenant(ctx, other); err != nil {
			t.Fatalf("create other tenant: %v", err)
		}
		otherUser := model.NewUser(other.ID(), "openid-connect", "sub-1", "jean@survivor.tld", "Jean", true)
		if err := store.SaveUser(ctx, otherUser); err != nil {
			t.Fatalf("save other user: %v", err)
		}

		if err := store.DeleteTenant(ctx, tenant.ID()); err != nil {
			t.Fatalf("delete tenant: %v", err)
		}

		if _, err := store.GetTenantByID(ctx, tenant.ID()); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("tenant: got %v, want %v", err, port.ErrNotFound)
		}
		if _, err := store.GetOrgByID(ctx, org.ID()); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("organization: got %v, want %v", err, port.ErrNotFound)
		}
		if _, err := store.GetUserByID(ctx, user.ID()); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("user: got %v, want %v", err, port.ErrNotFound)
		}
		if _, err := store.GetMembership(ctx, membership.ID()); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("membership: got %v, want %v", err, port.ErrNotFound)
		}

		roles, err := store.ListOrgRoles(ctx, org.ID())
		if err != nil {
			t.Fatalf("list org roles: %v", err)
		}
		if len(roles) != 0 {
			t.Errorf("roles: got %d, want none", len(roles))
		}

		if _, err := store.GetUserByID(ctx, otherUser.ID()); err != nil {
			t.Errorf("the other tenant's user should have survived, got %v", err)
		}

		if err := store.DeleteTenant(ctx, model.TenantID("nope")); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("unknown tenant: got %v, want %v", err, port.ErrNotFound)
		}
	})
}
