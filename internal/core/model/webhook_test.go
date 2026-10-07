package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidWebhookEvent(t *testing.T) {
	for _, typ := range []string{"*", "tenant.created.v1", "tenant_domain.deleted.v1", "organization.updated.v1", "member.deleted.v1", "organization_membership.created.v1"} {
		require.True(t, ValidWebhookEvent(typ), typ)
	}
	for _, typ := range []string{"", "tenant", "tenant.created", "tenant.created.v2", "tenant.renamed.v1", "role.created.v1", "tenant.created.v1.v1", "*.created.v1"} {
		require.False(t, ValidWebhookEvent(typ), typ)
	}
	// Every type the feed publishes can be subscribed to.
	for _, family := range []string{FamilyTenant, FamilyTenantDomain, FamilyOrganization, FamilyMember, FamilyOrganizationMembership} {
		for _, change := range []string{CommonEventCreated, CommonEventUpdated, CommonEventDeleted} {
			require.True(t, ValidWebhookEvent(CommonEventType(family, change)))
		}
	}
}

func TestParseWebhookID(t *testing.T) {
	_, err := ParseWebhookID("2ed07e0a-d163-4ab4-a36f-ebda3e06e51a")
	require.NoError(t, err)
	for _, raw := range []string{"", "2ED07E0A-D163-4AB4-A36F-EBDA3E06E51A", "not-a-uuid"} {
		_, err := ParseWebhookID(raw)
		require.Error(t, err, raw)
	}
}
