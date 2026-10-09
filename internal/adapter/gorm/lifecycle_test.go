package gorm_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	gormpkg "gorm.io/gorm"
)

// lifecycleStore opens a store of db recording deletions, with its guards.
func lifecycleStore(t *testing.T, db *gormpkg.DB, options ...xologorm.StoreOption) *xologorm.Store {
	t.Helper()
	store := xologorm.NewStore(db, append([]xologorm.StoreOption{xologorm.WithLifecycle(true, 24*time.Hour)}, options...)...)
	require.NoError(t, store.Migrate(context.Background()))
	require.NoError(t, store.PrepareLifecycle(context.Background(), true))
	return store
}

func orgScope(tenant model.TenantID) model.CommonScope {
	return model.CommonScope{Family: model.FamilyOrganization, TenantID: string(tenant)}
}

func memberScope(tenant model.TenantID) model.CommonScope {
	return model.CommonScope{Family: model.FamilyMember, TenantID: string(tenant)}
}

var tenantScope = model.CommonScope{Family: model.FamilyTenant}

// guardCount counts the lifecycle triggers installed.
func guardCount(t *testing.T, db *gormpkg.DB) int64 {
	t.Helper()
	var n int64
	if db.Dialector.Name() == "postgres" {
		require.NoError(t, db.Raw("SELECT COUNT(*) FROM pg_trigger WHERE tgname = 'xolo_lifecycle_guard'").Scan(&n).Error)
	} else {
		require.NoError(t, db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name LIKE 'xolo_lifecycle_%'").Scan(&n).Error)
	}
	return n
}

// TestLifecycleGuardsActivation installs the guards only when the lifecycle
// is enabled, and keeps them while a deletion is recorded.
func TestLifecycleGuardsActivation(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		base := newSeededStore(t, db)
		fixture := newOwnershipFixture(t, base)
		require.NoError(t, base.PrepareLifecycle(ctx, false))
		require.Zero(t, guardCount(t, db), "never enabled: no trigger")

		store := lifecycleStore(t, db)
		guards := guardCount(t, db)
		require.NotZero(t, guards)
		require.NoError(t, store.PrepareLifecycle(ctx, true))
		require.Equal(t, guards, guardCount(t, db), "installed again in place")
		require.NoError(t, store.PrepareLifecycle(ctx, false))
		require.Zero(t, guardCount(t, db), "disabled without deletion: removed")

		require.NoError(t, store.PrepareLifecycle(ctx, true))
		_, err := store.FreezeResource(ctx, orgScope(testTenantID), string(fixture.org), model.MatchCondition{})
		require.NoError(t, err)
		require.NoError(t, store.PrepareLifecycle(ctx, false))
		require.NotZero(t, guardCount(t, db), "disabled with a deletion: kept")
		installed, err := store.LifecycleGuardsInstalled(ctx)
		require.NoError(t, err)
		require.True(t, installed)

		disabled := xologorm.NewStore(db)
		_, err = disabled.FreezeResource(ctx, orgScope(testTenantID), string(newOwnershipFixture(t, base).org), model.MatchCondition{})
		require.ErrorIs(t, err, port.ErrLifecycleDisabled)
	})
}

// TestFreezeResource freezes an organization, a member and a tenant: each is
// suspended, published, and its deletion is recorded once.
func TestFreezeResource(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		base := newSeededStore(t, db)
		fixture := newOwnershipFixture(t, base)
		store := lifecycleStore(t, db)
		cursor, err := store.CaptureEventCursor(ctx)
		require.NoError(t, err)

		_, err = store.FreezeResource(ctx, orgScope(testTenantID), string(fixture.org), ifMatch(t, `W/"1"`))
		require.ErrorIs(t, err, port.ErrPreconditionFailed)
		current, err := store.ReadProjection(ctx, orgScope(testTenantID), string(fixture.org))
		require.NoError(t, err)
		deletion, err := store.FreezeResource(ctx, orgScope(testTenantID), string(fixture.org), ifMatch(t, current.ETag))
		require.NoError(t, err)
		require.Equal(t, string(testTenantID), deletion.TenantID)
		require.WithinDuration(t, deletion.DeletedAt.Add(24*time.Hour), deletion.PurgeAfter, time.Second)

		item, err := store.ReadProjection(ctx, orgScope(testTenantID), string(fixture.org))
		require.NoError(t, err)
		require.NotEqual(t, current.ETag, item.ETag, "the suspension moved the ETag")
		var rep map[string]any
		require.NoError(t, json.Unmarshal(item.Representation, &rep))
		require.Equal(t, "suspended", rep["status"])
		events, _ := feedSince(t, store, cursor)
		require.Equal(t, []string{"organization.updated.v1"}, eventTypes(events))

		again, err := store.FreezeResource(ctx, orgScope(testTenantID), string(fixture.org), model.MatchCondition{})
		require.NoError(t, err)
		require.Equal(t, deletion, again, "a repeated freeze returns the recorded deletion")
		retried, err := store.FreezeResource(ctx, orgScope(testTenantID), string(fixture.org), ifMatch(t, current.ETag))
		require.NoError(t, err)
		require.Equal(t, deletion, retried, "a retry carrying the original If-Match")
		read, err := store.ReadDeletion(ctx, orgScope(testTenantID), string(fixture.org))
		require.NoError(t, err)
		require.Equal(t, deletion, read)
		_, err = store.ReadDeletion(ctx, orgScope(model.NewTenantID()), string(fixture.org))
		require.ErrorIs(t, err, port.ErrNotFound)

		// A member: its account is deactivated.
		member := newOwnershipFixture(t, base)
		_, err = store.FreezeResource(ctx, memberScope(testTenantID), string(member.user), model.MatchCondition{})
		require.NoError(t, err)
		user, err := store.GetUserByID(ctx, member.user)
		require.NoError(t, err)
		require.False(t, user.Active())

		// A tenant: its organizations can not be frozen anymore, they are.
		tenant := model.NewTenant("frozen", "Frozen", "")
		require.NoError(t, base.CreateTenant(ctx, tenant))
		org := model.NewOrganization(tenant.ID(), "inside", "Inside", "")
		require.NoError(t, base.CreateOrg(ctx, org))
		_, err = store.FreezeResource(ctx, tenantScope, string(tenant.ID()), model.MatchCondition{})
		require.NoError(t, err)
		_, err = store.FreezeResource(ctx, orgScope(tenant.ID()), string(org.ID()), model.MatchCondition{})
		require.ErrorIs(t, err, port.ErrResourceDeleted)

		defaultTenant, err := store.GetTenantBySlug(ctx, model.DefaultTenantSlug)
		require.NoError(t, err)
		_, err = store.FreezeResource(ctx, tenantScope, string(defaultTenant.ID()), model.MatchCondition{})
		require.ErrorIs(t, err, port.ErrNotAllowed)
		_, err = store.FreezeResource(ctx, orgScope(tenant.ID()), string(fixture.org), model.MatchCondition{})
		require.ErrorIs(t, err, port.ErrNotFound, "an organization of another tenant")
	})
}

// TestFreezeKeepsOwnersAndAuthority refuses the freeze of a last owner, and
// of a scope holding a family of another authority, as a whole.
func TestFreezeKeepsOwnersAndAuthority(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		base := newSeededStore(t, db)
		fixture := newOwnershipFixture(t, base)
		store := lifecycleStore(t, db)

		// The only owner of the organization.
		soleOwner := func(f ownershipFixture) {
			membership, err := base.GetUserOrgMembership(ctx, f.user, f.org)
			require.NoError(t, err)
			roles, err := base.ListOrgRoles(ctx, f.org)
			require.NoError(t, err)
			for _, role := range roles {
				if role.BuiltinKind() == model.BuiltinKindOwner {
					require.NoError(t, base.SetMembershipRoles(ctx, membership.ID(), []model.RoleID{role.ID()}))
				}
			}
		}
		soleOwner(fixture)
		_, err := store.FreezeResource(ctx, memberScope(testTenantID), string(fixture.user), model.MatchCondition{})
		require.ErrorIs(t, err, port.ErrLastOwner)
		user, err := base.GetUserByID(ctx, fixture.user)
		require.NoError(t, err)
		require.True(t, user.Active(), "nothing changed")

		// A sole owner already deactivated: its organization has no active
		// owner left to protect.
		inactive := newOwnershipFixture(t, base)
		soleOwner(inactive)
		user, err = base.GetUserByID(ctx, inactive.user)
		require.NoError(t, err)
		next := model.CopyUser(user)
		next.SetActive(false)
		require.NoError(t, base.SaveUser(ctx, next))
		_, err = store.FreezeResource(ctx, memberScope(testTenantID), string(inactive.user), model.MatchCondition{})
		require.NoError(t, err)

		provider := model.NewProvider(fixture.org, "OpenAI", "openai", "", "ciphertext", "EUR")
		require.NoError(t, base.CreateProvider(ctx, provider))
		owned := lifecycleStore(t, db, xologorm.WithOwnership(model.OwnershipPolicy{model.FamilyProvider: model.OwnerControlPlane}))
		_, err = owned.FreezeResource(ctx, orgScope(testTenantID), string(fixture.org), model.MatchCondition{})
		require.ErrorIs(t, err, port.ErrOwnershipDenied)
		_, err = base.ReadDeletion(ctx, orgScope(testTenantID), string(fixture.org))
		require.ErrorIs(t, err, port.ErrNotFound)
		_, err = owned.FreezeResource(model.WithWriteAuthority(ctx, model.OwnerControlPlane), orgScope(testTenantID), string(fixture.org), model.MatchCondition{})
		require.NoError(t, err)
	})
}

// TestFreezeProtectsScope refuses every write of a frozen scope, through the
// stores as well as raw SQL, and leaves the other scopes alone.
func TestFreezeProtectsScope(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		base := newSeededStore(t, db)
		fixture := newOwnershipFixture(t, base)
		other := newOwnershipFixture(t, base)
		member := newOwnershipFixture(t, base)
		store := lifecycleStore(t, db)
		_, err := store.FreezeResource(ctx, orgScope(testTenantID), string(fixture.org), model.MatchCondition{})
		require.NoError(t, err)
		_, err = store.FreezeResource(ctx, memberScope(testTenantID), string(member.user), model.MatchCondition{})
		require.NoError(t, err)

		usage := func(org model.OrgID, user model.UserID) error {
			return store.RecordUsage(ctx, model.NewUsageRecord(user, "", org, "", "", "model", "", 1, 0, 1, 10, "EUR", model.CostSourceComputed, "model"))
		}
		writes := map[string]func(org model.OrgID, user model.UserID) error{
			"usage": usage,
			"event": func(org model.OrgID, _ model.UserID) error {
				return store.RecordEvent(ctx, model.NewEvent(model.EventSourcePlatform, "test.event", model.WithEventOrg(org)))
			},
			"quota": func(org model.OrgID, _ model.UserID) error {
				return store.SetQuota(ctx, model.NewQuota(model.QuotaScopeOrg, string(org), "EUR", nil, nil, nil))
			},
			"application": func(org model.OrgID, _ model.UserID) error {
				return store.CreateApplication(ctx, model.NewApplication(org, "App", "", true))
			},
			"secret": func(org model.OrgID, _ model.UserID) error {
				return store.SetSecret(ctx, string(org), "plugin", "node", "key", "value")
			},
			"raw role": func(org model.OrgID, _ model.UserID) error {
				return db.Exec("INSERT INTO roles (id, org_id, name, builtin) VALUES (?, ?, ?, ?)", uuid.NewString(), string(org), "Raw "+uuid.NewString()[:8], false).Error
			},
		}
		for name, write := range writes {
			err := write(fixture.org, other.user)
			require.Error(t, err, name)
			if name != "raw role" {
				require.ErrorIs(t, err, port.ErrResourceDeleted, name)
			}
			require.NoError(t, write(other.org, other.user), name+" elsewhere")
		}
		// A user quota belongs to the member, not to its organizations: the
		// freeze of one of them leaves it writable, the member's does not.
		require.NoError(t, store.SetQuota(ctx, model.NewQuota(model.QuotaScopeUser, string(fixture.user), "EUR", nil, nil, nil)))
		require.ErrorIs(t, store.SetQuota(ctx, model.NewQuota(model.QuotaScopeUser, string(member.user), "EUR", nil, nil, nil)), port.ErrResourceDeleted)

		org, err := store.GetOrgByID(ctx, fixture.org)
		require.NoError(t, err)
		require.ErrorIs(t, store.SaveOrg(ctx, model.UpdateOrganization(org, model.WithOrgActive(true))), port.ErrResourceDeleted, "a freeze can not be undone")
		require.ErrorIs(t, store.DeleteOrg(ctx, fixture.org), port.ErrResourceDeleted)

		// The frozen member: its own rows and its usage anywhere.
		require.ErrorIs(t, usage(other.org, member.user), port.ErrResourceDeleted)
		require.ErrorIs(t, store.SetSecret(ctx, "~:"+string(member.user), "plugin", "node", "key", "value"), port.ErrResourceDeleted)
		user, err := store.GetUserByID(ctx, member.user)
		require.NoError(t, err)
		next := model.CopyUser(user)
		next.SetDisplayName("Changed")
		require.ErrorIs(t, store.SaveUser(ctx, next), port.ErrResourceDeleted)
	})
}

// TestFreezeKeepsEvictingOrganization: the events of a frozen member stay,
// and the eviction trims those of the other members of its organization.
func TestFreezeKeepsEvictingOrganization(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		base := newSeededStore(t, db)
		fixture := newOwnershipFixture(t, base)
		store := lifecycleStore(t, db)
		peer := model.NewUser(testTenantID, "oidc", uuid.NewString(), uuid.NewString()[:8]+"@example.test", "Peer", true, model.PlatformRoleUser)
		require.NoError(t, base.SaveUser(ctx, peer))
		require.NoError(t, base.AddMember(ctx, model.NewMembership(peer.ID(), fixture.org)))

		// The frozen member's events are the oldest: none is in the keep-set.
		record := func(user model.UserID, n int) {
			for range n {
				require.NoError(t, store.RecordEvent(ctx, model.NewEvent(model.EventSourcePlatform, model.EventTypeProxyRequest, model.WithEventOrg(fixture.org), model.WithEventUser(user))))
			}
		}
		record(fixture.user, 3)
		record(peer.ID(), 6)
		_, err := store.FreezeResource(ctx, memberScope(testTenantID), string(fixture.user), model.MatchCondition{})
		require.NoError(t, err)

		orgs, err := store.ListEventOrgIDs(ctx)
		require.NoError(t, err)
		require.Contains(t, orgs, fixture.org)
		deleted, err := store.EvictOverflow(ctx, fixture.org, 2)
		require.NoError(t, err)
		require.EqualValues(t, 4, deleted, "9 events, 2 kept, 3 of the frozen member")

		count := func(user model.UserID) int64 {
			var n int64
			require.NoError(t, db.Table("events").Where("org_id = ? AND user_id = ?", string(fixture.org), string(user)).Count(&n).Error)
			return n
		}
		require.EqualValues(t, 3, count(fixture.user))
		require.EqualValues(t, 2, count(peer.ID()))
	})
}

// TestFreezeTenantStopsWebhooks leaves the webhooks of a frozen tenant
// undelivered and its subscriptions untouched.
func TestFreezeTenantStopsWebhooks(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		base := newSeededStore(t, db)
		store := lifecycleStore(t, db)
		tenant := newWebhookTenant(t, base, "hooked")
		hook := putHook(t, base, tenant)
		touchTenant(t, base, tenant)

		_, err := store.FreezeResource(ctx, tenantScope, string(tenant), model.MatchCondition{})
		require.NoError(t, err)
		require.NoError(t, store.PrepareWebhooks(ctx, roomyWebhooks, 100))
		job, err := store.ClaimWebhook(ctx)
		require.NoError(t, err)
		require.Nil(t, job, "a frozen tenant is not delivered")

		_, err = store.PutWebhook(model.WithWriteAuthority(ctx, model.OwnerControlPlane), tenant, hook.ID, model.WebhookSettings{Destination: hook.Destination, Events: []string{"*"}, Enabled: false})
		require.ErrorIs(t, err, port.ErrResourceDeleted)
		require.ErrorIs(t, store.DeleteWebhook(ctx, tenant, hook.ID), port.ErrResourceDeleted)

		// Detaching keeps the subscriptions of a frozen tenant: they deliver
		// nothing, and its purge removes them.
		admin := model.NewUser(testTenantID, "oidc", "root", "root@example.test", "Root", true, model.PlatformRoleUser, model.PlatformRoleAdmin)
		require.NoError(t, base.SaveUser(ctx, admin))
		report, err := store.DetachControlPlane(model.WithActor(ctx, model.Actor{URI: "urn:xolo:operator:1000"}))
		require.NoError(t, err)
		require.Zero(t, report.SubscriptionsRemoved)
	})
}

// TestInactiveOrganizationGrantsNothing: a suspended or frozen organization
// grants no membership and no permission.
func TestInactiveOrganizationGrantsNothing(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := newSeededStore(t, db)
		fixture := newOwnershipFixture(t, store)
		app := model.NewApplication(fixture.org, "App", "", true)
		require.NoError(t, store.CreateApplication(ctx, app))
		role := model.NewRole(fixture.org, "Reader", "")
		role.SetPermissions([]string{string(rbac.PermUsageRead)})
		require.NoError(t, store.CreateRole(ctx, role))
		require.NoError(t, store.SetApplicationRoles(ctx, app.ID(), []model.RoleID{role.ID()}))
		permissions, err := store.ResolveApplicationPermissions(ctx, app.ID(), fixture.org)
		require.NoError(t, err)
		require.True(t, permissions.Has(rbac.PermUsageRead))

		memberships, err := store.GetUserMemberships(ctx, fixture.user)
		require.NoError(t, err)
		require.Len(t, memberships, 1)

		org, err := store.GetOrgByID(ctx, fixture.org)
		require.NoError(t, err)
		require.NoError(t, store.SaveOrg(ctx, model.UpdateOrganization(org, model.WithOrgActive(false))))
		memberships, err = store.GetUserMemberships(ctx, fixture.user)
		require.NoError(t, err)
		require.Empty(t, memberships)
		member, err := store.IsMember(ctx, fixture.user, fixture.org)
		require.NoError(t, err)
		require.False(t, member)
		permissions, err = store.ResolveApplicationPermissions(ctx, app.ID(), fixture.org)
		require.NoError(t, err)
		require.False(t, permissions.Has(rbac.PermUsageRead))
	})
}

// TestFreezeRaces: a write of the scope racing a freeze either commits
// before it, or fails with ErrResourceDeleted; once the freeze is committed,
// every write fails. The refusal of a write in flight is opportunistic: every
// writer may finish before the freeze commits. The write after the freeze is
// the deterministic case.
func TestFreezeRaces(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		base := newSeededStore(t, db)
		store := lifecycleStore(t, db)

		// Provisioning writes, which lock their parents, against an
		// organization freeze. A writer whose snapshot predates the freeze
		// is replayed and sees it.
		fixture := newOwnershipFixture(t, base)
		var wg sync.WaitGroup
		results := make(chan error, 10)
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results <- store.WithProvisioningTransaction(context.Background(), func(tx port.ProvisioningTx) error {
					if _, err := tx.GetOrgByID(ctx, fixture.org); err != nil {
						return err
					}
					return tx.CreateRole(ctx, model.NewRole(fixture.org, "Race "+uuid.NewString()[:8], ""))
				})
			}()
		}
		_, err := store.FreezeResource(ctx, orgScope(testTenantID), string(fixture.org), model.MatchCondition{})
		require.NoError(t, err)
		wg.Wait()
		close(results)
		committed := countCommitted(t, results)
		var roles int64
		require.NoError(t, db.Table("roles").Where("org_id = ? AND name LIKE ?", string(fixture.org), "Race %").Count(&roles).Error)
		require.EqualValues(t, committed, roles, "every role written committed before the freeze")
		require.ErrorIs(t, base.CreateRole(ctx, model.NewRole(fixture.org, "After", "")), port.ErrResourceDeleted)

		// Usage, written at read committed, against a member freeze.
		member := newOwnershipFixture(t, base)
		usage := func() error {
			return store.RecordUsage(context.Background(), model.NewUsageRecord(member.user, "", member.org, "", "", "model", "", 1, 0, 1, 10, "EUR", model.CostSourceComputed, "model"))
		}
		results = make(chan error, 20)
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results <- usage()
			}()
		}
		_, err = store.FreezeResource(ctx, memberScope(testTenantID), string(member.user), model.MatchCondition{})
		require.NoError(t, err)
		wg.Wait()
		close(results)
		committed = countCommitted(t, results)
		var records int64
		require.NoError(t, db.Table("usage_records").Where("user_id = ?", string(member.user)).Count(&records).Error)
		require.EqualValues(t, committed, records)
		require.ErrorIs(t, usage(), port.ErrResourceDeleted, "no usage lands after the freeze")
	})
}

// countCommitted counts the writes that succeeded; the others must have been
// refused by the freeze.
func countCommitted(t *testing.T, results chan error) int {
	t.Helper()
	committed := 0
	for err := range results {
		if err == nil {
			committed++
			continue
		}
		require.ErrorIs(t, err, port.ErrResourceDeleted)
	}
	return committed
}

// TestFreezeLocksOnlyItsScope: on PostgreSQL, a freeze in progress holds the
// writes of its own scope only.
func TestFreezeLocksOnlyItsScope(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		if db.Dialector.Name() != "postgres" {
			t.Skip("SQLite serializes every writer")
		}
		ctx := t.Context()
		base := newSeededStore(t, db)
		fixture := newOwnershipFixture(t, base)
		lifecycleStore(t, db)
		foreign := model.NewTenant("elsewhere", "Elsewhere", "")
		require.NoError(t, base.CreateTenant(ctx, foreign))
		elsewhere := model.NewOrganization(foreign.ID(), "elsewhere", "Elsewhere", "")
		require.NoError(t, base.CreateOrg(ctx, elsewhere))

		// A freeze in progress: the frozen row is locked, the deletion
		// recorded, nothing committed yet.
		freezing := db.Begin()
		require.NoError(t, freezing.Exec("SELECT id FROM tenants WHERE id = ? FOR UPDATE", string(testTenantID)).Error)
		require.NoError(t, freezing.Create(&xologorm.ResourceDeletion{Family: model.FamilyTenant, ResourceID: string(testTenantID), TenantID: string(testTenantID), DeletedAt: time.Now(), PurgeAfter: time.Now()}).Error)

		// Another tenant writes freely.
		other := make(chan error, 1)
		go func() {
			other <- base.CreateRole(context.Background(), model.NewRole(elsewhere.ID(), "Free", ""))
		}()
		select {
		case err := <-other:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("a freeze blocked another tenant")
		}

		// The frozen tenant waits, then fails.
		blocked := make(chan error, 1)
		go func() {
			blocked <- base.CreateRole(context.Background(), model.NewRole(fixture.org, "Blocked", ""))
		}()
		select {
		case err := <-blocked:
			t.Fatalf("the write did not wait for the freeze: %v", err)
		case <-time.After(300 * time.Millisecond):
		}
		require.NoError(t, freezing.Commit().Error)
		require.ErrorIs(t, <-blocked, port.ErrResourceDeleted)
	})
}
