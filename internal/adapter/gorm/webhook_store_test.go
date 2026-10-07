package gorm_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	gormpkg "gorm.io/gorm"
)

var roomyWebhooks = model.WebhookCapacity{Queue: 1000, Subscription: 1000}

const testWebhookSecret = "encrypted-test-secret"

func putHook(t *testing.T, store *xologorm.Store, tenant model.TenantID, events ...string) model.WebhookSubscription {
	t.Helper()
	if len(events) == 0 {
		events = []string{"*"}
	}
	hook, err := store.PutWebhook(t.Context(), tenant, model.WebhookID(uuid.NewString()), model.WebhookSettings{Destination: "https://hooks.example.test/in", Events: events, Enabled: true, EncryptedSecrets: testWebhookSecret, SecretCount: 1})
	require.NoError(t, err)
	return hook
}

// touchTenant publishes one tenant.updated.v1 event of the tenant.
func touchTenant(t *testing.T, store *xologorm.Store, tenant model.TenantID, opts ...model.TenantOption) {
	t.Helper()
	current, err := store.GetTenantByID(t.Context(), tenant)
	require.NoError(t, err)
	if len(opts) == 0 {
		opts = []model.TenantOption{model.WithTenantName("Name " + uuid.NewString()[:8])}
	}
	require.NoError(t, store.SaveTenant(t.Context(), model.UpdateTenant(current, opts...)))
}

func newWebhookTenant(t *testing.T, store *xologorm.Store, slug string) model.TenantID {
	t.Helper()
	tenant := model.NewTenant(slug, slug, "")
	require.NoError(t, store.CreateTenant(t.Context(), tenant))
	return tenant.ID()
}

func hookDeliveries(t *testing.T, db *gormpkg.DB, hook model.WebhookSubscription) []xologorm.WebhookDelivery {
	t.Helper()
	var rows []xologorm.WebhookDelivery
	require.NoError(t, db.Where("subscription_id = ?", string(hook.ID)).Order("sequence").Find(&rows).Error)
	return rows
}

func hookRow(t *testing.T, db *gormpkg.DB, hook model.WebhookSubscription) xologorm.WebhookSubscription {
	t.Helper()
	var row xologorm.WebhookSubscription
	require.NoError(t, db.Take(&row, "id = ?", string(hook.ID)).Error)
	return row
}

func expireLeases(t *testing.T, db *gormpkg.DB) {
	t.Helper()
	past := time.Now().UTC().Add(-time.Minute)
	require.NoError(t, db.Model(&xologorm.WebhookDelivery{}).Where("1 = 1").Updates(map[string]any{"lease_until": past, "next_attempt": past}).Error)
}

func TestWebhookSubscriptions(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newSeededStore(t, db)
		ctx := t.Context()
		other := newWebhookTenant(t, store, "other")

		_, err := store.PutWebhook(ctx, model.TenantID(uuid.NewString()), model.WebhookID(uuid.NewString()), model.WebhookSettings{EncryptedSecrets: testWebhookSecret})
		require.ErrorIs(t, err, port.ErrParentNotFound)
		_, err = store.PutWebhook(ctx, testTenantID, model.WebhookID(uuid.NewString()), model.WebhookSettings{Destination: "https://hooks.example.test", Events: []string{"*"}})
		require.ErrorIs(t, err, port.ErrInvalid, "a new subscription needs secrets")

		hook := putHook(t, store, testTenantID, "tenant.updated.v1")
		require.Equal(t, model.WebhookReady, hook.State)
		require.Equal(t, lastEvent(t, db).Sequence, hook.Position, "a new subscription starts at the end of the feed")

		// Omitted secrets are kept.
		updated, err := store.PutWebhook(ctx, testTenantID, hook.ID, model.WebhookSettings{Destination: "https://hooks.example.test/next", Events: []string{"*"}, Enabled: false})
		require.NoError(t, err)
		require.Equal(t, 1, updated.SecretCount)
		require.Equal(t, testWebhookSecret, hookRow(t, db, hook).EncryptedSecrets)

		// Identifiers never move to another tenant, nor are visible from it.
		_, err = store.PutWebhook(ctx, other, hook.ID, model.WebhookSettings{Destination: "https://hooks.example.test", Events: []string{"*"}, EncryptedSecrets: testWebhookSecret, SecretCount: 1})
		require.ErrorIs(t, err, port.ErrAlreadyExists)
		_, err = store.GetWebhook(ctx, other, hook.ID)
		require.ErrorIs(t, err, port.ErrNotFound)
		require.ErrorIs(t, store.DeleteWebhook(ctx, other, hook.ID), port.ErrNotFound)
		require.ErrorIs(t, store.ResetWebhook(ctx, other, hook.ID), port.ErrNotFound)
		_, err = store.ListWebhookDeliveries(ctx, other, hook.ID)
		require.ErrorIs(t, err, port.ErrNotFound)
		listed, err := store.ListWebhooks(ctx, other)
		require.NoError(t, err)
		require.Empty(t, listed)

		for range model.WebhookMaxSubscriptionsPerTenant - 1 {
			putHook(t, store, testTenantID)
		}
		_, err = store.PutWebhook(ctx, testTenantID, model.WebhookID(uuid.NewString()), model.WebhookSettings{Destination: "https://hooks.example.test", Events: []string{"*"}, EncryptedSecrets: testWebhookSecret, SecretCount: 1})
		require.ErrorIs(t, err, port.ErrWebhookCapacity)
		putHook(t, store, other)

		// Every write is audited, never with its secrets.
		var audits []xologorm.MutationAudit
		require.NoError(t, db.Where("resource = ?", "webhook_subscription").Find(&audits).Error)
		require.Len(t, audits, model.WebhookMaxSubscriptionsPerTenant+2)
		for _, audit := range audits {
			require.NotContains(t, audit.Before+audit.After, testWebhookSecret)
			require.NotEmpty(t, audit.RequestID)
		}
	})
}

func lastEvent(t *testing.T, db *gormpkg.DB) xologorm.ProvisioningEvent {
	t.Helper()
	var event xologorm.ProvisioningEvent
	require.NoError(t, db.Order("sequence DESC").Take(&event).Error)
	return event
}

func TestWebhookDurableQueue(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newSeededStore(t, db)
		ctx := t.Context()
		hook := putHook(t, store, testTenantID, "tenant.updated.v1")
		touchTenant(t, store, testTenantID)

		require.NoError(t, store.PrepareWebhooks(ctx, roomyWebhooks, 100))
		require.NoError(t, store.PrepareWebhooks(ctx, roomyWebhooks, 100))
		queued := hookDeliveries(t, db, hook)
		require.Len(t, queued, 1, "preparation is idempotent")
		last := lastEvent(t, db)
		var event model.CommonEvent
		require.NoError(t, json.Unmarshal([]byte(last.Payload), &event))
		require.Equal(t, event.ID, queued[0].EventID)
		require.Equal(t, last.Sequence, queued[0].Sequence)
		require.Equal(t, last.Payload, queued[0].Body)

		// The feed retention never removes a queued delivery.
		_, err := store.PurgeEvents(ctx, time.Now().Add(time.Hour))
		require.NoError(t, err)

		var wg sync.WaitGroup
		jobs := make(chan *model.WebhookJob, 4)
		for range 4 {
			wg.Go(func() {
				job, err := store.ClaimWebhook(context.Background())
				require.NoError(t, err)
				jobs <- job
			})
		}
		wg.Wait()
		close(jobs)
		var first *model.WebhookJob
		for job := range jobs {
			if job != nil {
				require.Nil(t, first, "an attempt is leased once")
				first = job
			}
		}
		require.NotNil(t, first)
		require.Equal(t, testTenantID, first.TenantID)
		require.Equal(t, hook.ID, first.SubscriptionID)
		require.Equal(t, testWebhookSecret, first.EncryptedSecrets)
		require.Equal(t, queued[0].Body, first.Body)
		require.Equal(t, 1, first.Attempts)

		// An expired lease is claimed again, by any replica.
		expireLeases(t, db)
		second, err := xologorm.NewStore(db).ClaimWebhook(ctx)
		require.NoError(t, err)
		require.NotNil(t, second)
		require.Equal(t, first.ID, second.ID)
		require.Equal(t, first.EventID, second.EventID)
		require.NotEqual(t, first.Lease, second.Lease)
		require.Equal(t, 2, second.Attempts)
		require.ErrorIs(t, store.FinishWebhook(ctx, first, model.WebhookResult{Success: true}), port.ErrWebhookLeaseLost)

		require.NoError(t, store.FinishWebhook(ctx, second, model.WebhookResult{Diagnostic: "free text from a receiver", StatusCode: 503}))
		row := hookDeliveries(t, db, hook)[0]
		require.Equal(t, model.WebhookPending, row.State)
		require.Equal(t, "delivery_failed", row.Diagnostic)
		require.Equal(t, 503, row.StatusCode)
		require.True(t, row.NextAttempt.After(time.Now().Add(5*time.Second)), "the retry backs off")
		none, err := store.ClaimWebhook(ctx)
		require.NoError(t, err)
		require.Nil(t, none, "a retry waits for its next attempt")

		expireLeases(t, db)
		third, err := store.ClaimWebhook(ctx)
		require.NoError(t, err)
		require.NoError(t, store.FinishWebhook(ctx, third, model.WebhookResult{Success: true, Diagnostic: "accepted", StatusCode: 204}))
		stats, err := store.WebhookStats(ctx)
		require.NoError(t, err)
		require.Equal(t, int64(1), stats.Delivered)
		require.Zero(t, stats.Pending+stats.InFlight)
		require.NotNil(t, hookDeliveries(t, db, hook)[0].FinishedAt)

		statuses, err := store.ListWebhookDeliveries(ctx, testTenantID, hook.ID)
		require.NoError(t, err)
		require.Len(t, statuses, 1)
		require.Equal(t, model.WebhookDelivered, statuses[0].State)
		require.Equal(t, 3, statuses[0].Attempts)
	})
}

func TestWebhookCapacityCountsOnlyQueued(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newSeededStore(t, db)
		ctx := t.Context()
		hook := putHook(t, store, testTenantID)
		tight := model.WebhookCapacity{Queue: 1, Subscription: 1}
		for range 3 {
			touchTenant(t, store, testTenantID)
			require.NoError(t, store.PrepareWebhooks(ctx, tight, 100))
			require.Equal(t, model.WebhookReady, hookRow(t, db, hook).State)
			job, err := store.ClaimWebhook(ctx)
			require.NoError(t, err)
			require.NoError(t, store.FinishWebhook(ctx, job, model.WebhookResult{Success: true, Diagnostic: "accepted"}))
		}
		require.Len(t, hookDeliveries(t, db, hook), 3, "finished deliveries never hold the queue")

		// A saturated subscription holds back only itself.
		other := newWebhookTenant(t, store, "other")
		peer := putHook(t, store, other)
		touchTenant(t, store, testTenantID)
		touchTenant(t, store, testTenantID)
		touchTenant(t, store, other)
		perSubscription := model.WebhookCapacity{Queue: 100, Subscription: 1}
		require.NoError(t, store.PrepareWebhooks(ctx, perSubscription, 100))
		require.Equal(t, model.WebhookBackpressure, hookRow(t, db, hook).State)
		require.Len(t, hookDeliveries(t, db, hook), 4)
		require.Equal(t, model.WebhookReady, hookRow(t, db, peer).State)
		require.Len(t, hookDeliveries(t, db, peer), 1)
		stats, err := store.WebhookStats(ctx)
		require.NoError(t, err)
		require.Equal(t, int64(1), stats.Backpressure)

		// Room again: the backlog resumes where it stopped.
		require.NoError(t, store.PrepareWebhooks(ctx, roomyWebhooks, 100))
		require.Equal(t, model.WebhookReady, hookRow(t, db, hook).State)
		require.Len(t, hookDeliveries(t, db, hook), 5)
	})
}

func TestWebhookPausesInactiveTenant(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newSeededStore(t, db)
		ctx := t.Context()
		hook := putHook(t, store, testTenantID)
		touchTenant(t, store, testTenantID)
		require.NoError(t, store.PrepareWebhooks(ctx, roomyWebhooks, 100))
		require.Len(t, hookDeliveries(t, db, hook), 1)

		touchTenant(t, store, testTenantID, model.WithTenantActive(false))
		position := hookRow(t, db, hook).Position
		require.NoError(t, store.PrepareWebhooks(ctx, roomyWebhooks, 100))
		require.Equal(t, position, hookRow(t, db, hook).Position, "a suspended tenant is paused")
		require.Len(t, hookDeliveries(t, db, hook), 1)
		job, err := store.ClaimWebhook(ctx)
		require.NoError(t, err)
		require.Nil(t, job, "a suspended tenant receives nothing")

		touchTenant(t, store, testTenantID, model.WithTenantActive(true))
		job, err = store.ClaimWebhook(ctx)
		require.NoError(t, err)
		require.NotNil(t, job)
		require.NoError(t, store.PrepareWebhooks(ctx, roomyWebhooks, 100))
		require.Len(t, hookDeliveries(t, db, hook), 3, "the suspension and the reactivation are delivered on resume")
	})
}

func TestWebhookHistoryLossIsScoped(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newSeededStore(t, db)
		ctx := t.Context()
		other := newWebhookTenant(t, store, "other")
		lagging := putHook(t, store, testTenantID)
		current := putHook(t, store, other)
		_, err := store.PutWebhook(ctx, testTenantID, lagging.ID, model.WebhookSettings{Destination: lagging.Destination, Events: lagging.Events, Enabled: false})
		require.NoError(t, err)
		touchTenant(t, store, testTenantID)
		touchTenant(t, store, other)
		require.NoError(t, store.PrepareWebhooks(ctx, roomyWebhooks, 100))
		require.Len(t, hookDeliveries(t, db, current), 1)

		// The retention passes the paused subscription.
		_, err = store.PurgeEvents(ctx, time.Now().Add(time.Hour))
		require.NoError(t, err)
		_, err = store.PutWebhook(ctx, testTenantID, lagging.ID, model.WebhookSettings{Destination: lagging.Destination, Events: lagging.Events, Enabled: true})
		require.NoError(t, err)
		position := hookRow(t, db, lagging).Position
		require.NoError(t, store.PrepareWebhooks(ctx, roomyWebhooks, 100))
		require.Equal(t, model.WebhookHistoryLost, hookRow(t, db, lagging).State)
		require.Equal(t, position, hookRow(t, db, lagging).Position)
		require.Empty(t, hookDeliveries(t, db, lagging))
		require.Equal(t, model.WebhookReady, hookRow(t, db, current).State, "a subscription the retention did not pass is untouched")

		// A reset acknowledges the loss and resumes at the end of the feed.
		touchTenant(t, store, testTenantID)
		require.NoError(t, store.ResetWebhook(ctx, testTenantID, lagging.ID))
		reset := hookRow(t, db, lagging)
		require.Equal(t, model.WebhookReady, reset.State)
		require.Greater(t, reset.Position, position)
		touchTenant(t, store, testTenantID)
		require.NoError(t, store.PrepareWebhooks(ctx, roomyWebhooks, 100))
		require.Len(t, hookDeliveries(t, db, lagging), 1, "only events after the reset are delivered")
	})
}

func TestWebhookPreparationIsFairAcrossTenants(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newSeededStore(t, db)
		ctx := t.Context()
		other := newWebhookTenant(t, store, "other")
		busy := putHook(t, store, testTenantID)
		quiet := putHook(t, store, other)
		for range 12 {
			touchTenant(t, store, testTenantID)
		}
		touchTenant(t, store, other)
		require.NoError(t, store.PrepareWebhooks(ctx, roomyWebhooks, 5))
		require.Len(t, hookDeliveries(t, db, busy), 5)
		require.Len(t, hookDeliveries(t, db, quiet), 1, "the events of another tenant never consume the budget")
		require.NoError(t, store.PrepareWebhooks(ctx, roomyWebhooks, 5))
		require.NoError(t, store.PrepareWebhooks(ctx, roomyWebhooks, 5))
		require.Len(t, hookDeliveries(t, db, busy), 12)
		require.Equal(t, hookRow(t, db, busy).Position, hookRow(t, db, quiet).Position)
	})
}

func TestWebhookPreparationRollsBack(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newSeededStore(t, db)
		ctx := t.Context()
		hook := putHook(t, store, testTenantID)
		touchTenant(t, store, testTenantID)
		before := hookRow(t, db, hook)

		// A concurrent write of the subscription wins: nothing is kept.
		require.NoError(t, db.Callback().Create().After("gorm:create").Register("test:webhook-concurrent-write", func(tx *gormpkg.DB) {
			if tx.Statement.Table == "webhook_deliveries" {
				_ = tx.Session(&gormpkg.Session{NewDB: true}).Exec("UPDATE webhook_subscriptions SET revision = revision + 1").Error
			}
		}))
		err := store.PrepareWebhooks(ctx, roomyWebhooks, 100)
		require.NoError(t, db.Callback().Create().Remove("test:webhook-concurrent-write"))
		require.NoError(t, err, "a lost race is left to the next round")
		require.Empty(t, hookDeliveries(t, db, hook))
		require.Equal(t, before, hookRow(t, db, hook))

		// A failed checkpoint keeps no delivery either.
		injected := errors.New("injected checkpoint failure")
		require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:webhook-checkpoint", func(tx *gormpkg.DB) {
			if tx.Statement.Table == "webhook_subscriptions" {
				_ = tx.AddError(injected)
			}
		}))
		err = store.PrepareWebhooks(ctx, roomyWebhooks, 100)
		require.NoError(t, db.Callback().Update().Remove("test:webhook-checkpoint"))
		require.ErrorIs(t, err, injected)
		require.Empty(t, hookDeliveries(t, db, hook))

		require.NoError(t, store.PrepareWebhooks(ctx, roomyWebhooks, 100))
		require.Len(t, hookDeliveries(t, db, hook), 1)

		// A failed result leaves the attempt to the lease expiry.
		job, err := store.ClaimWebhook(ctx)
		require.NoError(t, err)
		require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:webhook-result", func(tx *gormpkg.DB) {
			if tx.Statement.Table == "webhook_deliveries" {
				_ = tx.AddError(injected)
			}
		}))
		err = store.FinishWebhook(ctx, job, model.WebhookResult{Success: true})
		require.NoError(t, db.Callback().Update().Remove("test:webhook-result"))
		require.ErrorIs(t, err, injected)
		expireLeases(t, db)
		again, err := store.ClaimWebhook(ctx)
		require.NoError(t, err)
		require.Equal(t, job.ID, again.ID)
	})
}

// TestWebhookTakesNoFeedLock pins the absence of any instance lock: every
// webhook operation completes while a publisher holds the feed lock.
func TestWebhookTakesNoFeedLock(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		if db.Dialector.Name() != "postgres" {
			t.Skip("SQLite serializes every writer")
		}
		store := newSeededStore(t, db)
		org := model.NewOrganization(testTenantID, "locked", "Locked", "")
		require.NoError(t, store.CreateOrg(t.Context(), org))
		hook := putHook(t, store, testTenantID)
		touchTenant(t, store, testTenantID)

		holdFeedLock(t, db, org.ID())
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		putHook(t, store, testTenantID)
		require.NoError(t, store.PrepareWebhooks(ctx, roomyWebhooks, 100))
		require.Len(t, hookDeliveries(t, db, hook), 1)
		job, err := store.ClaimWebhook(ctx)
		require.NoError(t, err)
		require.NoError(t, store.FinishWebhook(ctx, job, model.WebhookResult{Success: true}))
		require.NoError(t, store.ResetWebhook(ctx, testTenantID, hook.ID))
		require.NoError(t, store.SweepWebhooks(ctx, time.Now()))
		_, err = store.WebhookStats(ctx)
		require.NoError(t, err)
		require.NoError(t, ctx.Err())
	})
}

func TestWebhookTenantDeletion(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newSeededStore(t, db)
		ctx := t.Context()
		doomed := newWebhookTenant(t, store, "doomed")
		hook := putHook(t, store, doomed)
		kept := putHook(t, store, testTenantID)
		touchTenant(t, store, doomed)
		touchTenant(t, store, testTenantID)
		require.NoError(t, store.PrepareWebhooks(ctx, roomyWebhooks, 100))

		require.NoError(t, store.DeleteTenant(ctx, doomed))
		var subscriptions, deliveries int64
		require.NoError(t, db.Model(&xologorm.WebhookSubscription{}).Where("tenant_id = ?", string(doomed)).Count(&subscriptions).Error)
		require.NoError(t, db.Model(&xologorm.WebhookDelivery{}).Where("tenant_id = ?", string(doomed)).Count(&deliveries).Error)
		require.Zero(t, subscriptions+deliveries)
		require.Empty(t, hookDeliveries(t, db, hook))
		require.Len(t, hookDeliveries(t, db, kept), 1)
	})
}

func TestWebhookSweep(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newSeededStore(t, db)
		ctx := t.Context()
		hook := putHook(t, store, testTenantID)
		now := time.Now().UTC()
		old := now.Add(-model.WebhookRetention - time.Hour)
		var rows []xologorm.WebhookDelivery
		for i := range 1005 {
			rows = append(rows, xologorm.WebhookDelivery{ID: uuid.NewString(), SubscriptionID: string(hook.ID), TenantID: string(testTenantID), EventID: uuid.NewString(), Sequence: int64(i), Body: "{}", State: model.WebhookDelivered, NextAttempt: old, LeaseUntil: old, EventAt: old, CreatedAt: old, FinishedAt: &old, Diagnostic: "accepted"})
		}
		expired := xologorm.WebhookDelivery{ID: uuid.NewString(), SubscriptionID: string(hook.ID), TenantID: string(testTenantID), EventID: uuid.NewString(), Body: "{}", State: model.WebhookPending, NextAttempt: now, LeaseUntil: now, EventAt: old, CreatedAt: now.Add(-model.WebhookMaxAge - time.Minute)}
		exhausted := xologorm.WebhookDelivery{ID: uuid.NewString(), SubscriptionID: string(hook.ID), TenantID: string(testTenantID), EventID: uuid.NewString(), Body: "{}", State: model.WebhookLeased, Attempts: model.WebhookMaxAttempts, NextAttempt: now, LeaseUntil: now.Add(-time.Second), EventAt: now, CreatedAt: now, Lease: "lost"}
		recent := xologorm.WebhookDelivery{ID: uuid.NewString(), SubscriptionID: string(hook.ID), TenantID: string(testTenantID), EventID: uuid.NewString(), Body: "{}", State: model.WebhookPending, NextAttempt: now, LeaseUntil: now, EventAt: now, CreatedAt: now}
		rows = append(rows, expired, exhausted, recent)
		require.NoError(t, db.CreateInBatches(rows, 200).Error)

		require.NoError(t, store.SweepWebhooks(ctx, now.Add(-model.WebhookRetention)))
		left := hookDeliveries(t, db, hook)
		require.Len(t, left, 3)
		states := map[string]string{}
		for _, row := range left {
			states[row.ID] = row.State + "/" + row.Diagnostic
		}
		require.Equal(t, "failed/retry_exhausted", states[expired.ID])
		require.Equal(t, "failed/retry_exhausted", states[exhausted.ID])
		require.Equal(t, "pending/", states[recent.ID])
	})
}

func TestWebhookMigration(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newSeededStore(t, db)
		ctx := t.Context()
		require.NoError(t, db.Migrator().DropTable(&xologorm.WebhookDelivery{}, &xologorm.WebhookSubscription{}))
		require.NoError(t, db.Exec("DROP INDEX idx_provisioning_events_tenant_sequence").Error)
		require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", "202610090001").Error)
		disabled := xologorm.NewStore(db, xologorm.WithAutoMigrate(false))
		require.Error(t, disabled.CheckSchema(ctx))

		require.NoError(t, xologorm.NewStore(db).Migrate(ctx))
		require.NoError(t, disabled.CheckSchema(ctx))
		require.True(t, db.Migrator().HasTable(&xologorm.WebhookSubscription{}))
		require.True(t, db.Migrator().HasTable(&xologorm.WebhookDelivery{}))
		require.True(t, db.Migrator().HasIndex(&xologorm.ProvisioningEvent{}, "idx_provisioning_events_tenant_sequence"))
		putHook(t, store, testTenantID)
	})
}

// TestWebhookPreparationIsolatesFailures pins that one unreadable row never
// holds back preparation: an unreadable event is skipped, and an unreadable
// subscription is left behind while the others are prepared.
func TestWebhookPreparationIsolatesFailures(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newSeededStore(t, db)
		ctx := t.Context()
		other := newWebhookTenant(t, store, "other")

		// The broken subscription comes first in the preparation order.
		broken := putHook(t, store, other)
		touchTenant(t, store, other)
		require.NoError(t, db.Model(&xologorm.WebhookSubscription{}).Where("id = ?", string(broken.ID)).Update("events", "{").Error)

		hook := putHook(t, store, testTenantID)
		touchTenant(t, store, testTenantID)
		corrupted := lastEvent(t, db)
		require.NoError(t, db.Model(&xologorm.ProvisioningEvent{}).Where("sequence = ?", corrupted.Sequence).Update("payload", "not json").Error)
		touchTenant(t, store, testTenantID)
		valid := lastEvent(t, db)

		stats, err := store.WebhookStats(ctx)
		require.NoError(t, err)
		require.Positive(t, stats.MaterializationLag, "events wait for preparation")

		require.NoError(t, store.PrepareWebhooks(ctx, roomyWebhooks, 100))
		deliveries := hookDeliveries(t, db, hook)
		require.Len(t, deliveries, 1)
		require.Equal(t, valid.Sequence, deliveries[0].Sequence, "the unreadable event is skipped")
		require.Equal(t, valid.Sequence, hookRow(t, db, hook).Position)
		require.Empty(t, hookDeliveries(t, db, broken))
		require.Equal(t, broken.Position, hookRow(t, db, broken).Position, "an unreadable subscription stays where it is")

		// Once repaired, the subscription catches up.
		require.NoError(t, db.Model(&xologorm.WebhookSubscription{}).Where("id = ?", string(broken.ID)).Update("events", `["*"]`).Error)
		require.NoError(t, store.PrepareWebhooks(ctx, roomyWebhooks, 100))
		require.Len(t, hookDeliveries(t, db, broken), 1)
		stats, err = store.WebhookStats(ctx)
		require.NoError(t, err)
		require.Zero(t, stats.MaterializationLag, "every subscription is prepared")
	})
}

// TestWebhookCapIsConcurrencySafe pins the per-tenant cap under concurrent
// creations: exactly one of them takes the last place.
func TestWebhookCapIsConcurrencySafe(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newSeededStore(t, db)
		for range model.WebhookMaxSubscriptionsPerTenant - 1 {
			putHook(t, store, testTenantID)
		}
		// Widen the window between the count and the insert, where a writer
		// that did not wait for the others would count a stale total.
		require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:webhook-cap-window", func(tx *gormpkg.DB) {
			if tx.Statement.Table == "webhook_subscriptions" {
				time.Sleep(200 * time.Millisecond)
			}
		}))
		t.Cleanup(func() { _ = db.Callback().Create().Remove("test:webhook-cap-window") })
		const writers = 8
		start := make(chan struct{})
		errs := make(chan error, writers)
		var wg sync.WaitGroup
		for range writers {
			wg.Go(func() {
				<-start
				_, err := store.PutWebhook(context.Background(), testTenantID, model.WebhookID(uuid.NewString()), model.WebhookSettings{Destination: "https://hooks.example.test/in", Events: []string{"*"}, Enabled: true, EncryptedSecrets: testWebhookSecret, SecretCount: 1})
				errs <- err
			})
		}
		close(start)
		wg.Wait()
		close(errs)
		created := 0
		for err := range errs {
			if err == nil {
				created++
				continue
			}
			require.ErrorIs(t, err, port.ErrWebhookCapacity)
		}
		require.Equal(t, 1, created)
		var count int64
		require.NoError(t, db.Model(&xologorm.WebhookSubscription{}).Where("tenant_id = ?", string(testTenantID)).Count(&count).Error)
		require.Equal(t, int64(model.WebhookMaxSubscriptionsPerTenant), count)
	})
}
