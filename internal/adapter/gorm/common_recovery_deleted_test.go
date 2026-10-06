package gorm_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/crypto"
	"gorm.io/gorm"
)

func TestRecoveryAfterNormalUserDeletion(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(fmt.Sprintf("automatic=%t", automatic), func(t *testing.T) {
			eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
				store := adapter.NewStore(db)
				require.NoError(t, store.Migrate(t.Context()))
				require.NoError(t, db.Create(&adapter.Tenant{ID: "tenant-old", Slug: "old", Active: 1}).Error)
				require.NoError(t, db.Create(&adapter.Organization{ID: "org-old", TenantID: "tenant-old", Slug: "old"}).Error)
				const secretKey = "0000000000000000000000000000000000000000000000000000000000000000"
				encrypted, err := crypto.Encrypt(secretKey, "retained-secret")
				require.NoError(t, err)
				for _, id := range []string{"live-user", "deleted-user"} {
					require.NoError(t, db.Create(&adapter.User{ID: id, TenantID: "tenant-old", Provider: "oidc", Subject: id}).Error)
					for scope, value := range map[string]any{"org": "org", "empty": "", "null": nil, "personal": "personal"} {
						require.NoError(t, db.Table("alerts").Create(map[string]any{
							"id": id + "-" + scope, "org_id": "org-old", "owner_id": id, "scope": value,
						}).Error)
					}
					require.NoError(t, db.Create(&adapter.InviteToken{
						ID: id, OrgID: "org-old", CreatedByUserID: id, Role: "member",
					}).Error)
					for _, secret := range []adapter.PluginNodeSecret{
						{ID: id + "-personal", OrgID: "~:" + id, PluginName: "test", NodeID: "node", Key: "secret"},
						{ID: id + "-oauth", OrgID: "org-old", PluginName: "mcp-bridge", NodeID: "node", Key: "oauth:" + id},
						{ID: id + "-personal-oauth", OrgID: "~:" + id, PluginName: "mcp-bridge", NodeID: "node", Key: "oauth:" + id},
					} {
						secret.ValueEncrypted = encrypted
						require.NoError(t, db.Create(&secret).Error)
					}
					record := model.NewUsageRecord(model.UserID(id), "", "org-old", "provider", "model",
						"model", "", 10, 0, 10, 123, "EUR", model.CostSourceComputed, "")
					require.NoError(t, store.RecordUsage(t.Context(), record))
				}
				require.NoError(t, store.DeleteUser(t.Context(), "deleted-user"))
				var personalCount int64
				require.NoError(t, db.Table("alerts").Where("id = ?", "deleted-user-personal").Count(&personalCount).Error)
				require.Zero(t, personalCount, "the fixture must exercise normal deletion")
				require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", "202610020001").Error)
				plan, err := adapter.PlanCommonRecovery(t.Context(), db)
				require.NoError(t, err)
				require.NotContains(t, plan.IDs["users"], "deleted-user")
				before, err := adapter.DiagnoseCommonRecovery(t.Context(), db, plan)
				require.NoError(t, err)
				require.Empty(t, before.Issues)
				if automatic {
					require.NoError(t, adapter.NewStore(db).Migrate(t.Context()))
					plan, err = adapter.PlanCommonRecovery(t.Context(), db)
					require.NoError(t, err)
				} else {
					require.NoError(t, adapter.MigrateDatabase(t.Context(), db, plan))
				}
				after, err := adapter.DiagnoseCommonRecovery(t.Context(), db, plan)
				require.NoError(t, err)
				require.True(t, after.Applied)
				for table, count := range before.Counts {
					if table != "migrations" {
						require.Equal(t, count, after.Counts[table], table)
					}
				}
				orgID := plan.IDs["organizations"]["org-old"]
				for _, old := range []string{"live-user", "deleted-user"} {
					want := old
					if mapped, ok := plan.IDs["users"][old]; ok {
						want = mapped
					}
					var alerts []adapter.Alert
					require.NoError(t, db.Where("id LIKE ?", old+"-%").Find(&alerts).Error)
					count := 3
					if old == "live-user" {
						count++
					}
					require.Len(t, alerts, count)
					for _, alert := range alerts {
						require.Equal(t, want, alert.OwnerID)
						require.Equal(t, orgID, alert.OrgID)
					}
					var invite adapter.InviteToken
					require.NoError(t, db.First(&invite, "id = ?", old).Error)
					require.Equal(t, want, invite.CreatedByUserID)
					require.Equal(t, orgID, invite.OrgID)
					var secrets []adapter.PluginNodeSecret
					require.NoError(t, db.Where("id LIKE ?", old+"-%").Find(&secrets).Error)
					require.Len(t, secrets, 3)
					for _, secret := range secrets {
						wantOrg := "~:" + want
						if secret.ID == old+"-oauth" {
							wantOrg = orgID
						}
						require.Equal(t, wantOrg, secret.OrgID)
						if secret.PluginName == "mcp-bridge" {
							require.Equal(t, "oauth:"+want, secret.Key)
						}
						require.Equal(t, encrypted, secret.ValueEncrypted)
						plain, err := crypto.Decrypt(secretKey, secret.ValueEncrypted)
						require.NoError(t, err)
						require.Equal(t, "retained-secret", plain)
					}
					var usage adapter.UsageRecord
					require.NoError(t, db.First(&usage, "user_id = ?", want).Error)
					require.Equal(t, orgID, usage.OrgID)
					require.EqualValues(t, 123, usage.Cost)
					var quota adapter.QuotaUsage
					require.NoError(t, db.First(&quota, "scope = 'user' AND scope_id = ?", want).Error)
					require.Equal(t, orgID, quota.OrgID)
					require.EqualValues(t, 123, quota.Cost)
				}
				var orgQuota adapter.QuotaUsage
				require.NoError(t, db.First(&orgQuota, "scope = 'org' AND scope_id = ?", orgID).Error)
				require.EqualValues(t, 246, orgQuota.Cost)
				require.NoError(t, adapter.MigrateDatabase(t.Context(), db, plan))

				// Historical does not permit leaving a mapped old ID behind.
				for _, target := range []struct{ table, column, id, value string }{
					{table: "alerts", column: "owner_id", id: "live-user-org", value: "live-user"},
					{table: "alerts", column: "owner_id", id: "live-user-empty", value: "live-user"},
					{table: "alerts", column: "owner_id", id: "live-user-null", value: "live-user"},
					{table: "invite_tokens", column: "created_by_user_id", id: "live-user", value: "live-user"},
					{table: "plugin_node_secrets", column: "org_id", id: "live-user-personal", value: "~:live-user"},
					{table: "plugin_node_secrets", column: "key", id: "live-user-oauth", value: "oauth:live-user"},
				} {
					tx := db.Begin()
					require.NoError(t, tx.Error)
					require.NoError(t, tx.Table(target.table).Where("id = ?", target.id).Update(target.column, target.value).Error)
					_, err := adapter.DiagnoseCommonRecovery(t.Context(), tx, plan)
					require.NoError(t, tx.Rollback().Error)
					require.ErrorContains(t, err, "legacy reference remains in "+target.table+"."+target.column)
				}
			})
		})
	}
}
