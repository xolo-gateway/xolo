package gorm_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/rs/xid"
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

func TestRecoveryAppliesPendingMigrations(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		seedLegacyRecovery(t, db)
		prepareInvitationUpgrade(t, db)
		invite := adapter.InviteToken{
			ID: xid.New().String(), OrgID: "org-acme", CreatedByUserID: "user-alice",
		}
		require.NoError(t, db.Create(&invite).Error)
		artifact, err := adapter.PlanCommonRecovery(t.Context(), db)
		require.NoError(t, err)

		// Observe the earlier migrations before the UUID checkpoint is written.
		var earlierMarkers int64
		require.NoError(t, db.Callback().Create().Before("gorm:create").Register("check_earlier_migrations", func(tx *gormpkg.DB) {
			if tx.Statement.Table == "common_recoveries" {
				err := tx.Session(&gormpkg.Session{NewDB: true}).Table("migrations").
					Where("id IN (?, ?)", "202609300001", "202609300002").Count(&earlierMarkers).Error
				tx.AddError(err)
			}
		}))
		err = adapter.MigrateDatabase(t.Context(), db, artifact)
		require.NoError(t, db.Callback().Create().Remove("check_earlier_migrations"))
		require.NoError(t, err)
		require.EqualValues(t, 2, earlierMarkers)
		require.NoError(t, adapter.CheckDatabaseSchema(t.Context(), db))
		require.True(t, db.Migrator().HasIndex(&adapter.Membership{}, "idx_memberships_user_org"))
		var migrated adapter.InviteToken
		require.NoError(t, db.First(&migrated, "id = ?", invite.ID).Error)
		require.NotNil(t, migrated.RevokedAt)
		require.Equal(t, artifact.IDs["organizations"]["org-acme"], migrated.OrgID)
		require.Equal(t, artifact.IDs["users"]["user-alice"], migrated.CreatedByUserID)
		report, err := adapter.DiagnoseCommonRecovery(t.Context(), db, artifact)
		require.NoError(t, err)
		require.True(t, report.Applied)
	})
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

func TestRecoveryPreservesEmailCaseAndIdentity(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		seedLegacyRecovery(t, db)
		require.NoError(t, db.Create(&adapter.User{ID: "user-other", TenantID: "tenant-acme", Provider: "oidc", Subject: "other", Email: "alice@example.test", Active: true}).Error)
		a, err := adapter.PlanCommonRecovery(t.Context(), db)
		require.NoError(t, err)
		report, err := adapter.DiagnoseCommonRecovery(t.Context(), db, a)
		require.NoError(t, err)
		require.Empty(t, report.Issues)
		require.NoError(t, adapter.MigrateDatabase(t.Context(), db, a))
		var original, other adapter.User
		require.NoError(t, db.First(&original, "id = ?", a.IDs["users"]["user-alice"]).Error)
		require.NoError(t, db.First(&other, "id = ?", a.IDs["users"]["user-other"]).Error)
		require.Equal(t, "Alice@Example.test", original.Email)
		require.Equal(t, "alice@example.test", other.Email)
		require.Equal(t, "oidc", other.Provider)
		require.Equal(t, "other", other.Subject)
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
		changed.SerializedOverrides = nil
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
		require.NoError(t, db.Create(&adapter.User{ID: id, TenantID: "tenant-acme", Email: "uuid@example.test", Provider: "oidc", Subject: "uuid"}).Error)
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
		graph := `{"nodes":[{"id":"user-alice","data":{"value":"user-alice"}}]}`
		require.NoError(t, db.Create(&adapter.VirtualModel{ID: "vm", OrgID: "org-acme", Name: "vm", GraphJSON: graph}).Error)
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
		var vm adapter.VirtualModel
		require.NoError(t, db.First(&vm, "id = ?", "vm").Error)
		require.Equal(t, graph, vm.GraphJSON)
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

func TestRecoveryReferenceScopes(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		require.NoError(t, adapter.NewStore(db).Migrate(t.Context()))
		// Shared arbitrary keys across families must use their declared scope,
		// including characters which must never be interpreted as SQL or LIKE syntax.
		const old = "same'_%~:legacy"
		require.NoError(t, db.Create(&adapter.Tenant{ID: old, Slug: "shared", Name: "Shared", Active: 1}).Error)
		require.NoError(t, db.Create(&adapter.Organization{ID: old, TenantID: old, Slug: "shared", Name: "Shared", Active: 1}).Error)
		require.NoError(t, db.Create(&adapter.User{ID: old, TenantID: old, Provider: "OIDC", Subject: " Mixed Case ", Email: " Mixed@Example.test ", Active: true}).Error)
		for _, scope := range []string{"org", "user", "application"} {
			require.NoError(t, db.Create(&adapter.Quota{ID: scope, Scope: scope, ScopeID: old}).Error)
		}
		for _, secret := range []adapter.PluginNodeSecret{
			{ID: "org", OrgID: old, PluginName: "mcp-bridge", NodeID: "node", Key: "oauth:" + old, ValueEncrypted: "keep"},
			{ID: "personal", OrgID: "~:" + old, PluginName: "mcp-bridge", NodeID: "node", Key: "oauth:" + old, ValueEncrypted: "keep"},
			{ID: "literal", OrgID: old, PluginName: "other", NodeID: "node", Key: "oauth:" + old, ValueEncrypted: "keep"},
		} {
			require.NoError(t, db.Create(&secret).Error)
		}
		require.NoError(t, db.Table("events").Create(map[string]any{"id": "historical", "user_id": "deleted-user", "org_id": "deleted-org", "attributes": "{}"}).Error)
		require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", "202610020001").Error)
		a, err := adapter.PlanCommonRecovery(t.Context(), db)
		require.NoError(t, err)
		require.NoError(t, adapter.MigrateDatabase(t.Context(), db, a))
		uid, oid := a.IDs["users"][old], a.IDs["organizations"][old]
		for scope, want := range map[string]string{"org": oid, "user": uid, "application": old} {
			var quota adapter.Quota
			require.NoError(t, db.First(&quota, "id = ?", scope).Error)
			require.Equal(t, want, quota.ScopeID)
		}
		for id, want := range map[string]string{"org": oid, "personal": "~:" + uid, "literal": oid} {
			var secret adapter.PluginNodeSecret
			require.NoError(t, db.First(&secret, "id = ?", id).Error)
			require.Equal(t, want, secret.OrgID)
			key := "oauth:" + uid
			if id == "literal" {
				key = "oauth:" + old
			}
			require.Equal(t, key, secret.Key)
			require.Equal(t, "keep", secret.ValueEncrypted)
		}
		var user adapter.User
		require.NoError(t, db.First(&user, "id = ?", uid).Error)
		require.Equal(t, " Mixed@Example.test ", user.Email)
		require.Equal(t, "OIDC", user.Provider)
		require.Equal(t, " Mixed Case ", user.Subject)
		require.NoError(t, db.Transaction(func(tx *gormpkg.DB) error {
			if db.Dialector.Name() == "postgres" {
				if err := tx.Exec("SET TRANSACTION READ ONLY").Error; err != nil {
					return err
				}
			}
			report, err := adapter.DiagnoseCommonRecovery(t.Context(), tx, a)
			require.NoError(t, err)
			require.True(t, report.Applied)
			return nil
		}))
	})
}

func TestRecoveryPagesAndQueryScaling(t *testing.T) {
	for _, users := range []int{2, 1002} {
		t.Run(fmt.Sprintf("users=%d", users), func(t *testing.T) {
			eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
				seedLegacyRecovery(t, db)
				rows := make([]map[string]any, 0, users)
				for i := 0; i < users; i++ {
					id := fmt.Sprintf("legacy-%04d", i)
					rows = append(rows, map[string]any{"id": id, "tenant_id": "tenant-acme", "provider": "test", "subject": id})
				}
				require.NoError(t, db.Table("users").CreateInBatches(&rows, 200).Error)
				events := make([]map[string]any, 0, 2001)
				for i := 0; i < 2001; i++ {
					id := fmt.Sprintf("legacy-%04d", i%users)
					events = append(events, map[string]any{"id": fmt.Sprintf("event-%04d", i), "org_id": "org-acme", "user_id": id, "attributes": fmt.Sprintf(`{"actor_id":%q}`, id)})
				}
				require.NoError(t, db.Table("events").CreateInBatches(&events, 200).Error)
				graphs := make([]map[string]any, 0, 2001)
				for i := 0; i < 2001; i++ {
					id := fmt.Sprintf("legacy-%04d", i%users)
					name := fmt.Sprintf("graph-%04d", i)
					graphs = append(graphs, map[string]any{"id": name, "name": name, "org_id": "org-acme", "graph_json": fmt.Sprintf(`{"value":%q,"script":"unmatched"}`, id)})
				}
				require.NoError(t, db.Table("virtual_models").CreateInBatches(&graphs, 200).Error)
				a, err := adapter.PlanCommonRecovery(t.Context(), db)
				require.NoError(t, err)
				counter := &recoveryQueryCounter{Interface: db.Logger}
				measured := db.Session(&gormpkg.Session{Logger: counter})
				require.NoError(t, adapter.MigrateDatabase(t.Context(), measured, a))
				// Three serialized pages plus the two declared relational references.
				require.Equal(t, int64(5), counter.eventWrites.Load())
				require.Less(t, counter.eventReads.Load(), int64(64), "event scans must stay bounded as IDs increase")
				t.Logf("event reads=%d, updates=%d", counter.eventReads.Load(), counter.eventWrites.Load())
				var remaining int64
				require.NoError(t, db.Table("events").Where("user_id LIKE 'legacy-%' OR attributes LIKE '%legacy-%'").Count(&remaining).Error)
				require.Zero(t, remaining)
				var count int64
				require.NoError(t, db.Table("events").Count(&count).Error)
				require.Equal(t, int64(2001), count)
				require.NoError(t, db.Table("virtual_models").Where("graph_json LIKE '%legacy-%'").Count(&remaining).Error)
				require.Zero(t, remaining)
				require.NoError(t, db.Table("virtual_models").Count(&count).Error)
				require.EqualValues(t, 2001, count)
			})
		})
	}
}

func TestUUIDSchemaAndOrdinaryWrites(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := adapter.NewStore(db)
		require.NoError(t, store.Migrate(t.Context()))
		for _, table := range []string{"domains", "reserved_domains", "publications", "publication_clocks"} {
			require.False(t, db.Migrator().HasTable(table), table)
		}
		for _, column := range [][2]string{{"users", "tenant_role"}, {"memberships", "common_role"}, {"memberships", "status"}} {
			require.False(t, db.Migrator().HasColumn(column[0], column[1]))
		}
		// Migration state is initialized: subsequent store calls have no migration
		// lock dependency. No global publication counter exists in this schema.
		require.NoError(t, db.Migrator().DropTable("migration_lock"))
		tenant, err := store.GetTenantBySlug(t.Context(), model.DefaultTenantSlug)
		require.NoError(t, err)
		user, err := store.FindOrCreateUser(t.Context(), tenant.ID(), "oidc", "ordinary")
		require.NoError(t, err)
		copy := model.CopyUser(user)
		copy.SetEmail("Keep.Case@Example.test")
		require.NoError(t, store.SaveUser(t.Context(), copy))
		stored, err := store.GetUserByIdentity(t.Context(), tenant.ID(), "oidc", "ordinary")
		require.NoError(t, err)
		require.Equal(t, copy.Email(), stored.Email())
	})
}
