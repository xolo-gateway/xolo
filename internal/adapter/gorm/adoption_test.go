package gorm_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/adoption"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	gormpkg "gorm.io/gorm"
)

// TestAdoptionExport exports the whole inventory and resumes it from its
// cursor: a write committed after the export is exactly the feed after c0.
func TestAdoptionExport(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := newSeededStore(t, db)
		fixture := newOwnershipFixture(t, store)
		for range 3 {
			newOwnershipFixture(t, store)
		}
		// Pages of two cross every family boundary and split families.
		t.Cleanup(xologorm.SetInventoryBatchSize(2))

		var out bytes.Buffer
		require.NoError(t, adoption.Export(ctx, store, &out))
		summary, err := adoption.Verify(bytes.NewReader(out.Bytes()))
		require.NoError(t, err)
		var projections int64
		require.NoError(t, db.Model(&xologorm.ProvisioningProjection{}).Count(&projections).Error)
		require.EqualValues(t, projections, summary.Count, "every projection is exported")
		require.Equal(t, 4, summary.Families[model.FamilyOrganization])
		require.Equal(t, 4, summary.Families[model.FamilyOrganizationMembership])

		events, _ := feedSince(t, store, summary.Cursor)
		require.Empty(t, events, "the export misses nothing before its cursor")

		org, err := store.GetOrgByID(ctx, fixture.org)
		require.NoError(t, err)
		require.NoError(t, store.SaveOrg(ctx, model.UpdateOrganization(org, model.WithOrgName("Renamed"))))
		events, _ = feedSince(t, store, summary.Cursor)
		require.Equal(t, []string{model.CommonEventType(model.FamilyOrganization, model.CommonEventUpdated)}, eventTypes(events))
	})
}

// TestDetachControlPlane removes the subscriptions of the control plane and
// nothing else, only when an administrator can still sign in.
func TestDetachControlPlane(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := newSeededStore(t, db)
		newOwnershipFixture(t, store)
		putHook(t, store, testTenantID)
		operator := model.WithActor(ctx, model.Actor{URI: "urn:xolo:operator:1000"})

		_, err := store.DetachControlPlane(model.WithActor(ctx, model.Actor{UserID: model.NewUserID()}))
		require.ErrorIs(t, err, port.ErrNotAllowed, "only an operator detaches")

		// An administrator without sign-in link cannot sign in.
		unlinked := model.NewUser(testTenantID, "", "", "admin@example.test", "Admin", true, model.PlatformRoleUser, model.PlatformRoleAdmin)
		require.NoError(t, store.SaveUser(ctx, unlinked))
		_, err = store.DetachControlPlane(operator)
		require.ErrorIs(t, err, xologorm.ErrNoUsableAdmin)

		admin := model.NewUser(testTenantID, "oidc", "admin", "root@example.test", "Root", true, model.PlatformRoleUser, model.PlatformRoleAdmin)
		require.NoError(t, store.SaveUser(ctx, admin))

		var before []xologorm.ProvisioningProjection
		require.NoError(t, db.Order("family, tenant_id, org_id, resource_key").Find(&before).Error)
		events := eventCount(t, db)
		audits := auditCount(t, db)

		require.NoError(t, db.Callback().Create().Before("gorm:create").Register("detach-failure", func(tx *gormpkg.DB) {
			if tx.Statement.Table == "mutation_audits" {
				tx.AddError(errors.New("injected detach failure"))
			}
		}))
		_, err = store.DetachControlPlane(operator)
		require.Error(t, err)
		require.NoError(t, db.Callback().Create().Remove("detach-failure"))
		hooks, err := store.ListWebhooks(ctx, testTenantID)
		require.NoError(t, err)
		require.Len(t, hooks, 1, "a failed detachment removes nothing")

		report, err := store.DetachControlPlane(operator)
		require.NoError(t, err)
		require.EqualValues(t, 1, report.SubscriptionsRemoved)
		hooks, err = store.ListWebhooks(ctx, testTenantID)
		require.NoError(t, err)
		require.Empty(t, hooks)

		var after []xologorm.ProvisioningProjection
		require.NoError(t, db.Order("family, tenant_id, org_id, resource_key").Find(&after).Error)
		require.Equal(t, before, after, "projections, keys and ETags are untouched")
		require.Equal(t, events, eventCount(t, db), "the feed is untouched")
		require.Equal(t, audits+1, auditCount(t, db))
		stored, err := store.GetUserByID(ctx, admin.ID())
		require.NoError(t, err)
		require.Equal(t, "oidc", stored.Provider())
		require.Equal(t, "admin", stored.Subject())
	})
}
