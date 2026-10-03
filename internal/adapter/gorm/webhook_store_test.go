package gorm_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
)

func webhookFixture(t *testing.T, db *gorm.DB) (*adapter.Store, model.Tenant, string) {
	s := adapter.NewStore(db)
	require.NoError(t, s.Migrate(t.Context()))
	tenant, err := s.GetTenantBySlug(t.Context(), "default")
	require.NoError(t, err)
	id := string(model.NewTenantID())
	_, err = s.PutWebhook(t.Context(), string(tenant.ID()), id, model.WebhookSettings{Destination: "https://console.example.test/hooks", Events: []string{"*"}, Enabled: true, EncryptedSecrets: "encrypted-fixture", SecretCount: 1})
	require.NoError(t, err)
	return s, tenant, id
}
func TestWebhookDurableQueue(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		s, tenant, id := webhookFixture(t, db)
		ctx := t.Context()
		require.NoError(t, s.SaveTenant(ctx, model.UpdateTenant(tenant, model.WithTenantName("Changed"))))
		require.NoError(t, s.PrepareWebhooks(ctx, 100))
		require.NoError(t, s.PrepareWebhooks(ctx, 100))
		var count int64
		require.NoError(t, db.Model(&adapter.WebhookDelivery{}).Count(&count).Error)
		require.EqualValues(t, 1, count)
		// Two connections race to reserve one row; only one obtains it.
		start := make(chan struct{})
		results := make(chan *model.WebhookJob, 2)
		errs := make(chan error, 2)
		for range 2 {
			go func() { <-start; job, err := s.ClaimWebhook(ctx); results <- job; errs <- err }()
		}
		close(start)
		var job *model.WebhookJob
		claims := 0
		for range 2 {
			got := <-results
			require.NoError(t, <-errs)
			if got != nil {
				job = got
				claims++
			}
		}
		require.Equal(t, 1, claims)
		require.Equal(t, string(tenant.ID()), job.TenantID)
		require.Equal(t, id, job.SubscriptionID)
		// Crash after reserve: a restarted store recovers the expired lease. A stale
		// worker cannot overwrite the new worker's accepted result.
		require.NoError(t, db.Model(&adapter.WebhookDelivery{}).Where("id = ?", job.ID).Update("lease_until", time.Now().UTC().Add(-time.Minute)).Error)
		recovered, err := adapter.NewStore(db).ClaimWebhook(ctx)
		require.NoError(t, err)
		require.NotNil(t, recovered)
		require.Equal(t, job.Body, recovered.Body)
		require.Equal(t, job.EventID, recovered.EventID)
		require.NotEqual(t, job.Lease, recovered.Lease)
		require.ErrorIs(t, s.FinishWebhook(ctx, job, model.WebhookResult{Success: true, Diagnostic: "accepted"}), port.ErrWebhookLeaseLost)
		require.NoError(t, s.FinishWebhook(ctx, recovered, model.WebhookResult{Diagnostic: strings.Repeat("secret", 1000), StatusCode: 503}))
		var stored adapter.WebhookDelivery
		require.NoError(t, db.First(&stored, "id = ?", job.ID).Error)
		require.Equal(t, "delivery_failed", stored.Diagnostic)
		require.Greater(t, stored.NextAttempt.Sub(time.Now().UTC()), time.Second)
		require.NoError(t, db.Model(&adapter.WebhookDelivery{}).Where("id = ?", job.ID).Update("next_attempt", time.Now().UTC().Add(-time.Second)).Error)
		recovered, err = s.ClaimWebhook(ctx)
		require.NoError(t, err)
		require.NotNil(t, recovered)
		require.NoError(t, s.FinishWebhook(ctx, recovered, model.WebhookResult{Success: true, Diagnostic: "accepted"}))
		stats, err := s.WebhookStats(ctx)
		require.NoError(t, err)
		require.EqualValues(t, 1, stats.Delivered)
		require.Zero(t, stats.InFlight)
		// Completed payloads are retained independently of feed retention, then purged.
		require.NoError(t, s.PurgeCommonEvents(ctx, time.Now().UTC().Add(time.Hour)))
		require.NoError(t, db.Model(&adapter.WebhookDelivery{}).Where("id = ?", job.ID).Update("finished_at", time.Now().UTC().Add(-8*24*time.Hour)).Error)
		require.NoError(t, s.PrepareWebhooks(ctx, 100))
		require.NoError(t, db.Model(&adapter.WebhookDelivery{}).Count(&count).Error)
		require.Zero(t, count)
	})
}
func TestWebhookHistoryLossBackpressureAndScope(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		s, tenant, id := webhookFixture(t, db)
		ctx := t.Context()
		for i := range 3 {
			require.NoError(t, s.SaveTenant(ctx, model.UpdateTenant(tenant, model.WithTenantName(fmt.Sprint("Change", i)))))
		}
		require.NoError(t, s.PrepareWebhooks(ctx, 1))
		sub, err := s.GetWebhook(ctx, string(tenant.ID()), id)
		require.NoError(t, err)
		require.Equal(t, "backpressure", sub.State)
		position := sub.Position
		require.NoError(t, s.PurgeCommonEvents(ctx, time.Now().UTC().Add(time.Hour)))
		require.NoError(t, s.PrepareWebhooks(ctx, 1))
		sub, err = s.GetWebhook(ctx, string(tenant.ID()), id)
		require.NoError(t, err)
		require.Equal(t, "history_lost", sub.State)
		require.Equal(t, position, sub.Position)
		// Already materialized payload survives complete event-feed purge.
		job, err := s.ClaimWebhook(ctx)
		require.NoError(t, err)
		require.NotNil(t, job)
		require.NotEmpty(t, job.Body)
		foreign := model.NewTenant("webhook-foreign", "Foreign", "")
		require.NoError(t, s.CreateTenant(ctx, foreign))
		_, err = s.GetWebhook(ctx, string(foreign.ID()), id)
		require.ErrorIs(t, err, port.ErrNotFound)
		require.ErrorIs(t, s.DeleteWebhook(ctx, string(foreign.ID()), id), port.ErrNotFound)
		require.NoError(t, s.ResetWebhook(ctx, string(tenant.ID()), id))
		require.ErrorIs(t, s.FinishWebhook(ctx, job, model.WebhookResult{Success: true}), port.ErrWebhookLeaseLost)
		sub, err = s.GetWebhook(ctx, string(tenant.ID()), id)
		require.NoError(t, err)
		require.Equal(t, "ready", sub.State)
		require.Greater(t, sub.Position, position)
		require.NoError(t, s.SaveTenant(ctx, model.UpdateTenant(tenant, model.WithTenantName("after reset"))))
		require.NoError(t, s.PrepareWebhooks(ctx, 100))
		require.NoError(t, deleteAndPurgeTenant(t, s, tenant.ID()))
		var n int64
		require.NoError(t, db.Model(&adapter.WebhookDelivery{}).Count(&n).Error)
		require.Zero(t, n)
		require.NoError(t, db.Model(&adapter.WebhookSubscription{}).Count(&n).Error)
		require.Zero(t, n)
	})
}
func TestWebhookPauseRotationAndExpiry(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		s, tenant, id := webhookFixture(t, db)
		ctx := t.Context()
		require.NoError(t, s.SaveTenant(ctx, model.UpdateTenant(tenant, model.WithTenantName("queued"))))
		require.NoError(t, s.PrepareWebhooks(ctx, 100))
		p := model.WebhookSettings{Destination: "https://console.example.test/new", Events: []string{"*"}, Enabled: false}
		_, err := s.PutWebhook(ctx, string(tenant.ID()), id, p)
		require.NoError(t, err)
		job, err := s.ClaimWebhook(ctx)
		require.NoError(t, err)
		require.Nil(t, job)
		p.Enabled = true
		p.EncryptedSecrets = "rotated"
		p.SecretCount = 2
		_, err = s.PutWebhook(ctx, string(tenant.ID()), id, p)
		require.NoError(t, err)
		job, err = s.ClaimWebhook(ctx)
		require.NoError(t, err)
		require.Equal(t, "rotated", job.EncryptedSecrets)
		require.Equal(t, p.Destination, job.Destination)
		require.NoError(t, db.Model(&adapter.WebhookDelivery{}).Where("id = ?", job.ID).Updates(map[string]any{"lease_until": time.Now().UTC().Add(-time.Hour), "created_at": time.Now().UTC().Add(-25 * time.Hour)}).Error)
		require.NoError(t, s.PrepareWebhooks(ctx, 100))
		stats, err := s.WebhookStats(ctx)
		require.NoError(t, err)
		require.EqualValues(t, 1, stats.Failed)
		job, err = s.ClaimWebhook(ctx)
		require.NoError(t, err)
		require.Nil(t, job)
	})
}

func TestWebhookAtomicMaterializationAndResultFailure(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		s, tenant, id := webhookFixture(t, db)
		ctx := t.Context()
		before, err := s.GetWebhook(ctx, string(tenant.ID()), id)
		require.NoError(t, err)
		require.NoError(t, s.SaveTenant(ctx, model.UpdateTenant(tenant, model.WithTenantName("Atomic"))))
		// Crash/error between delivery insertion and checkpoint update rolls back both.
		require.NoError(t, db.Callback().Update().Before("gorm:update").Register("webhook:checkpoint-failure", func(tx *gorm.DB) {
			if tx.Statement.Table == "webhook_subscriptions" {
				tx.AddError(fmt.Errorf("injected checkpoint failure"))
			}
		}))
		require.Error(t, s.PrepareWebhooks(ctx, 100))
		require.NoError(t, db.Callback().Update().Remove("webhook:checkpoint-failure"))
		after, err := s.GetWebhook(ctx, string(tenant.ID()), id)
		require.NoError(t, err)
		require.Equal(t, before.Position, after.Position)
		var n int64
		require.NoError(t, db.Model(&adapter.WebhookDelivery{}).Count(&n).Error)
		require.Zero(t, n)
		require.NoError(t, s.PrepareWebhooks(ctx, 100))
		job, err := s.ClaimWebhook(ctx)
		require.NoError(t, err)
		require.NotNil(t, job)
		require.NoError(t, db.Callback().Update().Before("gorm:update").Register("webhook:result-failure", func(tx *gorm.DB) {
			if tx.Statement.Table == "webhook_deliveries" {
				tx.AddError(fmt.Errorf("injected result failure"))
			}
		}))
		require.Error(t, s.FinishWebhook(ctx, job, model.WebhookResult{Success: true, Diagnostic: "accepted"}))
		require.NoError(t, db.Callback().Update().Remove("webhook:result-failure"))
		require.NoError(t, db.Model(&adapter.WebhookDelivery{}).Where("id = ?", job.ID).Update("lease_until", time.Now().UTC().Add(-time.Minute)).Error)
		recovered, err := adapter.NewStore(db).ClaimWebhook(ctx)
		require.NoError(t, err)
		require.NotNil(t, recovered)
		require.Equal(t, job.EventID, recovered.EventID)
		require.Equal(t, job.Body, recovered.Body)
		// The receiver may have accepted the previous attempt; this remains at-least-once.
		require.NoError(t, s.FinishWebhook(ctx, recovered, model.WebhookResult{Success: true, Diagnostic: "accepted"}))
	})
}
func TestWebhookAutomaticUpgrade(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		s, tenant, _ := webhookFixture(t, db)
		ctx := t.Context()
		require.NoError(t, s.SaveTenant(ctx, model.UpdateTenant(tenant, model.WithTenantName("Before upgrade"))))
		var feed adapter.CommonFeed
		require.NoError(t, db.First(&feed, 1).Error)
		var publications []adapter.Publication
		require.NoError(t, db.Order("sequence").Find(&publications).Error)
		require.NoError(t, db.Migrator().DropTable(&adapter.WebhookDelivery{}, &adapter.WebhookSubscription{}))
		require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", "202610020003").Error)
		require.NoError(t, adapter.NewStore(db).Migrate(ctx))
		var restored adapter.CommonFeed
		require.NoError(t, db.First(&restored, 1).Error)
		require.Equal(t, feed, restored)
		var after []adapter.Publication
		require.NoError(t, db.Order("sequence").Find(&after).Error)
		require.Equal(t, publications, after)
		require.True(t, db.Migrator().HasTable(&adapter.WebhookDelivery{}))
		require.True(t, db.Migrator().HasTable(&adapter.WebhookSubscription{}))
	})
}

func TestWebhookBoundedMaterializationIsFair(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		s, tenant, id := webhookFixture(t, db)
		ctx := t.Context()
		second := string(model.NewTenantID())
		_, err := s.PutWebhook(ctx, string(tenant.ID()), second, model.WebhookSettings{Destination: "https://console.example.test/hooks", Events: []string{"*"}, Enabled: true, EncryptedSecrets: "fixture", SecretCount: 1})
		require.NoError(t, err)
		require.NoError(t, s.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
			for i := range 110 {
				u := model.NewUser(tenant.ID(), "", "", fmt.Sprintf("batch%d@fixture", i), "Batch", true)
				if err := tx.SaveUser(ctx, u); err != nil {
					return err
				}
			}
			return nil
		}))
		require.NoError(t, s.PrepareWebhooks(ctx, 1000))
		var count int64
		require.NoError(t, db.Model(&adapter.WebhookDelivery{}).Count(&count).Error)
		require.EqualValues(t, 100, count)
		a, err := s.GetWebhook(ctx, string(tenant.ID()), id)
		require.NoError(t, err)
		b, err := s.GetWebhook(ctx, string(tenant.ID()), second)
		require.NoError(t, err)
		require.NotEqual(t, a.Position, b.Position)
		require.NoError(t, s.PrepareWebhooks(ctx, 1000))
		a, err = s.GetWebhook(ctx, string(tenant.ID()), id)
		require.NoError(t, err)
		b, err = s.GetWebhook(ctx, string(tenant.ID()), second)
		require.NoError(t, err)
		require.Equal(t, a.Position, b.Position)
		for range 2 {
			require.NoError(t, s.PrepareWebhooks(ctx, 1000))
		}
		require.NoError(t, db.Model(&adapter.WebhookDelivery{}).Count(&count).Error)
		require.EqualValues(t, 220, count)
	})
}
