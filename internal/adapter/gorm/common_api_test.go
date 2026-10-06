package gorm_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	gormpkg "gorm.io/gorm"
)

func TestDomainStore(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := t.Context()
		acme := provisionedTenant(t, store)
		other := provisionedTenant(t, store)

		require.NoError(t, store.SaveDomain(ctx, model.Domain{Hostname: "acme.example.test", TenantID: acme.ID(), Status: model.StatusActive}))
		require.NoError(t, store.SaveDomain(ctx, model.Domain{Hostname: "acme.example.test", TenantID: acme.ID(), Status: model.StatusSuspended}))
		domain, err := store.GetDomain(ctx, "acme.example.test")
		require.NoError(t, err)
		require.Equal(t, model.Domain{Hostname: "acme.example.test", TenantID: acme.ID(), Status: model.StatusSuspended}, domain)

		err = store.SaveDomain(ctx, model.Domain{Hostname: "acme.example.test", TenantID: other.ID(), Status: model.StatusActive})
		require.ErrorIs(t, err, port.ErrAlreadyExists)

		for _, invalid := range []string{"Upper.Example.Test", "127.0.0.1", "bad_label.test", "-dash.test", ""} {
			err := store.SaveDomain(ctx, model.Domain{Hostname: invalid, TenantID: acme.ID(), Status: model.StatusActive})
			require.ErrorIs(t, err, port.ErrInvalidHostname, invalid)
		}

		domains, err := store.ListTenantDomains(ctx, acme.ID())
		require.NoError(t, err)
		require.Len(t, domains, 1)

		require.NoError(t, store.DeleteTenant(ctx, acme.ID()))
		_, err = store.GetDomain(ctx, "acme.example.test")
		require.ErrorIs(t, err, port.ErrNotFound)
	})
}

func TestSlugRenameConflicts(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := t.Context()
		first := provisionedTenant(t, store)
		second := provisionedTenant(t, store)
		err := store.SaveTenant(ctx, model.UpdateTenant(second, model.WithTenantSlug(first.Slug())))
		require.ErrorIs(t, err, port.ErrAlreadyExists)

		acme := model.NewOrganization(first.ID(), "acme", "Acme", "")
		globex := model.NewOrganization(first.ID(), "globex", "Globex", "")
		require.NoError(t, store.CreateOrg(ctx, acme))
		require.NoError(t, store.CreateOrg(ctx, globex))
		err = store.SaveOrg(ctx, model.UpdateOrganization(globex, model.WithOrgSlug("acme")))
		require.ErrorIs(t, err, port.ErrAlreadyExists)
	})
}

func TestUsersWithoutIdentityCoexist(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := t.Context()
		tenant := provisionedTenant(t, store)
		for _, email := range []string{"first@example.test", "second@example.test"} {
			require.NoError(t, store.SaveUser(ctx, model.NewUser(tenant.ID(), "", "", email, "", true)))
		}
		// The identity key still holds for users that have one.
		_, err := store.FindOrCreateUser(ctx, tenant.ID(), "oidc", "subject")
		require.NoError(t, err)
		duplicate := model.NewUser(tenant.ID(), "oidc", "subject", "", "", true)
		require.Error(t, store.SaveUser(ctx, duplicate))
	})
}

func TestSuspendedMembershipGrantsNothing(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := t.Context()
		svc := provisioningService(store, store)
		tenant := provisionedTenant(t, store)
		org := provisionedOrg(t, svc, tenant, "owner-"+uuid.NewString())
		member := secondOwner(t, store, tenant, org.Org.ID())

		require.NoError(t, store.SetMembershipStatus(ctx, member.ID(), model.StatusSuspended))

		isMember, err := store.IsMember(ctx, member.UserID(), org.Org.ID())
		require.NoError(t, err)
		require.False(t, isMember)
		memberships, err := store.GetUserMemberships(ctx, member.UserID())
		require.NoError(t, err)
		require.Empty(t, memberships)
		permissions, err := store.ResolveEffectivePermissions(ctx, member.UserID(), org.Org.ID())
		require.NoError(t, err)
		require.False(t, permissions.IsOwner(), "a suspended owner keeps no permission")

		stored, err := store.GetUserOrgMembership(ctx, member.UserID(), org.Org.ID())
		require.NoError(t, err)
		require.Equal(t, model.StatusSuspended, stored.Status())
		require.Equal(t, model.MembershipRoleOwner, model.CommonRoleOf(stored))
	})
}

func TestLastOwnerTransitions(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := t.Context()
		svc := provisioningService(store, store)
		tenant := provisionedTenant(t, store)
		org := provisionedOrg(t, svc, tenant, "owner-"+uuid.NewString())
		owner := org.OwnerMembership

		suspend := func(id model.MembershipID) error {
			return store.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
				return tx.SetMembershipStatus(ctx, id, model.StatusSuspended)
			})
		}
		require.ErrorIs(t, suspend(owner.ID()), port.ErrLastOwner)

		second := secondOwner(t, store, tenant, org.Org.ID())
		require.NoError(t, suspend(owner.ID()))
		require.ErrorIs(t, suspend(second.ID()), port.ErrLastOwner)

		tenantOwner := model.NewUser(tenant.ID(), "", "", "owner@example.test", "", true)
		tenantOwner.SetTenantRole(model.TenantRoleOwner)
		require.NoError(t, store.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
			return tx.SaveUser(ctx, tenantOwner)
		}))
		demote := func(change func(*model.BaseUser)) error {
			return store.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
				u := model.CopyUser(tenantOwner)
				change(u)
				return tx.SaveUser(ctx, u)
			})
		}
		require.ErrorIs(t, demote(func(u *model.BaseUser) { u.SetTenantRole(model.TenantRoleMember) }), port.ErrLastOwner)
		require.ErrorIs(t, demote(func(u *model.BaseUser) { u.SetActive(false) }), port.ErrLastOwner)

		// Removing the whole tenant is not a transition of its owners.
		require.NoError(t, store.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
			return tx.DeleteTenant(ctx, tenant.ID())
		}))
	})
}

func TestDomainMutationsAreAudited(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newStoreOn(t, db)
		ctx := model.WithActor(t.Context(), model.Actor{URI: "urn:test:provisioner"})
		tenant := provisionedTenant(t, store)
		save := func(status model.Status) error {
			return store.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
				return tx.SaveDomain(ctx, model.Domain{Hostname: "audited.example.test", TenantID: tenant.ID(), Status: status})
			})
		}
		require.NoError(t, save(model.StatusActive))
		require.NoError(t, save(model.StatusActive))
		require.NoError(t, save(model.StatusSuspended))

		var audits []xologorm.MutationAudit
		require.NoError(t, db.Where("resource = ? AND resource_id = ?", "domain", "audited.example.test").Find(&audits).Error)
		require.Len(t, audits, 2, "a no-op write produces no audit")
		for _, audit := range audits {
			require.Equal(t, string(tenant.ID()), audit.TenantID)
		}
	})
}

func secondOwner(t *testing.T, store *xologorm.Store, tenant model.Tenant, orgID model.OrgID) model.Membership {
	t.Helper()
	ctx := context.Background()
	user, err := store.FindOrCreateUser(ctx, tenant.ID(), "oidc", "second-"+uuid.NewString())
	require.NoError(t, err)
	membership := model.NewMembership(user.ID(), orgID)
	require.NoError(t, store.AddMember(ctx, membership))
	roles, err := store.ListOrgRoles(ctx, orgID)
	require.NoError(t, err)
	for _, role := range roles {
		if role.BuiltinKind() == model.BuiltinKindOwner {
			require.NoError(t, store.SetMembershipRoles(ctx, membership.ID(), []model.RoleID{role.ID()}))
		}
	}
	return membership
}

func TestInitializeDomainRouting(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := t.Context()
		acme := model.NewTenant("acme", "Acme", "")
		require.NoError(t, store.CreateTenant(ctx, acme))
		other := provisionedTenant(t, store)
		// A hostname already declared for another tenant is kept, never stolen.
		require.NoError(t, store.SaveDomain(ctx, model.Domain{Hostname: other.Slug() + ".xolo.example.test", TenantID: acme.ID(), Status: model.StatusActive}))

		require.NoError(t, store.InitializeDomainRouting(ctx, ""), "an empty pattern does nothing")
		require.NoError(t, store.InitializeDomainRouting(ctx, "{tenant}.XOLO.example.test:3002"))

		domain, err := store.GetDomain(ctx, "acme.xolo.example.test")
		require.NoError(t, err)
		require.Equal(t, model.Domain{Hostname: "acme.xolo.example.test", TenantID: acme.ID(), Status: model.StatusActive}, domain)
		kept, err := store.GetDomain(ctx, other.Slug()+".xolo.example.test")
		require.NoError(t, err)
		require.Equal(t, acme.ID(), kept.TenantID)

		// The expansion runs once: later tenants and changes are not overwritten.
		require.NoError(t, store.SaveDomain(ctx, model.Domain{Hostname: "acme.xolo.example.test", TenantID: acme.ID(), Status: model.StatusSuspended}))
		late := model.NewTenant("late", "Late", "")
		require.NoError(t, store.CreateTenant(ctx, late))
		require.NoError(t, store.InitializeDomainRouting(ctx, "{tenant}.xolo.example.test"))
		domain, err = store.GetDomain(ctx, "acme.xolo.example.test")
		require.NoError(t, err)
		require.Equal(t, model.StatusSuspended, domain.Status)
		_, err = store.GetDomain(ctx, "late.xolo.example.test")
		require.ErrorIs(t, err, port.ErrNotFound)
	})
}
