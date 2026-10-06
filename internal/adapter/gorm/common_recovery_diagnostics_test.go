package gorm_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"gorm.io/gorm"
)

func TestRecoveryRequiredReferenceDiagnostics(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		seedLegacyRecovery(t, db)
		for i := 7; i >= 0; i-- {
			id := fmt.Sprintf("missing-%d", i)
			require.NoError(t, db.Create(&adapter.Alert{
				ID: id, OrgID: "org-acme", OwnerID: id, Scope: "personal",
			}).Error)
			for _, scope := range []string{"user", "org"} {
				require.NoError(t, db.Create(&adapter.Quota{ID: scope + id, Scope: scope, ScopeID: id}).Error)
			}
		}
		// Repeated values count once. Organization alerts are outside this group.
		require.NoError(t, db.Create(&adapter.Alert{
			ID: "duplicate", OrgID: "org-acme", OwnerID: "missing-0", Scope: "personal",
		}).Error)
		require.NoError(t, db.Create(&adapter.Alert{
			ID: "historical", OrgID: "org-acme", OwnerID: "historical-owner", Scope: "org",
		}).Error)
		require.NoError(t, db.Create(&adapter.PluginNodeSecret{
			ID: "bad-org", OrgID: "missing-\"org\"\n", PluginName: "test", NodeID: "n", Key: "k",
		}).Error)
		plan, err := adapter.PlanCommonRecovery(t.Context(), db)
		require.NoError(t, err)
		report, err := adapter.DiagnoseCommonRecovery(t.Context(), db, plan)
		require.NoError(t, err)
		require.Len(t, report.Issues, 4)
		require.IsIncreasing(t, report.Issues)
		for _, issue := range report.Issues {
			if strings.Contains(issue, "plugin_node_secrets") {
				require.Contains(t, issue, `org_id NOT LIKE '~:%'`)
				require.Contains(t, issue, `1 distinct values; examples: "missing-\"org\"\n"`)
				continue
			}
			require.Contains(t, issue, `8 distinct values; examples: "missing-0", "missing-1", "missing-2", "missing-3", "missing-4"; 3 omitted`)
			require.NotContains(t, issue, "missing-5")
			require.NotContains(t, issue, "historical-owner")
		}
		require.Contains(t, strings.Join(report.Issues, "\n"), "quota.scope_id [references users; scope = 'user']")
		require.Contains(t, strings.Join(report.Issues, "\n"), "quota.scope_id [references organizations; scope = 'org']")
		for range 3 {
			repeated, err := adapter.DiagnoseCommonRecovery(t.Context(), db, plan)
			require.NoError(t, err)
			require.Equal(t, report.Issues, repeated.Issues)
		}
		require.ErrorContains(t, adapter.NewStore(db).Migrate(t.Context()), "orphan: alerts.owner_id")
		err = adapter.MigrateDatabase(t.Context(), db, plan)
		require.ErrorContains(t, err, "orphan: alerts.owner_id")
		var user adapter.User
		require.NoError(t, db.First(&user, "id = ?", "user-alice").Error)
	})
}

func TestRecoveryMappingDiagnostics(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		seedLegacyRecovery(t, db)
		preserved := string(model.NewUserID())
		require.NoError(t, db.Create(&adapter.User{
			ID: preserved, TenantID: "tenant-acme", Provider: "oidc", Subject: "uuid",
		}).Error)
		const target = "11111111-1111-4111-8111-111111111111"
		for _, tc := range []struct {
			name string
			edit func(*adapter.RecoveryArtifact)
			want []string
		}{
			{
				name: "missing and extra keys with equal count",
				edit: func(a *adapter.RecoveryArtifact) {
					delete(a.IDs["users"], "user-alice")
					a.IDs["users"]["ghost-user"] = target
				},
				want: []string{`missing mapping key: users.id: 1 distinct values; examples: "user-alice"`,
					`unexpected mapping key: users.id: 1 distinct values; examples: "ghost-user" -> "` + target + `"`},
			},
			{
				name: "invalid UUID",
				edit: func(a *adapter.RecoveryArtifact) { a.IDs["users"]["user-alice"] = "bad-uuid" },
				want: []string{`invalid UUID: users.id: 1 distinct values; examples: "user-alice" -> "bad-uuid"`},
			},
			{
				name: "duplicate across families",
				edit: func(a *adapter.RecoveryArtifact) {
					a.IDs["users"]["user-alice"] = target
					a.IDs["organizations"]["org-acme"] = target
				},
				want: []string{
					`duplicate target UUID: organizations.id: 1 distinct values; examples: "org-acme" -> "` + target + `"`,
					`duplicate target UUID: users.id: 1 distinct values; examples: "user-alice" -> "` + target + `"`,
				},
			},
			{
				name: "existing UUID",
				edit: func(a *adapter.RecoveryArtifact) { a.IDs["users"][preserved] = target },
				want: []string{fmt.Sprintf("existing UUID must be preserved: users.id: 1 distinct values; examples: %q -> %q", preserved, target)},
			},
			{
				name: "missing and extra families",
				edit: func(a *adapter.RecoveryArtifact) {
					delete(a.IDs, "users")
					a.IDs["typo"] = map[string]string{}
				},
				want: []string{`missing mapping family: ids: 1 distinct values; examples: "users"`,
					`unexpected mapping family: ids: 1 distinct values; examples: "typo"`},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				a, err := adapter.PlanCommonRecovery(t.Context(), db)
				require.NoError(t, err)
				tc.edit(a)
				report, err := adapter.DiagnoseCommonRecovery(t.Context(), db, a)
				require.NoError(t, err)
				for _, want := range tc.want {
					require.Contains(t, report.Issues, want)
				}
				for range 3 {
					repeated, err := adapter.DiagnoseCommonRecovery(t.Context(), db, a)
					require.NoError(t, err)
					require.Equal(t, report.Issues, repeated.Issues)
				}
				require.Error(t, adapter.MigrateDatabase(t.Context(), db, a))
			})
		}
	})
}

func TestRecoveryMappingDiagnosticLimits(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		seedLegacyRecovery(t, db)
		for i := 7; i >= 0; i-- {
			id := fmt.Sprintf("bad-%d", i)
			require.NoError(t, db.Create(&adapter.User{
				ID: id, TenantID: "tenant-acme", Provider: "oidc", Subject: id,
			}).Error)
		}
		for _, category := range []string{"invalid UUID", "duplicate target UUID"} {
			t.Run(category, func(t *testing.T) {
				a, err := adapter.PlanCommonRecovery(t.Context(), db)
				require.NoError(t, err)
				for i := range 8 {
					target := fmt.Sprintf("invalid-%d", i)
					if category == "duplicate target UUID" {
						// Four separate collisions still produce one capped group.
						target = fmt.Sprintf("11111111-1111-4111-8111-%012d", i/2)
					}
					a.IDs["users"][fmt.Sprintf("bad-%d", i)] = target
				}
				report, err := adapter.DiagnoseCommonRecovery(t.Context(), db, a)
				require.NoError(t, err)
				require.Len(t, report.Issues, 1)
				issue := report.Issues[0]
				require.True(t, strings.HasPrefix(issue, category+": users.id: 8 distinct values; examples: "))
				require.True(t, strings.HasSuffix(issue, "; 3 omitted"))
				previous := -1
				for i := range 5 {
					position := strings.Index(issue, fmt.Sprintf(`"bad-%d" -> %q`, i, a.IDs["users"][fmt.Sprintf("bad-%d", i)]))
					require.Greater(t, position, previous, "examples must be sorted and include both source and target")
					previous = position
				}
				for i := 5; i < 8; i++ {
					require.NotContains(t, issue, fmt.Sprintf(`"bad-%d"`, i))
				}
			})
		}
	})
}
