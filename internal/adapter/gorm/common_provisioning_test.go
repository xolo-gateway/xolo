package gorm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	"gorm.io/gorm"
)

func TestCommonPUTTransactions(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		s := adapter.NewStore(db)
		ctx := t.Context()
		require.NoError(t, s.Migrate(ctx))
		api := service.NewProvisioningService(s, s, s, s, service.WithMultiTenant(true))
		tid := model.NewTenantID()
		oid := model.NewOrgID()
		uid := model.NewUserID()
		resource := service.CommonResource{Slug: " example ", Name: " Example ", Status: "active"}
		tenant, err := api.PutCommonTenant(ctx, tid, resource)
		require.NoError(t, err)
		org, err := api.PutCommonOrganization(ctx, tid, oid, resource)
		require.NoError(t, err)
		member, err := api.PutCommonMember(ctx, tid, uid, service.CommonMember{Email: "A@B", TenantRole: "owner", Status: "active"})
		require.NoError(t, err)
		membership, err := api.PutCommonMembership(ctx, tid, oid, uid, service.CommonMembership{Role: "owner", Status: "active"})
		require.NoError(t, err)
		domain, err := api.PutCommonDomain(ctx, tid, "EXAMPLE.TEST", "active")
		require.NoError(t, err)
		// Snapshot rows, timestamps, associations and durable facts for all families.
		snapshot := func() map[string][]map[string]any {
			result := map[string][]map[string]any{}
			for _, table := range []string{"tenants", "organizations", "users", "memberships", "membership_roles", "domains", "events", "mutation_audits", "publications"} {
				var rows []map[string]any
				require.NoError(t, db.Table(table).Find(&rows).Error)
				result[table] = rows
			}
			return result
		}
		before := snapshot()
		_, err = api.PutCommonTenant(ctx, tid, tenant)
		require.NoError(t, err)
		_, err = api.PutCommonOrganization(ctx, tid, oid, org)
		require.NoError(t, err)
		_, err = api.PutCommonMember(ctx, tid, uid, member)
		require.NoError(t, err)
		_, err = api.PutCommonMembership(ctx, tid, oid, uid, membership)
		require.NoError(t, err)
		_, err = api.PutCommonDomain(ctx, tid, "example.test", domain)
		require.NoError(t, err)
		require.Equal(t, before, snapshot())
		// Distinct connections begin together. The final owner transition is checked
		// inside the same transaction as the writes, never in an HTTP pre-check.
		other := model.NewUserID()
		_, err = api.PutCommonMember(ctx, tid, other, service.CommonMember{Email: "other@b", TenantRole: "owner", Status: "active"})
		require.NoError(t, err)
		_, err = api.PutCommonMembership(ctx, tid, oid, other, membership)
		require.NoError(t, err)
		for _, scope := range []string{"tenant", "organization"} {
			t.Run(scope, func(t *testing.T) {
				start := make(chan struct{})
				results := make(chan error, 2)
				for _, id := range []model.UserID{uid, other} {
					go func(id model.UserID) {
						<-start
						var err error
						if scope == "tenant" {
							email := "a@b"
							if id == other {
								email = "other@b"
							}
							_, err = api.PutCommonMember(context.Background(), tid, id, service.CommonMember{Email: email, TenantRole: "owner", Status: "suspended"})
						} else {
							_, err = api.PutCommonMembership(context.Background(), tid, oid, id, service.CommonMembership{Role: "member", Status: "active"})
						}
						results <- err
					}(id)
				}
				close(start)
				success, rejected := 0, 0
				for range 2 {
					err := <-results
					if err == nil {
						success++
					} else if errors.Is(err, port.ErrLastOwner) {
						rejected++
					} else {
						require.NoError(t, err)
					}
				}
				require.Equal(t, 1, success)
				require.Equal(t, 1, rejected)
				var remaining int64
				if scope == "tenant" {
					require.NoError(t, db.Model(&adapter.User{}).Where("tenant_id = ? AND tenant_role = ? AND active = ?", tid, "owner", true).Count(&remaining).Error)
				} else {
					require.NoError(t, db.Model(&adapter.Membership{}).Where("org_id = ? AND common_role = ? AND status = ?", oid, "owner", "active").Count(&remaining).Error)
				}
				require.EqualValues(t, 1, remaining)
			})
		}
	})
}

func TestDomainRoutingMigration(t *testing.T) {
	eachBackend(t, func(t *testing.T, s *adapter.Store) {
		ctx := t.Context()
		require.NoError(t, s.InitializeDomainRouting(ctx, "{tenant}.example.test", "test"))
		domain, err := s.GetDomain(ctx, "test.example.test")
		require.NoError(t, err)
		require.Equal(t, testTenantID, domain.TenantID)
		domain.Status = model.StatusSuspended
		require.NoError(t, s.SaveDomain(ctx, domain))
		tenant, err := s.GetTenantByID(ctx, testTenantID)
		require.NoError(t, err)
		require.NoError(t, s.SaveTenant(ctx, model.UpdateTenant(tenant, model.WithTenantSlug("renamed"))))
		require.NoError(t, s.InitializeDomainRouting(ctx, "{tenant}.other.test"))
		shared, err := s.GetSharedTenant(ctx, "test")
		require.NoError(t, err)
		require.Equal(t, testTenantID, shared.ID())
		domain, err = s.GetDomain(ctx, "test.example.test")
		require.NoError(t, err)
		require.Equal(t, model.StatusSuspended, domain.Status)
		_, err = s.GetDomain(ctx, "renamed.other.test")
		require.ErrorIs(t, err, port.ErrNotFound)
	})
}
