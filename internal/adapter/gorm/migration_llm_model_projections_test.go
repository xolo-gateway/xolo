package gorm

import (
	"testing"

	"github.com/ncruces/go-sqlite3/gormlite"
	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
	gormpkg "gorm.io/gorm"
)

// TestLLMModelProjectionsBackfill projects the models and the personal
// virtual models written before they became business families, disabled
// models included, without publishing any event.
func TestLLMModelProjectionsBackfill(t *testing.T) {
	db, err := gormpkg.Open(gormlite.Open(":memory:"), &gormpkg.Config{})
	require.NoError(t, err)
	require.NoError(t, MigrateDatabase(t.Context(), db, nil))

	var tenant Tenant
	require.NoError(t, db.First(&tenant, "slug = ?", model.DefaultTenantSlug).Error)
	org := model.NewOrganization(model.TenantID(tenant.ID), "acme", "Acme", "")
	require.NoError(t, NewStore(db).CreateOrg(t.Context(), org))
	// Written as an old server did: without any projection.
	require.NoError(t, db.Create(&Provider{ID: "provider-1", OrgID: string(org.ID()), Name: "OpenAI", Type: "openai", APIKey: "x", Active: 1}).Error)
	require.NoError(t, db.Create(&LLMModel{ID: "model-1", ProviderID: "provider-1", OrgID: string(org.ID()), ProxyName: "gpt-4o", RealModel: "gpt-4o", Enabled: 1}).Error)
	require.NoError(t, db.Create(&LLMModel{ID: "model-2", ProviderID: "provider-1", OrgID: string(org.ID()), ProxyName: "meta/embed", RealModel: "embed", Kind: string(model.ModelKindEmbeddings)}).Error)
	user := model.NewUser(model.TenantID(tenant.ID), "", "", "owner@example.test", "", true, model.PlatformRoleUser)
	require.NoError(t, NewStore(db).SaveUser(t.Context(), user))
	require.NoError(t, db.Create(&PersonalVirtualModel{ID: "pvm-1", UserID: string(user.ID()), Name: "router", GraphJSON: "{}"}).Error)
	require.NoError(t, db.Where("family = ?", model.FamilyPersonalVirtualModel).Delete(&ProvisioningProjection{}).Error)
	var events int64
	require.NoError(t, db.Model(&ProvisioningEvent{}).Count(&events).Error)

	require.NoError(t, db.Transaction(func(tx *gormpkg.DB) error { return migrateLLMModelProjections(tx) }))

	var projections []ProvisioningProjection
	require.NoError(t, db.Where("family = ?", model.FamilyLLMModel).Order("resource_key").Find(&projections).Error)
	require.Len(t, projections, 2)
	require.JSONEq(t, `{"provider_id":"provider-1","proxy_name":"gpt-4o","kind":"completion","enabled":true}`, projections[0].Representation)
	require.JSONEq(t, `{"provider_id":"provider-1","proxy_name":"meta/embed","kind":"embeddings","enabled":false}`, projections[1].Representation)
	var personal ProvisioningProjection
	require.NoError(t, db.Where("family = ?", model.FamilyPersonalVirtualModel).First(&personal).Error)
	require.Equal(t, string(user.ID())+":pvm-1", personal.ResourceKey)
	require.JSONEq(t, `{"name":"router","description":"","team_id":"","catalog_overrides":null,"graph":{"nodes":[],"edges":[]}}`, personal.Representation)
	require.JSONEq(t, `{"tenant_id":"`+tenant.ID+`","member_id":"`+string(user.ID())+`","resource_id":"pvm-1"}`, personal.Reference)
	var after int64
	require.NoError(t, db.Model(&ProvisioningEvent{}).Count(&after).Error)
	require.Equal(t, events, after, "a backfill publishes nothing")
}
