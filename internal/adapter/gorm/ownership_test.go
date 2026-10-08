package gorm_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	gormpkg "gorm.io/gorm"
)

func controlPlane(ctx context.Context) context.Context {
	return model.WithWriteAuthority(ctx, model.OwnerControlPlane)
}

// ownershipFixture is one organization of the test tenant with one member.
type ownershipFixture struct {
	org  model.OrgID
	user model.UserID
}

func newOwnershipFixture(t *testing.T, store *xologorm.Store) ownershipFixture {
	t.Helper()
	ctx := t.Context()
	org := model.NewOrganization(testTenantID, "org-"+uuid.NewString()[:8], "Org", "")
	require.NoError(t, store.CreateOrg(ctx, org))
	require.NoError(t, store.EnsureBuiltinRoles(ctx, org.ID()))
	user := model.NewUser(testTenantID, "oidc", uuid.NewString(), uuid.NewString()[:8]+"@example.test", "User", true, model.PlatformRoleUser)
	require.NoError(t, store.SaveUser(ctx, user))
	require.NoError(t, store.AddMember(ctx, model.NewMembership(user.ID(), org.ID())))
	return ownershipFixture{org: org.ID(), user: user.ID()}
}

// ownedStore opens a store of db enforcing policy.
func ownedStore(t *testing.T, db *gormpkg.DB, policy model.OwnershipPolicy) *xologorm.Store {
	t.Helper()
	store := xologorm.NewStore(db, xologorm.WithOwnership(policy))
	require.NoError(t, store.Migrate(context.Background()))
	return store
}

// familyWrites changes the public representation of one resource of each
// family, differently on each call. base writes the fixtures a write needs
// in another family.
func familyWrites(f ownershipFixture, base *xologorm.Store) map[string]func(ctx context.Context, store *xologorm.Store) error {
	return map[string]func(ctx context.Context, store *xologorm.Store) error{
		model.FamilyTenant: func(ctx context.Context, store *xologorm.Store) error {
			tenant, err := store.GetTenantByID(ctx, testTenantID)
			if err != nil {
				return err
			}
			return store.SaveTenant(ctx, model.UpdateTenant(tenant, model.WithTenantName("Tenant "+uuid.NewString()[:8])))
		},
		model.FamilyTenantDomain: func(ctx context.Context, store *xologorm.Store) error {
			return store.SaveDomain(ctx, model.Domain{Hostname: uuid.NewString()[:8] + ".example.test", TenantID: testTenantID, Status: model.StatusActive})
		},
		model.FamilyOrganization: func(ctx context.Context, store *xologorm.Store) error {
			org, err := store.GetOrgByID(ctx, f.org)
			if err != nil {
				return err
			}
			return store.SaveOrg(ctx, model.UpdateOrganization(org, model.WithOrgName("Org "+uuid.NewString()[:8])))
		},
		model.FamilyMember: func(ctx context.Context, store *xologorm.Store) error {
			user, err := store.GetUserByID(ctx, f.user)
			if err != nil {
				return err
			}
			next := model.CopyUser(user)
			next.SetDisplayName("User " + uuid.NewString()[:8])
			return store.SaveUser(ctx, next)
		},
		model.FamilyOrganizationMembership: func(ctx context.Context, store *xologorm.Store) error {
			user := model.NewUser(testTenantID, "oidc", uuid.NewString(), "", "", true, model.PlatformRoleUser)
			// The member itself is written without restriction: only the
			// membership is under test.
			if err := base.SaveUser(context.Background(), user); err != nil {
				return err
			}
			return store.AddMember(ctx, model.NewMembership(user.ID(), f.org))
		},
	}
}

// TestOwnershipMatrix asserts every family against every policy and
// authority: shared accepts both, local and control_plane only themselves.
func TestOwnershipMatrix(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		base := newSeededStore(t, db)
		fixture := newOwnershipFixture(t, base)
		writes := familyWrites(fixture, base)
		for _, family := range []string{model.FamilyTenant, model.FamilyTenantDomain, model.FamilyOrganization, model.FamilyMember, model.FamilyOrganizationMembership} {
			for _, owner := range []model.Owner{model.OwnerShared, model.OwnerLocal, model.OwnerControlPlane} {
				store := ownedStore(t, db, model.OwnershipPolicy{family: owner})
				for _, authority := range []model.Owner{model.OwnerLocal, model.OwnerControlPlane} {
					ctx := model.WithWriteAuthority(t.Context(), authority)
					events := eventCount(t, db)
					err := writes[family](ctx, store)
					if owner == model.OwnerShared || owner == authority {
						require.NoError(t, err, "%s owned by %s, written by %s", family, owner, authority)
						continue
					}
					require.ErrorIs(t, err, port.ErrOwnershipDenied, "%s owned by %s, written by %s", family, owner, authority)
					require.ErrorIs(t, err, port.ErrNotAllowed)
					if family != model.FamilyOrganizationMembership {
						require.Equal(t, events, eventCount(t, db), "a refused write publishes nothing")
					}
				}
			}
		}
	})
}

// TestOwnershipLeavesLocalFields keeps the fields outside the common
// contract writable locally when the control plane owns every family.
func TestOwnershipLeavesLocalFields(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		base := newSeededStore(t, db)
		fixture := newOwnershipFixture(t, base)
		store := ownedStore(t, db, controlPlanePolicy())

		org, err := store.GetOrgByID(ctx, fixture.org)
		require.NoError(t, err)
		require.NoError(t, store.SaveOrg(ctx, model.UpdateOrganization(org, model.WithOrgCurrency("USD"))), "organization settings stay local")

		user, err := store.GetUserByID(ctx, fixture.user)
		require.NoError(t, err)
		next := model.CopyUser(user)
		next.SetRoles(model.PlatformRoleUser, model.PlatformRoleAdmin)
		require.NoError(t, store.SaveUser(ctx, next), "platform roles stay local")

		next.SetActive(false)
		require.ErrorIs(t, store.SaveUser(ctx, next), port.ErrOwnershipDenied, "the status is part of the member")
		stored, err := store.GetUserByID(ctx, fixture.user)
		require.NoError(t, err)
		require.True(t, stored.Active())
	})
}

func controlPlanePolicy() model.OwnershipPolicy {
	policy := model.OwnershipPolicy{}
	for _, family := range model.OwnershipFamilies {
		policy[family] = model.OwnerControlPlane
	}
	return policy
}

// TestOwnershipCascades refuses a cascade reaching a family of another
// authority as a whole: nothing is removed nor published.
func TestOwnershipCascades(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		base := newSeededStore(t, db)
		fixture := newOwnershipFixture(t, base)

		members := ownedStore(t, db, model.OwnershipPolicy{model.FamilyMember: model.OwnerControlPlane})
		events := eventCount(t, db)
		require.ErrorIs(t, members.DeleteTenant(ctx, testTenantID), port.ErrOwnershipDenied)
		_, err := base.GetTenantByID(ctx, testTenantID)
		require.NoError(t, err)
		_, err = base.GetUserByID(ctx, fixture.user)
		require.NoError(t, err)
		require.Equal(t, events, eventCount(t, db))

		memberships := ownedStore(t, db, model.OwnershipPolicy{model.FamilyOrganizationMembership: model.OwnerControlPlane})
		require.ErrorIs(t, memberships.DeleteOrg(ctx, fixture.org), port.ErrOwnershipDenied)
		require.ErrorIs(t, memberships.DeleteUser(ctx, fixture.user), port.ErrOwnershipDenied)
		_, err = base.GetOrgByID(ctx, fixture.org)
		require.NoError(t, err)
		require.Equal(t, events, eventCount(t, db))

		// The authority owning every family of the cascade removes it.
		require.NoError(t, memberships.DeleteOrg(controlPlane(ctx), fixture.org))
	})
}

// TestOwnershipWithoutProjection covers the writers checking their own
// family: invitations and webhook subscriptions.
func TestOwnershipWithoutProjection(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		base := newSeededStore(t, db)
		fixture := newOwnershipFixture(t, base)
		invite := model.NewInviteToken(fixture.org, "member", nil, nil, nil, fixture.user)
		require.NoError(t, base.CreateInvite(ctx, invite))

		store := ownedStore(t, db, model.OwnershipPolicy{model.FamilyOrganizationMembership: model.OwnerControlPlane, model.FamilySubscription: model.OwnerControlPlane})
		require.ErrorIs(t, store.CreateInvite(ctx, model.NewInviteToken(fixture.org, "member", nil, nil, nil, fixture.user)), port.ErrOwnershipDenied)
		require.ErrorIs(t, store.RevokeInvite(ctx, invite.ID()), port.ErrOwnershipDenied)
		require.ErrorIs(t, store.DeleteInvite(ctx, invite.ID()), port.ErrOwnershipDenied)

		settings := model.WebhookSettings{Destination: "https://hooks.example.test/in", Events: []string{"*"}, Enabled: true, EncryptedSecrets: testWebhookSecret, SecretCount: 1}
		id := model.WebhookID(uuid.NewString())
		_, err := store.PutWebhook(ctx, testTenantID, id, settings)
		require.ErrorIs(t, err, port.ErrOwnershipDenied)
		_, err = store.PutWebhook(controlPlane(ctx), testTenantID, id, settings)
		require.NoError(t, err)
		require.ErrorIs(t, store.ResetWebhook(ctx, testTenantID, id), port.ErrOwnershipDenied)
		require.ErrorIs(t, store.DeleteWebhook(ctx, testTenantID, id), port.ErrOwnershipDenied)

		// The tenant cascade removes subscriptions: a tenant owned locally
		// still cannot take the control plane's subscriptions with it.
		tenants := ownedStore(t, db, model.OwnershipPolicy{model.FamilySubscription: model.OwnerControlPlane})
		require.ErrorIs(t, tenants.DeleteTenant(ctx, testTenantID), port.ErrOwnershipDenied)
		hooks, err := base.ListWebhooks(ctx, testTenantID)
		require.NoError(t, err)
		require.Len(t, hooks, 1)
	})
}

// TestOwnershipKeepsPlatformAdminsProtected: owning members never lets the
// control plane change a platform administrator.
func TestOwnershipKeepsPlatformAdminsProtected(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		base := newSeededStore(t, db)
		admin := model.NewUser(testTenantID, "oidc", "admin", "admin@example.test", "Admin", true, model.PlatformRoleUser, model.PlatformRoleAdmin)
		require.NoError(t, base.SaveUser(t.Context(), admin))

		store := ownedStore(t, db, model.OwnershipPolicy{model.FamilyMember: model.OwnerControlPlane})
		svc := service.NewProvisioningService(store, store, store, store, service.WithProvisioningTransaction(store), service.WithProvisioningReader(store), service.WithMultiTenant(true))
		_, err := svc.PutTenantMember(controlPlane(t.Context()), testTenantID, admin.ID(), service.CommonMember{
			Email: "attacker@evil.example", DisplayName: "Admin", TenantRole: model.TenantRoleMember, Status: model.StatusActive,
		})
		require.ErrorIs(t, err, port.ErrPlatformAdminProtected)
	})
}

// TestManagedSignIn: when the control plane owns members, a sign-in links an
// existing member without copying the provider's profile, and creates none
// but the default admins.
func TestManagedSignIn(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		newIdentityStore(t, db)
		store := xologorm.NewStore(db, xologorm.WithIdentityIssuers(func() model.IdentityIssuers { return testIssuers }), xologorm.WithOwnership(model.OwnershipPolicy{model.FamilyMember: model.OwnerControlPlane}))
		svc := newIdentityService(store)
		resolver := service.NewIdentityResolver(store, store, store)
		managed := service.LoginPolicy{AutoCreate: true, ActiveByDefault: true, Managed: true, DefaultAdmins: []string{"root@example.test"}}

		alice := model.NewUserID()
		_, err := svc.PutTenantMember(controlPlane(ctx), testTenantID, alice, service.CommonMember{
			Email: "alice@example.test", DisplayName: "Alice", TenantRole: model.TenantRoleMember, Status: model.StatusActive, Identity: declare("alice"),
		})
		require.NoError(t, err)
		declared := memberProjection(t, store, testTenantID, alice)

		proof := signIn("alice", "alice@idp.example.test", true)
		proof.DisplayName = "Alice From IdP"
		user, err := resolver.Resolve(ctx, testTenantID, proof, managed)
		require.NoError(t, err, "the sign-in links its own account")
		require.Equal(t, alice, user.ID())
		stored, err := store.GetUserByID(ctx, alice)
		require.NoError(t, err)
		require.Equal(t, "oidc", stored.Provider())
		require.Equal(t, "alice@example.test", stored.Email(), "the declared profile is kept")
		require.Equal(t, "Alice", stored.DisplayName())
		require.Equal(t, declared, memberProjection(t, store, testTenantID, alice))

		audits := auditCount(t, db)
		_, err = resolver.Resolve(ctx, testTenantID, proof, managed)
		require.NoError(t, err)
		require.Equal(t, audits, auditCount(t, db), "a sign-in that changes nothing writes nothing")

		_, err = resolver.Resolve(ctx, testTenantID, signIn("stranger", "stranger@example.test", true), managed)
		require.ErrorIs(t, err, service.ErrAccountCreationDisabled, "a managed instance creates no member")

		root, err := resolver.Resolve(ctx, testTenantID, signIn("root", "root@example.test", true), managed)
		require.NoError(t, err, "default admins bootstrap the instance")
		require.Contains(t, root.Roles(), model.PlatformRoleAdmin)

		// The exemption is the signed-in account only.
		other, err := store.GetUserByID(ctx, alice)
		require.NoError(t, err)
		next := model.CopyUser(other)
		next.SetDisplayName("Changed")
		require.ErrorIs(t, store.SaveUser(model.WithSignInAccount(ctx, root.ID()), next), port.ErrOwnershipDenied)
	})
}
