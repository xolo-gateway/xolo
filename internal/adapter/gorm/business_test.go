package gorm_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	"gorm.io/gorm"
)

func TestBusinessResourcesConditionsOwnershipAndScope(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		s := adapter.NewStore(db)
		ctx := t.Context()
		require.NoError(t, s.Migrate(ctx))
		s.ConfigureBusiness(true, strings.Repeat("11", 32))
		api := service.NewProvisioningService(s, s, s, s, service.WithMultiTenant(true))
		tenant, err := s.GetTenantBySlug(ctx, model.DefaultTenantSlug)
		require.NoError(t, err)
		tid := string(tenant.ID())
		org := model.NewOrganization(tenant.ID(), "business", "Business", "")
		require.NoError(t, s.CreateOrg(ctx, org))
		oid := string(org.ID())
		roleID := string(model.NewRoleID())
		appID := string(model.NewApplicationID())
		quotaID := string(model.NewQuotaID())
		alertID := string(model.NewAlertID())
		providerID := string(model.NewProviderID())
		secret := "provider-secret"
		resources := []struct {
			family, key string
			p           model.BusinessSettings
		}{
			{"custom_role", roleID, model.BusinessSettings{Role: &model.CustomRoleSettings{Name: "custom", Permissions: []string{}, ModelGrants: []model.ModelGrantSettings{}}}},
			{"application", appID, model.BusinessSettings{Application: &model.ApplicationSettings{Name: "App", Active: true, RoleIDs: []string{roleID}}}},
			{"quota", quotaID, model.BusinessSettings{Quota: &model.QuotaSettings{Scope: model.QuotaScopeApplication, ScopeID: appID, Currency: "USD"}}},
			{"alert", alertID, model.BusinessSettings{Alert: &model.AlertSettings{Name: "Alert", Scope: model.AlertScopeOrg, Query: "{}", Aggregation: model.AggregationCount, WindowSeconds: 60, Comparator: model.ComparatorGT, Enabled: true}}},
			{"provider", providerID, model.BusinessSettings{Provider: &model.ProviderSettings{Name: "Provider", Type: "openai", BaseURL: "https://api.example", Active: true, Currency: "USD", BillingMode: model.BillingModePayg, APIKey: &secret}}},
		}
		policy := model.OwnershipPolicy{}
		for _, f := range model.OwnershipFamilies {
			policy[f] = model.OwnerControlPlane
		}
		require.NoError(t, s.ConfigureOwnership(policy))
		ctx = model.WithWriteAuthority(ctx, model.OwnerControlPlane)
		for _, r := range resources {
			t.Run(r.family, func(t *testing.T) {
				scope := model.CommonScope{Family: r.family, TenantID: tid, OrganizationID: oid}
				if r.family == "quota" {
					scope.OrganizationID = ""
				}
				_, err := api.PutBusiness(ctx, scope, r.key, model.MatchCondition{Present: true, Any: true}, r.p)
				require.ErrorIs(t, err, port.ErrPreconditionFailed)
				item, err := api.PutBusiness(ctx, scope, r.key, model.MatchCondition{}, r.p)
				require.NoError(t, err)
				require.NotContains(t, string(item.Representation), secret)
				require.NotContains(t, string(item.Representation), "api_key")
				table := map[string]string{"custom_role": "roles", "application": "applications", "quota": "quota", "alert": "alerts", "provider": "providers"}[r.family]
				var before, after map[string]any
				require.NoError(t, db.Table(table).Where("id = ?", r.key).Take(&before).Error)
				repeat, err := api.PutBusiness(ctx, scope, r.key, model.MatchCondition{}, r.p)
				require.NoError(t, err)
				require.Equal(t, item, repeat)
				require.NoError(t, db.Table(table).Where("id = ?", r.key).Take(&after).Error)
				require.Equal(t, before, after, "no-op must not change storage timestamps")
				page, err := s.ListCommon(ctx, scope, "", 1)
				require.NoError(t, err)
				require.Len(t, page.Items, 1)
				c, err := model.ParseMatchCondition([]string{`W/"u-1"`})
				require.NoError(t, err)
				_, err = api.PutBusiness(ctx, scope, r.key, c, r.p)
				require.ErrorIs(t, err, port.ErrPreconditionFailed)
				_, err = api.PutBusiness(model.WithWriteAuthority(ctx, model.OwnerLocal), scope, r.key, model.MatchCondition{}, r.p)
				require.ErrorIs(t, err, port.ErrOwnershipDenied)
				foreign := scope
				foreign.TenantID = string(model.NewTenantID())
				_, err = api.PutBusiness(ctx, foreign, r.key, model.MatchCondition{}, r.p)
				require.Error(t, err)
			})
		}
		var pubs []adapter.Publication
		require.NoError(t, db.Find(&pubs).Error)
		for _, p := range pubs {
			require.NotContains(t, p.Payload, secret)
		}
		// Local UI writers share the configured family authority.
		require.ErrorIs(t, s.UpdateApplication(model.WithWriteAuthority(ctx, model.OwnerLocal), model.NewApplication(org.ID(), "other", "", true)), port.ErrOwnershipDenied)
		_, err = s.ScheduleDeletion(ctx, model.CommonScope{Family: "organization", TenantID: tid}, oid, model.MatchCondition{}, time.Second)
		require.NoError(t, err)
		_, err = api.PutBusiness(ctx, model.CommonScope{Family: "provider", TenantID: tid, OrganizationID: oid}, providerID, model.MatchCondition{}, resources[4].p)
		require.ErrorIs(t, err, port.ErrResourceDeleted)
		scope := model.CommonScope{Family: "organization", TenantID: tid}
		d, err := s.ReadDeletion(ctx, scope, oid)
		require.NoError(t, err)
		raw, err := s.ExportDeletion(ctx, scope, oid)
		require.NoError(t, err)
		var archive struct {
			SHA256  string
			Payload struct{ Tables map[string][]map[string]any }
		}
		require.NoError(t, json.Unmarshal(raw, &archive))
		for _, table := range []string{"roles", "applications", "quota", "alerts", "providers"} {
			expected := 1
			if table == "roles" {
				expected = 4
			}
			require.Len(t, archive.Payload.Tables[table], expected, table)
		}
		require.NotContains(t, string(raw), secret, "provider credentials must remain encrypted in the archive")
		c, err := model.ParseMatchCondition([]string{d.ETag})
		require.NoError(t, err)
		_, err = s.ConfirmDeletion(ctx, scope, oid, c, archive.SHA256)
		require.NoError(t, err)
		require.NoError(t, db.Model(&adapter.ResourceDeletion{}).Where("resource_id = ?", oid).Update("purge_after", time.Now().Add(-time.Hour)).Error)
		require.NoError(t, s.PurgeDeletion(ctx, scope, oid))
		for _, table := range []string{"roles", "applications", "application_roles", "quota", "alerts", "providers"} {
			var n int64
			require.NoError(t, db.Table(table).Count(&n).Error)
			require.Zero(t, n, table)
		}
		var stale int64
		require.NoError(t, db.Model(&adapter.CommonRecord{}).Where("family IN ?", []string{"custom_role", "application", "quota", "alert", "provider"}).Count(&stale).Error)
		require.Zero(t, stale)
		// A writer that retained only an application ID before parent purge
		// cannot recreate an orphan quota after that parent row disappeared.
		require.ErrorIs(t, s.SetQuota(ctx, model.NewQuota(model.QuotaScopeApplication, appID, "USD", nil, nil, nil)), port.ErrResourceDeleted)
	})
}

func TestBusinessRoleGrantSetsAndProviderRotation(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		ctx := t.Context()
		s := adapter.NewStore(db)
		require.NoError(t, s.Migrate(ctx))
		s.ConfigureBusiness(true, strings.Repeat("11", 32))
		api := service.NewProvisioningService(s, s, s, s, service.WithMultiTenant(true))
		tenant, err := s.GetTenantBySlug(ctx, "default")
		require.NoError(t, err)
		org := model.NewOrganization(tenant.ID(), "grants", "Grants", "")
		require.NoError(t, s.CreateOrg(ctx, org))
		tid, oid := string(tenant.ID()), string(org.ID())
		providerKey := string(model.NewProviderID())
		firstSecret := "first"
		secondSecret := "second"
		provider := model.BusinessSettings{Provider: &model.ProviderSettings{Name: "Provider", Type: "openai", Currency: "USD", BillingMode: model.BillingModePayg, Active: true, APIKey: &firstSecret}}
		scope := model.CommonScope{Family: "provider", TenantID: tid, OrganizationID: oid}
		before, err := api.PutBusiness(ctx, scope, providerKey, model.MatchCondition{}, provider)
		require.NoError(t, err)
		provider.Provider.APIKey = &secondSecret
		after, err := api.PutBusiness(ctx, scope, providerKey, model.MatchCondition{}, provider)
		require.NoError(t, err)
		require.Equal(t, before.Representation, after.Representation)
		require.NotEqual(t, before.ETag, after.ETag)
		require.NoError(t, db.Create(&adapter.VirtualModel{ID: "a-virtual", OrgID: oid, Name: "virtual"}).Error)
		require.NoError(t, db.Create(&adapter.LLMModel{ID: "z-llm", OrgID: oid, ProviderID: providerKey, ProxyName: "llm", RealModel: "llm"}).Error)
		scope.Family = "custom_role"
		key := string(model.NewRoleID())
		settings := model.BusinessSettings{Role: &model.CustomRoleSettings{Name: "Role", Permissions: []string{}, ModelGrants: []model.ModelGrantSettings{{ModelID: "z-llm", Kind: "llm"}, {ModelID: "a-virtual", Kind: "virtual"}}}}
		before, err = api.PutBusiness(ctx, scope, key, model.MatchCondition{}, settings)
		require.NoError(t, err)
		var storedBefore, storedAfter adapter.Role
		require.NoError(t, db.First(&storedBefore, "id = ?", key).Error)
		settings.Role.ModelGrants = []model.ModelGrantSettings{{ModelID: "a-virtual", Kind: "virtual"}, {ModelID: "z-llm", Kind: "llm"}, {ModelID: "a-virtual", Kind: "virtual"}}
		after, err = api.PutBusiness(ctx, scope, key, model.MatchCondition{}, settings)
		require.NoError(t, err)
		require.NoError(t, db.First(&storedAfter, "id = ?", key).Error)
		require.Equal(t, before, after)
		require.Equal(t, storedBefore.UpdatedAt, storedAfter.UpdatedAt)
		var publications []adapter.Publication
		require.NoError(t, db.Find(&publications).Error)
		for _, p := range publications {
			require.NotContains(t, p.Payload, firstSecret)
			require.NotContains(t, p.Payload, secondSecret)
		}
	})
}
