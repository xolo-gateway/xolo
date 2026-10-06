package gorm_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"gorm.io/gorm"
)

func TestRecoveryUnknownEventAttributesAreInformational(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(fmt.Sprintf("automatic=%t", automatic), func(t *testing.T) {
			eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
				seedLegacyRecovery(t, db)
				existingUUID := string(model.NewUserID())
				require.NoError(t, db.Create(&adapter.User{
					ID: existingUUID, TenantID: "tenant-acme", Provider: "oidc", Subject: "existing-uuid",
				}).Error)
				// Cross a keyset page and repeat values: notices count distinct
				// values per key across the whole scan, not rows or pages.
				rows := make([]map[string]any, 0, 1002)
				for i := range 1002 {
					value := "user-alice"
					if i%8 != 0 {
						value += fmt.Sprintf("/%d", i%8)
					}
					attributes := fmt.Sprintf(
						`{"actor_id":"user-alice","tenant_user":%q,"tenant_org":"org-acme","plain":"ordinary text","uuid":%q,"deleted":"deleted-user"}`,
						value, existingUUID,
					)
					rows = append(rows, map[string]any{
						"id": fmt.Sprintf("event-%04d", i), "user_id": "user-alice", "org_id": "org-acme", "attributes": attributes,
					})
				}
				require.NoError(t, db.Table("events").CreateInBatches(&rows, 200).Error)
				a, err := adapter.PlanCommonRecovery(t.Context(), db)
				require.NoError(t, err)
				report, err := adapter.DiagnoseCommonRecovery(t.Context(), db, a)
				require.NoError(t, err)
				require.Empty(t, report.Issues)
				wantNotices := []string{
					`unmapped event attribute: events.attributes [key "tenant_org"]: 1 distinct values; examples: "org-acme"`,
					`unmapped event attribute: events.attributes [key "tenant_user"]: 8 distinct values; examples: "user-alice", "user-alice/1", "user-alice/2", "user-alice/3", "user-alice/4"; 3 omitted`,
				}
				require.Equal(t, wantNotices, report.Notices)
				repeated, err := adapter.DiagnoseCommonRecovery(t.Context(), db, a)
				require.NoError(t, err)
				require.Equal(t, report.Notices, repeated.Notices)
				var before adapter.Event
				require.NoError(t, db.First(&before, "id = ?", "event-0000").Error)
				require.Equal(t, "user-alice", (*before.Attributes.Val)["actor_id"], "diagnosis is read-only")

				if automatic {
					require.NoError(t, adapter.NewStore(db).Migrate(t.Context()))
					a, err = adapter.PlanCommonRecovery(t.Context(), db)
					require.NoError(t, err)
				} else {
					// Save/reload the unchanged v2 artifact before applying it.
					encoded, err := json.Marshal(a)
					require.NoError(t, err)
					a = &adapter.RecoveryArtifact{}
					require.NoError(t, json.Unmarshal(encoded, a))
					require.NoError(t, adapter.MigrateDatabase(t.Context(), db, a))
				}
				applied, err := adapter.DiagnoseCommonRecovery(t.Context(), db, a)
				require.NoError(t, err)
				require.True(t, applied.Applied)
				require.Empty(t, applied.Issues)
				require.Equal(t, 2, a.Version)
				var events []adapter.Event
				require.NoError(t, db.Order("id").Find(&events).Error)
				require.Len(t, events, len(rows))
				for i, event := range events {
					want := "user-alice"
					if i%8 != 0 {
						want += fmt.Sprintf("/%d", i%8)
					}
					require.Equal(t, want, (*event.Attributes.Val)["tenant_user"])
					require.Equal(t, "org-acme", (*event.Attributes.Val)["tenant_org"])
					require.Equal(t, existingUUID, (*event.Attributes.Val)["uuid"])
					require.Equal(t, a.IDs["users"]["user-alice"], (*event.Attributes.Val)["actor_id"])
				}
				require.NoError(t, adapter.MigrateDatabase(t.Context(), db, a))
			})
		})
	}
}

func TestRecoveryEventAttributeNoticesRespectOverrides(t *testing.T) {
	for _, correct := range []bool{false, true} {
		t.Run(fmt.Sprintf("correct=%t", correct), func(t *testing.T) {
			eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
				seedLegacyRecovery(t, db)
				before := `{"tenant_user":"user-alice"}`
				require.NoError(t, db.Table("events").Create(map[string]any{
					"id": "event", "org_id": "org-acme", "attributes": before,
				}).Error)
				a, err := adapter.PlanCommonRecovery(t.Context(), db)
				require.NoError(t, err)
				report, err := adapter.DiagnoseCommonRecovery(t.Context(), db, a)
				require.NoError(t, err)
				require.Len(t, report.Notices, 1)
				after := before
				want := "user-alice"
				if correct {
					want = a.IDs["users"]["user-alice"]
					after = fmt.Sprintf(`{"tenant_user":%q}`, want)
				}
				a.SerializedOverrides = []adapter.RecoverySerializedOverride{{
					Table: "events", ID: "event", Column: "attributes", Before: before, After: after,
				}}
				report, err = adapter.DiagnoseCommonRecovery(t.Context(), db, a)
				require.NoError(t, err)
				require.Empty(t, report.Issues)
				require.Empty(t, report.Notices, "an explicit correction or acknowledgement has been reviewed")
				require.NoError(t, adapter.MigrateDatabase(t.Context(), db, a))
				var event adapter.Event
				require.NoError(t, db.First(&event, "id = ?", "event").Error)
				require.Equal(t, want, (*event.Attributes.Val)["tenant_user"])
			})
		})
	}
}
