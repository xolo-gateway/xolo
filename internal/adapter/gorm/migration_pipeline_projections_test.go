package gorm

import (
	"testing"

	"github.com/ncruces/go-sqlite3/gormlite"
	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
	gormpkg "gorm.io/gorm"
)

// TestPipelineProjectionsBackfill projects the virtual models and
// middlewares written before they became business families, without
// publishing any event.
func TestPipelineProjectionsBackfill(t *testing.T) {
	db, err := gormpkg.Open(gormlite.Open(":memory:"), &gormpkg.Config{})
	require.NoError(t, err)
	require.NoError(t, MigrateDatabase(t.Context(), db, nil))

	var tenant Tenant
	require.NoError(t, db.First(&tenant, "slug = ?", model.DefaultTenantSlug).Error)
	org := model.NewOrganization(model.TenantID(tenant.ID), "acme", "Acme", "")
	require.NoError(t, NewStore(db).CreateOrg(t.Context(), org))
	// Written as an old server did: without any projection.
	require.NoError(t, db.Create(&VirtualModel{ID: "vm-1", OrgID: string(org.ID()), Name: "router",
		GraphJSON: `{"nodes":[{"id":"gen","type":"generator","position":{"x":0,"y":0}}],"edges":[]}`}).Error)
	require.NoError(t, db.Create(&Middleware{ID: "mw-1", OrgID: string(org.ID()), Name: "guard", Enabled: true, TargetsJSON: "[]", GraphJSON: "{}"}).Error)
	var events int64
	require.NoError(t, db.Model(&ProvisioningEvent{}).Count(&events).Error)

	require.NoError(t, db.Transaction(func(tx *gormpkg.DB) error { return migratePipelineProjections(tx) }))

	var projections []ProvisioningProjection
	require.NoError(t, db.Where("family IN ?", []string{model.FamilyVirtualModel, model.FamilyMiddleware}).Order("family").Find(&projections).Error)
	require.Len(t, projections, 2)
	require.JSONEq(t, `{"name":"guard","description":"","enabled":true,"priority":0,"applies_to_all":false,"targets":[],"graph":{"nodes":[],"edges":[]}}`, projections[0].Representation)
	require.JSONEq(t, `{"name":"router","description":"","catalog_overrides":null,"graph":{"nodes":[{"id":"gen","type":"generator","position":{"x":0,"y":0},"data":{}}],"edges":[]}}`, projections[1].Representation)
	var after int64
	require.NoError(t, db.Model(&ProvisioningEvent{}).Count(&after).Error)
	require.Equal(t, events, after, "a backfill publishes nothing")
}
