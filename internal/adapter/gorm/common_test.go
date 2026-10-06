package gorm_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/rs/xid"
	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/crypto"
	gormpkg "gorm.io/gorm"
)

func TestCommonRecovery(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(fmt.Sprintf("automatic=%v", automatic), func(t *testing.T) {
			eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
				ctx := t.Context()
				s := adapter.NewStore(db)
				require.NoError(t, s.Migrate(ctx))
				// Simulate the previous release with actual xid keys, foreign keys enabled,
				// an optional provider identity and a personal secret scope.
				tenantID, orgID, userID := xid.New().String(), xid.New().String(), xid.New().String()
				require.NoError(t, db.Create(&adapter.Tenant{ID: tenantID, Slug: "legacy", Name: "Legacy", Active: 1}).Error)
				require.NoError(t, db.Create(&adapter.Organization{ID: orgID, TenantID: tenantID, Slug: "legacy", Name: "Legacy", Active: 1, Currency: "EUR"}).Error)
				require.NoError(t, db.Create(&adapter.User{ID: userID, TenantID: tenantID, Provider: "oidc", Subject: "subject", Email: "Legacy@Example.test", Active: true}).Error)
				secretKey := "0000000000000000000000000000000000000000000000000000000000000000"
				encrypted, err := crypto.Encrypt(secretKey, "fixture-secret")
				require.NoError(t, err)
				require.NoError(t, db.Create(&adapter.PluginNodeSecret{ID: "secret", OrgID: "~:" + userID, PluginName: "test", NodeID: "node", Key: "secret", ValueEncrypted: encrypted}).Error)
				require.NoError(t, db.Create(&adapter.Membership{ID: "membership", UserID: userID, OrgID: orgID}).Error)
				require.NoError(t, db.Create(&adapter.Role{ID: "legacy-owner", OrgID: orgID, Name: "Owner", Builtin: true, BuiltinKind: "owner"}).Error)
				require.NoError(t, db.Create(&adapter.MembershipRole{MembershipID: "membership", RoleID: "legacy-owner"}).Error)
				require.NoError(t, db.Create(&adapter.AuthToken{ID: "legacy-token", OwnerID: &userID, OrgID: orgID, Value: crypto.HashToken("fixture-key")}).Error)
				require.NoError(t, db.Create(&adapter.Quota{ID: "legacy-quota", Scope: "user", ScopeID: userID, Currency: "EUR"}).Error)
				require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", "202610020001").Error)
				artifact, err := adapter.PlanCommonRecovery(ctx, db)
				require.NoError(t, err)
				report, err := adapter.DiagnoseCommonRecovery(ctx, db, artifact)
				require.NoError(t, err)
				require.Empty(t, report.Issues)
				require.NoError(t, db.Create(&adapter.UserRole{UserID: userID, Role: "admin"}).Error)
				for _, role := range []adapter.Role{
					{ID: "legacy-admin", OrgID: orgID, Name: "Admin", Builtin: true, BuiltinKind: "admin"},
					{ID: "legacy-custom", OrgID: orgID, Name: "Custom"},
				} {
					require.NoError(t, db.Create(&role).Error)
					require.NoError(t, db.Create(&adapter.MembershipRole{MembershipID: "membership", RoleID: role.ID}).Error)
				}
				if db.Dialector.Name() == "sqlite" {
					// Use one physical connection to verify its FK setting survives
					// rollback before it returns to the pool.
					pool, err := db.DB()
					require.NoError(t, err)
					pool.SetMaxOpenConns(1)
					require.NoError(t, db.Exec("PRAGMA foreign_keys = ON").Error)
				}
				// An interruption before the checkpoint rolls back all rewritten references.
				require.NoError(t, db.Callback().Create().Before("gorm:create").Register("interrupt_recovery", func(tx *gormpkg.DB) {
					if tx.Statement.Table == "common_recoveries" {
						tx.AddError(fmt.Errorf("interrupted"))
					}
				}))
				if automatic {
					err = adapter.NewStore(db).Migrate(ctx)
				} else {
					err = adapter.MigrateDatabase(ctx, db, artifact)
				}
				require.Error(t, err)
				require.NoError(t, db.Callback().Create().Remove("interrupt_recovery"))
				if db.Dialector.Name() == "sqlite" {
					var enabled int
					require.NoError(t, db.Raw("PRAGMA foreign_keys").Scan(&enabled).Error)
					require.Equal(t, 1, enabled)
					pool, err := db.DB()
					require.NoError(t, err)
					pool.SetMaxOpenConns(0)
				}
				var original adapter.User
				require.NoError(t, db.Select("id, tenant_id, provider, subject, email, active").First(&original, "id = ?", userID).Error)
				if automatic {
					// Independent startup instances race on the same legacy database.
					results := make(chan error, 3)
					for range 3 {
						go func() { results <- adapter.NewStore(db).Migrate(ctx) }()
					}
					for range 3 {
						require.NoError(t, <-results)
					}
					var checkpoint adapter.CommonRecovery
					require.NoError(t, db.First(&checkpoint, 1).Error)
					artifact = &adapter.RecoveryArtifact{}
					require.NoError(t, json.Unmarshal([]byte(checkpoint.Artifact), artifact))
				} else {
					require.NoError(t, adapter.MigrateDatabase(ctx, db, artifact))
					report, err = adapter.DiagnoseCommonRecovery(ctx, db, artifact)
					require.NoError(t, err)
					require.True(t, report.Applied)
					require.NoError(t, adapter.MigrateDatabase(ctx, db, artifact))
				}
				migrated := adapter.NewStore(db)
				require.NoError(t, migrated.Migrate(ctx))
				user, err := migrated.GetUserByID(ctx, model.UserID(artifact.IDs["users"][userID]))
				require.NoError(t, err)

				require.Equal(t, "Legacy@Example.test", user.Email())
				if automatic {
					var roles []adapter.MembershipRole
					require.NoError(t, db.Where("membership_id = ?", "membership").Find(&roles).Error)
					require.Len(t, roles, 3)
					var platformRole adapter.UserRole
					require.NoError(t, db.First(&platformRole, "user_id = ?", user.ID()).Error)
					require.Equal(t, "admin", platformRole.Role)
				}

				membership, err := migrated.GetMembership(ctx, "membership")
				require.NoError(t, err)
				require.Len(t, membership.Roles(), 3)
				require.Equal(t, user.ID(), membership.UserID())
				require.Equal(t, model.OrgID(artifact.IDs["organizations"][orgID]), membership.OrgID())
				token, err := migrated.FindAuthToken(ctx, "fixture-key")
				require.NoError(t, err)
				require.Equal(t, user.ID(), token.Owner().ID())
				require.Equal(t, model.OrgID(artifact.IDs["organizations"][orgID]), token.OrgID())
				var quota adapter.Quota
				require.NoError(t, db.First(&quota, "id = ?", "legacy-quota").Error)
				require.Equal(t, string(user.ID()), quota.ScopeID)

				var secret adapter.PluginNodeSecret
				require.NoError(t, db.First(&secret, "id = ?", "secret").Error)
				require.Equal(t, "~:"+string(user.ID()), secret.OrgID)
				require.Equal(t, encrypted, secret.ValueEncrypted)
				plaintext, err := crypto.Decrypt(secretKey, secret.ValueEncrypted)
				require.NoError(t, err)
				require.Equal(t, "fixture-secret", plaintext)
				for _, table := range []string{"publications", "publication_clocks", "uuid_recovery_ids", "uuid_recovery_changes"} {
					require.False(t, db.Migrator().HasTable(table), table)
				}
			})
		})
	}
}
