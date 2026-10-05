package gorm_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/eventql"
	"github.com/xolo-gateway/xolo/internal/core/model"
	gormpkg "gorm.io/gorm"
)

func seedLegacyRecovery(t *testing.T, db *gormpkg.DB) {
	t.Helper()
	require.NoError(t, adapter.NewStore(db).Migrate(t.Context()))
	require.NoError(t, db.Create(&adapter.Tenant{ID: "tenant-acme", Slug: "acme", Name: "Acme", Active: 1}).Error)
	require.NoError(t, db.Create(&adapter.Organization{ID: "org-acme", TenantID: "tenant-acme", Slug: "acme", Name: "Acme", Active: 1}).Error)
	require.NoError(t, db.Create(&adapter.User{ID: "user-alice", TenantID: "tenant-acme", Email: "Alice@Example.test", Active: true}).Error)
	require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", "202610020001").Error)
}

func TestRecoveryRewritesQueriesGraphsAndHistoricalActors(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		seedLegacyRecovery(t, db)
		query := `{user="user-alice", org!="org-acme"} | actor_id="user-alice" | user_id="user-alice"`
		require.NoError(t, db.Create(&adapter.Alert{ID: "alert", OrgID: "org-acme", OwnerID: "user-alice", Query: query}).Error)
		graph := `{"nodes":[{"id":"user-alice","type":"value","data":{"portType":"string","value":"user-alice","large":9007199254740993}},{"id":"plugin","type":"plugin","data":{"config":{"org_id":"org-acme","tenant_id":"tenant-acme"}}}],"edges":[{"id":"edge","source":"user-alice","target":"plugin"}]}`
		require.NoError(t, db.Create(&adapter.VirtualModel{ID: "vm", OrgID: "org-acme", Name: "vm", GraphJSON: graph}).Error)
		require.NoError(t, db.Table("events").Create(map[string]any{"id": "event", "org_id": "org-acme", "user_id": "user-alice", "attributes": `{"actor_id":"user-alice","message":"literal user-alice"}`}).Error)
		require.NoError(t, db.Create(&adapter.PluginNodeSecret{ID: "oauth", OrgID: "~:user-alice", PluginName: "mcp-bridge", NodeID: "node", Key: "oauth:user-alice", ValueEncrypted: "unchanged"}).Error)
		a, err := adapter.PlanCommonRecovery(t.Context(), db)
		require.NoError(t, err)
		report, err := adapter.DiagnoseCommonRecovery(t.Context(), db, a)
		require.NoError(t, err)
		require.Empty(t, report.Issues)
		var original adapter.Alert
		require.NoError(t, db.First(&original, "id = ?", "alert").Error)
		require.Equal(t, query, original.Query)
		require.NoError(t, adapter.MigrateDatabase(t.Context(), db, a))
		uid, oid := a.IDs["users"]["user-alice"], a.IDs["organizations"]["org-acme"]
		var alert adapter.Alert
		require.NoError(t, db.First(&alert, "id = ?", "alert").Error)
		require.NotContains(t, alert.Query, "user-alice")
		require.Equal(t, uid, alert.OwnerID)
		require.Equal(t, oid, alert.OrgID)
		compiled, err := eventql.Compile(alert.Query)
		require.NoError(t, err)
		require.True(t, compiled.Match(eventql.Fields{User: uid, Org: "other", Attributes: map[string]string{"actor_id": uid, "user_id": uid}}))
		var vm adapter.VirtualModel
		require.NoError(t, db.First(&vm, "id = ?", "vm").Error)
		require.Contains(t, vm.GraphJSON, `"large":9007199254740993`)
		require.Contains(t, vm.GraphJSON, `"id":"user-alice"`)
		require.Contains(t, vm.GraphJSON, `"source":"user-alice"`)
		require.Contains(t, vm.GraphJSON, `"value":"`+uid+`"`)
		var event adapter.Event
		require.NoError(t, db.First(&event, "id = ?", "event").Error)
		require.Equal(t, uid, (*event.Attributes.Val)["actor_id"])
		require.Equal(t, "literal user-alice", (*event.Attributes.Val)["message"])
		var secret adapter.PluginNodeSecret
		require.NoError(t, db.First(&secret, "id = ?", "oauth").Error)
		require.Equal(t, "oauth:"+uid, secret.Key)
		require.Equal(t, "~:"+uid, secret.OrgID)
		require.Equal(t, "unchanged", secret.ValueEncrypted)
		require.NoError(t, adapter.MigrateDatabase(t.Context(), db, a))
		plannedAgain, err := adapter.PlanCommonRecovery(t.Context(), db)
		require.NoError(t, err)
		require.Equal(t, a.IDs, plannedAgain.IDs)
		repeated, err := adapter.DiagnoseCommonRecovery(t.Context(), db, plannedAgain)
		require.NoError(t, err)
		require.Equal(t, report.Digest, repeated.Digest)
		require.NoError(t, adapter.NewStore(db, adapter.WithAutoMigrate(false)).CheckSchema(t.Context()))
	})
}

func TestRecoveryEmailCollisionAndExplicitResolution(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		seedLegacyRecovery(t, db)
		require.NoError(t, db.Create(&adapter.User{ID: "user-other", TenantID: "tenant-acme", Email: "alice@example.test", Active: true}).Error)
		a, err := adapter.PlanCommonRecovery(t.Context(), db)
		require.NoError(t, err)
		report, err := adapter.DiagnoseCommonRecovery(t.Context(), db, a)
		require.NoError(t, err)
		require.Contains(t, strings.Join(report.Issues, " "), "user-alice and user-other")
		require.ErrorContains(t, adapter.MigrateDatabase(t.Context(), db, a), "normalized email collision")
		var original adapter.User
		require.NoError(t, db.First(&original, "id = ?", "user-alice").Error)
		require.Equal(t, "Alice@Example.test", original.Email)
		a.EmailOverrides["user-other"] = " Other@example.test "
		report, err = adapter.DiagnoseCommonRecovery(t.Context(), db, a)
		require.NoError(t, err)
		require.Empty(t, report.Issues)
		require.NoError(t, adapter.MigrateDatabase(t.Context(), db, a))
		var users []adapter.User
		require.NoError(t, db.Where("tenant_id = ?", a.IDs["tenants"]["tenant-acme"]).Order("email").Find(&users).Error)
		require.Len(t, users, 2)
		require.Equal(t, "alice@example.test", users[0].Email)
		require.Equal(t, "other@example.test", users[1].Email)
	})
}

func TestRecoveryOpaqueReferenceAndStaleOverride(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		seedLegacyRecovery(t, db)
		graph := `{"nodes":[{"id":"n","type":"plugin","data":{"config":{"script":"lookup(user-alice)"}}}],"edges":[]}`
		require.NoError(t, db.Create(&adapter.VirtualModel{ID: "vm", OrgID: "org-acme", Name: "vm", GraphJSON: graph}).Error)
		a, err := adapter.PlanCommonRecovery(t.Context(), db)
		require.NoError(t, err)
		report, err := adapter.DiagnoseCommonRecovery(t.Context(), db, a)
		require.NoError(t, err)
		require.Contains(t, strings.Join(report.Issues, " "), "virtual_models[vm].graph_json: opaque legacy reference at /nodes/0/data/config/script")
		a.SerializedOverrides = []adapter.RecoverySerializedOverride{{Table: "virtual_models", ID: "vm", Column: "graph_json", Before: graph, After: strings.ReplaceAll(graph, "user-alice", a.IDs["users"]["user-alice"])}}
		report, err = adapter.DiagnoseCommonRecovery(t.Context(), db, a)
		require.NoError(t, err)
		require.Empty(t, report.Issues)
		require.NoError(t, db.Model(&adapter.VirtualModel{}).Where("id = ?", "vm").Update("graph_json", graph+" ").Error)
		require.ErrorContains(t, adapter.MigrateDatabase(t.Context(), db, a), "serialized override is stale")
		require.NoError(t, db.Model(&adapter.VirtualModel{}).Where("id = ?", "vm").Update("graph_json", graph).Error)
		require.NoError(t, adapter.MigrateDatabase(t.Context(), db, a))
		var vm adapter.VirtualModel
		require.NoError(t, db.First(&vm, "id = ?", "vm").Error)
		require.Equal(t, a.SerializedOverrides[0].After, vm.GraphJSON)
		changed := *a
		changed.EmailOverrides = map[string]string{"user-alice": "wrong@example.test"}
		require.ErrorContains(t, adapter.MigrateDatabase(t.Context(), db, &changed), "different artifact")
	})
}

func TestManualMigrationNeverWritesOnStoreAccess(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := adapter.NewStore(db, adapter.WithAutoMigrate(false))
		_, err := store.GetTenantBySlug(ctx, model.DefaultTenantSlug)
		require.ErrorContains(t, err, "not initialized")
		tables, err := db.Migrator().GetTables()
		require.NoError(t, err)
		require.Empty(t, tables)
		require.NoError(t, store.Migrate(ctx))
		_, err = store.GetTenantBySlug(ctx, model.DefaultTenantSlug)
		require.NoError(t, err)
		require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", "202610020001").Error)
		store = adapter.NewStore(db, adapter.WithAutoMigrate(false))
		_, err = store.GetTenantBySlug(ctx, model.DefaultTenantSlug)
		require.ErrorContains(t, err, "pending migration 202610020001")
		var count int64
		require.NoError(t, db.Table("migrations").Where("id = ?", "202610020001").Count(&count).Error)
		require.Zero(t, count)
		require.NoError(t, store.Migrate(ctx))
		require.NoError(t, db.Exec("INSERT INTO migrations (id) VALUES (?)", "209901010001").Error)
		err = adapter.NewStore(db, adapter.WithAutoMigrate(false)).CheckSchema(ctx)
		require.ErrorContains(t, err, "unknown migrations")
	})
}

func TestRecoveryPlanKeepsUUIDs(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		seedLegacyRecovery(t, db)
		id := string(model.NewUserID())
		require.NoError(t, db.Create(&adapter.User{ID: id, TenantID: "tenant-acme", Email: "uuid@example.test"}).Error)
		a, err := adapter.PlanCommonRecovery(t.Context(), db)
		require.NoError(t, err)
		require.Equal(t, id, a.IDs["users"][id])
		encoded, err := json.Marshal(a)
		require.NoError(t, err)
		require.Contains(t, string(encoded), fmt.Sprintf(`"%s":"%s"`, id, id))
	})
}

func TestRecoveryLateFailureRollsBackCorrections(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		seedLegacyRecovery(t, db)
		query := `{user="user-alice"}`
		require.NoError(t, db.Create(&adapter.Alert{ID: "alert", OrgID: "org-acme", Query: query}).Error)
		a, err := adapter.PlanCommonRecovery(t.Context(), db)
		require.NoError(t, err)
		a.EmailOverrides["user-alice"] = "corrected@example.test"
		failure := errors.New("checkpoint failure")
		require.NoError(t, db.Callback().Create().Before("gorm:create").Register("fail_checkpoint", func(tx *gormpkg.DB) {
			if tx.Statement.Table == "common_recoveries" {
				tx.AddError(failure)
			}
		}))
		err = adapter.MigrateDatabase(t.Context(), db, a)
		require.NoError(t, db.Callback().Create().Remove("fail_checkpoint"))
		require.ErrorIs(t, err, failure)
		var user adapter.User
		require.NoError(t, db.First(&user, "id = ?", "user-alice").Error)
		require.Equal(t, "Alice@Example.test", user.Email)
		var alert adapter.Alert
		require.NoError(t, db.First(&alert, "id = ?", "alert").Error)
		require.Equal(t, query, alert.Query)
		require.Equal(t, "org-acme", alert.OrgID)
		require.NoError(t, adapter.MigrateDatabase(t.Context(), db, a))
	})
}

func TestRecoveryDoesNotPretendToApplyToCurrentSchema(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		require.NoError(t, adapter.NewStore(db).Migrate(t.Context()))
		a, err := adapter.PlanCommonRecovery(t.Context(), db)
		require.NoError(t, err)
		require.ErrorContains(t, adapter.MigrateDatabase(t.Context(), db, a), "already applied without this recovery plan")
	})
}
