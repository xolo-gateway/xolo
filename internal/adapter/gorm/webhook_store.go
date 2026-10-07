package gorm

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// WebhookSubscription delivers the events of one tenant to one destination.
// Position is the feed sequence up to which events were turned into
// deliveries. Revision changes with every write, so concurrent writers detect
// each other without any lock.
type WebhookSubscription struct {
	ID               string    `gorm:"primaryKey;autoIncrement:false"`
	TenantID         string    `gorm:"index;not null"`
	Destination      string    `gorm:"not null"`
	Events           string    `gorm:"type:text;not null"`
	Enabled          bool      `gorm:"not null"`
	State            string    `gorm:"not null"`
	Position         int64     `gorm:"not null"`
	Revision         int64     `gorm:"not null"`
	EncryptedSecrets string    `gorm:"type:text;not null"`
	SecretCount      int       `gorm:"not null"`
	UpdatedAt        time.Time `gorm:"autoUpdateTime:false;not null"`
}

// WebhookDelivery is one event to deliver to one subscription. Body is a copy
// of the event: the retention of the feed never removes a queued delivery.
type WebhookDelivery struct {
	ID             string     `gorm:"primaryKey;autoIncrement:false"`
	SubscriptionID string     `gorm:"not null;uniqueIndex:idx_webhook_deliveries_event,priority:1;index:idx_webhook_deliveries_subscription,priority:1"`
	EventID        string     `gorm:"not null;uniqueIndex:idx_webhook_deliveries_event,priority:2"`
	TenantID       string     `gorm:"not null;index"`
	Sequence       int64      `gorm:"not null"`
	Body           string     `gorm:"type:text;not null"`
	State          string     `gorm:"not null;index:idx_webhook_deliveries_due,priority:1;index:idx_webhook_deliveries_subscription,priority:2"`
	Attempts       int        `gorm:"not null"`
	NextAttempt    time.Time  `gorm:"not null;index:idx_webhook_deliveries_due,priority:2"`
	Lease          string     `gorm:"not null"`
	LeaseUntil     time.Time  `gorm:"not null"`
	EventAt        time.Time  `gorm:"not null"`
	CreatedAt      time.Time  `gorm:"autoCreateTime:false;not null"`
	FinishedAt     *time.Time `gorm:"index"`
	Diagnostic     string     `gorm:"not null"`
	StatusCode     int        `gorm:"not null"`
}

// webhookSweepBatch bounds the rows changed by one sweep transaction.
const webhookSweepBatch = 1000

// webhookDiagnostics allowlists the diagnostics a sender may report: anything
// else is stored as delivery_failed, so a sender never leaks free text into the
// delivery history. The store adds retry_exhausted itself.
var webhookDiagnostics = map[string]bool{
	"accepted": true, "http_status": true, "redirect": true, "transport_error": true,
	"credentials_unavailable": true, "destination_rejected": true,
}

// errWebhookStale aborts a preparation that lost the race against another
// write of the subscription. The next round starts again from the new state.
var errWebhookStale = errors.New("webhook subscription changed concurrently")

// errWebhookUnreadable aborts the preparation of a subscription whose stored
// settings cannot be read. It never stops the preparation of the others.
var errWebhookUnreadable = errors.New("webhook subscription unreadable")

func webhookView(row WebhookSubscription) (model.WebhookSubscription, error) {
	out := model.WebhookSubscription{ID: model.WebhookID(row.ID), TenantID: model.TenantID(row.TenantID), Destination: row.Destination, Enabled: row.Enabled, State: row.State, Position: row.Position, SecretCount: row.SecretCount, UpdatedAt: row.UpdatedAt}
	err := json.Unmarshal([]byte(row.Events), &out.Events)
	return out, errors.WithStack(err)
}

// webhookAudit is the audited representation of a subscription: never its
// secrets, only how many there are.
func webhookAudit(row *WebhookSubscription) (string, error) {
	if row == nil {
		return "null", nil
	}
	raw, err := json.Marshal(map[string]any{
		"tenant_id": row.TenantID, "destination": row.Destination, "events": json.RawMessage(row.Events),
		"enabled": row.Enabled, "secret_count": row.SecretCount, "state": row.State, "position": row.Position,
	})
	return string(raw), errors.WithStack(err)
}

func auditWebhook(ctx context.Context, db *gorm.DB, id model.WebhookID, tenant model.TenantID, before, after *WebhookSubscription) error {
	actor := model.ActorFromContext(ctx)
	actorJSON, err := json.Marshal(actor)
	if err != nil {
		return errors.WithStack(err)
	}
	beforeJSON, err := webhookAudit(before)
	if err != nil {
		return err
	}
	afterJSON, err := webhookAudit(after)
	if err != nil {
		return err
	}
	audit := MutationAudit{ID: uuid.NewString(), CreatedAt: db.NowFunc().UTC(), Actor: string(actorJSON), RequestID: actor.RequestID, TenantID: string(tenant), Resource: "webhook_subscription", ResourceID: string(id), Before: beforeJSON, After: afterJSON}
	return errors.WithStack(db.Create(&audit).Error)
}

// webhookTenant asserts that the tenant exists.
func webhookTenant(db *gorm.DB, tenant model.TenantID) error {
	var count int64
	if err := db.Model(&Tenant{}).Where("id = ?", string(tenant)).Count(&count).Error; err != nil {
		return errors.WithStack(err)
	}
	if count != 1 {
		return errors.WithStack(port.ErrParentNotFound)
	}
	return nil
}

// lockWebhookTenant asserts that the tenant exists and, on PostgreSQL, locks
// its row until commit: concurrent creations for one tenant then count its
// subscriptions one after the other, so the cap holds at READ COMMITTED.
// SQLite serializes writers already.
func lockWebhookTenant(db *gorm.DB, tenant model.TenantID) error {
	if !isPostgres(db) {
		return webhookTenant(db, tenant)
	}
	var ids []string
	if err := db.Model(&Tenant{}).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", string(tenant)).Pluck("id", &ids).Error; err != nil {
		return errors.WithStack(err)
	}
	if len(ids) != 1 {
		return errors.WithStack(port.ErrParentNotFound)
	}
	return nil
}

// webhookRow loads a subscription of tenant: a subscription of another tenant
// is reported missing.
func webhookRow(db *gorm.DB, tenant model.TenantID, id model.WebhookID) (WebhookSubscription, error) {
	var row WebhookSubscription
	if err := webhookTenant(db, tenant); err != nil {
		return row, err
	}
	err := db.Where("id = ? AND tenant_id = ?", string(id), string(tenant)).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, errors.WithStack(port.ErrNotFound)
	}
	return row, errors.WithStack(err)
}

// webhookHorizon is the end of the feed, where a new or reset subscription
// starts.
func webhookHorizon(db *gorm.DB) (ProvisioningFeed, int64, error) {
	feed, err := provisioningFeed(db)
	if err != nil {
		return feed, 0, err
	}
	horizon, err := feedHorizon(db, feed)
	return feed, horizon, err
}

// webhookTransaction runs fn in a short transaction, replayed on contention
// and when a preparation changed the subscription in between.
func (s *Store) webhookTransaction(ctx context.Context, fn func(db *gorm.DB) error) error {
	db, err := s.getDatabase(ctx)
	if err != nil {
		return errors.WithStack(err)
	}
	for attempt := 0; ; attempt++ {
		err := retryTransaction(ctx, func() error {
			return db.WithContext(ctx).Transaction(fn)
		})
		if !errors.Is(err, errWebhookStale) || attempt >= 10 {
			return err
		}
	}
}

// PutWebhook implements port.WebhookStore.
func (s *Store) PutWebhook(ctx context.Context, tenant model.TenantID, id model.WebhookID, settings model.WebhookSettings) (model.WebhookSubscription, error) {
	ctx = model.EnsureActor(ctx)
	events, err := json.Marshal(settings.Events)
	if err != nil {
		return model.WebhookSubscription{}, errors.WithStack(err)
	}
	var out model.WebhookSubscription
	err = s.webhookTransaction(ctx, func(db *gorm.DB) error {
		if err := lockWebhookTenant(db, tenant); err != nil {
			return err
		}
		var row WebhookSubscription
		var before *WebhookSubscription
		err := db.Where("id = ?", string(id)).Take(&row).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			var count int64
			if err := db.Model(&WebhookSubscription{}).Where("tenant_id = ?", string(tenant)).Count(&count).Error; err != nil {
				return errors.WithStack(err)
			}
			if count >= model.WebhookMaxSubscriptionsPerTenant {
				return errors.WithStack(port.ErrWebhookCapacity)
			}
			if settings.EncryptedSecrets == "" {
				return errors.WithStack(port.ErrInvalid)
			}
			_, horizon, err := webhookHorizon(db)
			if err != nil {
				return err
			}
			row = WebhookSubscription{ID: string(id), TenantID: string(tenant), State: model.WebhookReady, Position: horizon}
		case err != nil:
			return errors.WithStack(err)
		case row.TenantID != string(tenant):
			// Identifiers are instance-wide: one never moves to another tenant.
			return errors.WithStack(port.ErrAlreadyExists)
		default:
			previous := row
			before = &previous
		}
		row.Destination = settings.Destination
		row.Events = string(events)
		row.Enabled = settings.Enabled
		if settings.EncryptedSecrets != "" {
			row.EncryptedSecrets = settings.EncryptedSecrets
			row.SecretCount = settings.SecretCount
		}
		row.Revision++
		row.UpdatedAt = db.NowFunc().UTC()
		if before == nil {
			if err := db.Create(&row).Error; err != nil {
				return errors.WithStack(err)
			}
		} else {
			result := db.Select("*").Where("revision = ?", before.Revision).Updates(&row)
			if result.Error != nil {
				return errors.WithStack(result.Error)
			}
			if result.RowsAffected != 1 {
				return errors.WithStack(errWebhookStale)
			}
		}
		if err := auditWebhook(ctx, db, id, tenant, before, &row); err != nil {
			return err
		}
		out, err = webhookView(row)
		return err
	})
	return out, err
}

// GetWebhook implements port.WebhookStore.
func (s *Store) GetWebhook(ctx context.Context, tenant model.TenantID, id model.WebhookID) (model.WebhookSubscription, error) {
	var out model.WebhookSubscription
	err := s.readTransaction(ctx, func(db *gorm.DB) error {
		row, err := webhookRow(db, tenant, id)
		if err != nil {
			return err
		}
		out, err = webhookView(row)
		return err
	})
	return out, err
}

// ListWebhooks implements port.WebhookStore.
func (s *Store) ListWebhooks(ctx context.Context, tenant model.TenantID) ([]model.WebhookSubscription, error) {
	out := []model.WebhookSubscription{}
	err := s.readTransaction(ctx, func(db *gorm.DB) error {
		out = []model.WebhookSubscription{}
		if err := webhookTenant(db, tenant); err != nil {
			return err
		}
		var rows []WebhookSubscription
		if err := db.Where("tenant_id = ?", string(tenant)).Order("id").Find(&rows).Error; err != nil {
			return errors.WithStack(err)
		}
		for _, row := range rows {
			item, err := webhookView(row)
			if err != nil {
				return err
			}
			out = append(out, item)
		}
		return nil
	})
	return out, err
}

// DeleteWebhook implements port.WebhookStore.
func (s *Store) DeleteWebhook(ctx context.Context, tenant model.TenantID, id model.WebhookID) error {
	ctx = model.EnsureActor(ctx)
	return s.webhookTransaction(ctx, func(db *gorm.DB) error {
		row, err := webhookRow(db, tenant, id)
		if err != nil {
			return err
		}
		if err := db.Where("subscription_id = ? AND tenant_id = ?", string(id), string(tenant)).Delete(&WebhookDelivery{}).Error; err != nil {
			return errors.WithStack(err)
		}
		if err := db.Where("id = ? AND tenant_id = ?", string(id), string(tenant)).Delete(&WebhookSubscription{}).Error; err != nil {
			return errors.WithStack(err)
		}
		return auditWebhook(ctx, db, id, tenant, &row, nil)
	})
}

// ResetWebhook implements port.WebhookStore.
func (s *Store) ResetWebhook(ctx context.Context, tenant model.TenantID, id model.WebhookID) error {
	ctx = model.EnsureActor(ctx)
	return s.webhookTransaction(ctx, func(db *gorm.DB) error {
		row, err := webhookRow(db, tenant, id)
		if err != nil {
			return err
		}
		_, horizon, err := webhookHorizon(db)
		if err != nil {
			return err
		}
		if err := db.Where("subscription_id = ? AND tenant_id = ?", string(id), string(tenant)).Delete(&WebhookDelivery{}).Error; err != nil {
			return errors.WithStack(err)
		}
		after := row
		after.Position, after.State, after.Revision, after.UpdatedAt = horizon, model.WebhookReady, row.Revision+1, db.NowFunc().UTC()
		result := db.Model(&WebhookSubscription{}).Where("id = ? AND revision = ?", row.ID, row.Revision).
			Updates(map[string]any{"position": after.Position, "state": after.State, "revision": after.Revision, "updated_at": after.UpdatedAt})
		if result.Error != nil {
			return errors.WithStack(result.Error)
		}
		if result.RowsAffected != 1 {
			return errors.WithStack(errWebhookStale)
		}
		return auditWebhook(ctx, db, id, tenant, &row, &after)
	})
}

// ListWebhookDeliveries implements port.WebhookStore.
func (s *Store) ListWebhookDeliveries(ctx context.Context, tenant model.TenantID, id model.WebhookID) ([]model.WebhookDeliveryStatus, error) {
	out := []model.WebhookDeliveryStatus{}
	err := s.readTransaction(ctx, func(db *gorm.DB) error {
		out = []model.WebhookDeliveryStatus{}
		if _, err := webhookRow(db, tenant, id); err != nil {
			return err
		}
		return errors.WithStack(db.Model(&WebhookDelivery{}).
			Select("id, event_id, sequence, state, attempts, next_attempt, created_at, finished_at, diagnostic, status_code").
			Where("tenant_id = ? AND subscription_id = ?", string(tenant), string(id)).
			Order("created_at DESC, id").Limit(100).Scan(&out).Error)
	})
	return out, err
}

// PrepareWebhooks implements port.WebhookStore. It reads the feed without the
// publication lock: the feed is prefix-closed, so no transaction still in
// flight can commit an event at or below the horizon read first. Each
// subscription is prepared in its own short transaction, and reads only the
// events of its tenant.
func (s *Store) PrepareWebhooks(ctx context.Context, capacity model.WebhookCapacity, budget int) error {
	if capacity.Queue < 1 || capacity.Subscription < 1 || budget < 1 {
		return errors.WithStack(port.ErrInvalid)
	}
	db, err := s.getDatabase(ctx)
	if err != nil {
		return errors.WithStack(err)
	}
	db = db.WithContext(ctx)
	_, horizon, err := webhookHorizon(db)
	if err != nil {
		return err
	}
	// A suspended tenant is paused: its position stays where it is.
	var subscriptions []WebhookSubscription
	if err := db.Table("webhook_subscriptions AS s").Select("s.*").
		Joins("JOIN tenants AS t ON t.id = s.tenant_id").
		Where("s.enabled = ? AND s.state <> ? AND s.position < ? AND t.active <> 0", true, model.WebhookHistoryLost, horizon).
		Order("s.position, s.id").Find(&subscriptions).Error; err != nil {
		return errors.WithStack(err)
	}
	// A subscription that cannot be prepared is logged and left behind: it
	// never holds back the others. Database and context errors stop the round.
	for _, subscription := range subscriptions {
		err := retryTransaction(ctx, func() error {
			return db.Transaction(func(tx *gorm.DB) error {
				return prepareWebhook(tx, subscription, horizon, capacity, budget)
			})
		})
		switch {
		case err == nil, errors.Is(err, errWebhookStale):
		case errors.Is(err, errWebhookUnreadable):
			slog.ErrorContext(ctx, "webhook subscription left unprepared", slog.String("subscription_id", subscription.ID), slog.Any("error", err))
		default:
			return err
		}
	}
	return nil
}

func prepareWebhook(db *gorm.DB, subscription WebhookSubscription, horizon int64, capacity model.WebhookCapacity, budget int) error {
	var types []string
	if err := json.Unmarshal([]byte(subscription.Events), &types); err != nil {
		return errors.Wrapf(errWebhookUnreadable, "events: %v", err)
	}
	selected := map[string]bool{}
	for _, typ := range types {
		selected[typ] = true
	}
	var rows []ProvisioningEvent
	if err := db.Where("tenant_id = ? AND sequence > ? AND sequence <= ?", subscription.TenantID, subscription.Position, horizon).
		Order("sequence ASC").Limit(budget).Find(&rows).Error; err != nil {
		return errors.WithStack(err)
	}
	// The floor is read after the events: a purge committed before that read
	// is detected here, so a purged event is never silently skipped.
	feed, err := provisioningFeed(db)
	if err != nil {
		return err
	}
	if subscription.Position < feed.Floor {
		return advanceWebhook(db, subscription, subscription.Position, model.WebhookHistoryLost)
	}
	var queued, own int64
	active := []string{model.WebhookPending, model.WebhookLeased}
	if err := db.Model(&WebhookDelivery{}).Where("state IN ?", active).Count(&queued).Error; err != nil {
		return errors.WithStack(err)
	}
	if err := db.Model(&WebhookDelivery{}).Where("subscription_id = ? AND state IN ?", subscription.ID, active).Count(&own).Error; err != nil {
		return errors.WithStack(err)
	}
	now := db.NowFunc().UTC()
	position, full := subscription.Position, false
	for _, row := range rows {
		var event model.CommonEvent
		if err := json.Unmarshal([]byte(row.Payload), &event); err != nil || event.ID == "" {
			// An unreadable event cannot be delivered: it is skipped so the
			// rest of the subscription's deliveries still commit.
			slog.ErrorContext(db.Statement.Context, "webhook event skipped: unreadable payload", slog.String("subscription_id", subscription.ID), slog.Int64("sequence", row.Sequence), slog.Any("error", err))
			position = row.Sequence
			continue
		}
		if selected["*"] || selected[event.Type] {
			if queued >= int64(capacity.Queue) || own >= int64(capacity.Subscription) {
				full = true
				break
			}
			delivery := WebhookDelivery{ID: uuid.NewString(), SubscriptionID: subscription.ID, TenantID: subscription.TenantID, EventID: event.ID, Sequence: row.Sequence, Body: row.Payload, State: model.WebhookPending, NextAttempt: now, LeaseUntil: now, EventAt: row.CreatedAt, CreatedAt: now}
			result := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&delivery)
			if result.Error != nil {
				return errors.WithStack(result.Error)
			}
			queued += result.RowsAffected
			own += result.RowsAffected
		}
		position = row.Sequence
	}
	if !full && len(rows) < budget {
		// Nothing else of this tenant up to the horizon.
		position = horizon
	}
	state := model.WebhookReady
	if full {
		state = model.WebhookBackpressure
	}
	return advanceWebhook(db, subscription, position, state)
}

// advanceWebhook checkpoints a preparation, unless the subscription was
// written since it was read: the deliveries inserted are then rolled back.
func advanceWebhook(db *gorm.DB, subscription WebhookSubscription, position int64, state string) error {
	result := db.Model(&WebhookSubscription{}).Where("id = ? AND revision = ?", subscription.ID, subscription.Revision).
		Updates(map[string]any{"position": position, "state": state, "revision": subscription.Revision + 1})
	if result.Error != nil {
		return errors.WithStack(result.Error)
	}
	if result.RowsAffected != 1 {
		return errors.WithStack(errWebhookStale)
	}
	return nil
}

// webhookClaimBatch is the number of candidates read per claim attempt.
const webhookClaimBatch = 8

// ClaimWebhook implements port.WebhookStore. A candidate is leased by a
// conditional update on its current state: concurrent workers, on any
// replica, never lease the same attempt twice and never wait for each other.
func (s *Store) ClaimWebhook(ctx context.Context) (*model.WebhookJob, error) {
	db, err := s.getDatabase(ctx)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	db = db.WithContext(ctx)
	now := db.NowFunc().UTC()
	var candidates []WebhookDelivery
	if err := db.Table("webhook_deliveries AS d").Select("d.id, d.state, d.lease, d.attempts").
		Joins("JOIN webhook_subscriptions AS s ON s.id = d.subscription_id AND s.tenant_id = d.tenant_id").
		Joins("JOIN tenants AS t ON t.id = d.tenant_id").
		Where("s.enabled = ? AND t.active <> 0", true).
		Where("(d.state = ? AND d.next_attempt <= ?) OR (d.state = ? AND d.lease_until <= ?)", model.WebhookPending, now, model.WebhookLeased, now).
		Where("d.attempts < ? AND d.created_at > ?", model.WebhookMaxAttempts, now.Add(-model.WebhookMaxAge)).
		Order("d.next_attempt, d.id").Limit(webhookClaimBatch).Find(&candidates).Error; err != nil {
		return nil, errors.WithStack(err)
	}
	for _, candidate := range candidates {
		lease := uuid.NewString()
		var result *gorm.DB
		err := retryTransaction(ctx, func() error {
			result = db.Model(&WebhookDelivery{}).
				Where("id = ? AND state = ? AND lease = ? AND attempts = ?", candidate.ID, candidate.State, candidate.Lease, candidate.Attempts).
				Updates(map[string]any{"state": model.WebhookLeased, "lease": lease, "lease_until": now.Add(model.WebhookLease), "attempts": candidate.Attempts + 1})
			return result.Error
		})
		if err != nil {
			return nil, errors.WithStack(err)
		}
		if result.RowsAffected != 1 {
			continue
		}
		var job struct {
			WebhookDelivery
			Destination      string
			EncryptedSecrets string
		}
		if err := db.Table("webhook_deliveries AS d").Select("d.*, s.destination, s.encrypted_secrets").
			Joins("JOIN webhook_subscriptions AS s ON s.id = d.subscription_id AND s.tenant_id = d.tenant_id").
			Where("d.id = ? AND d.lease = ?", candidate.ID, lease).Limit(1).Scan(&job).Error; err != nil {
			return nil, errors.WithStack(err)
		}
		if job.ID == "" {
			// Reset or deleted right after the lease.
			continue
		}
		return &model.WebhookJob{ID: job.ID, SubscriptionID: model.WebhookID(job.SubscriptionID), TenantID: model.TenantID(job.TenantID), EventID: job.EventID, Body: job.Body, Destination: job.Destination, EncryptedSecrets: job.EncryptedSecrets, Lease: lease, Attempts: job.Attempts, CreatedAt: job.CreatedAt}, nil
	}
	return nil, nil
}

// FinishWebhook implements port.WebhookStore.
func (s *Store) FinishWebhook(ctx context.Context, job *model.WebhookJob, result model.WebhookResult) error {
	db, err := s.getDatabase(ctx)
	if err != nil {
		return errors.WithStack(err)
	}
	db = db.WithContext(ctx)
	now := db.NowFunc().UTC()
	state := model.WebhookPending
	var finished *time.Time
	switch {
	case result.Success:
		state, finished = model.WebhookDelivered, &now
	case job.Attempts >= model.WebhookMaxAttempts || !now.Before(job.CreatedAt.Add(model.WebhookMaxAge)):
		state, finished = model.WebhookFailed, &now
	}
	diagnostic := result.Diagnostic
	if !webhookDiagnostics[diagnostic] {
		diagnostic = "delivery_failed"
	}
	delay := min(5*time.Second<<min(max(job.Attempts-1, 0), 10), time.Hour)
	var update *gorm.DB
	err = retryTransaction(ctx, func() error {
		update = db.Model(&WebhookDelivery{}).
			Where("id = ? AND tenant_id = ? AND subscription_id = ? AND state = ? AND lease = ?", job.ID, string(job.TenantID), string(job.SubscriptionID), model.WebhookLeased, job.Lease).
			Updates(map[string]any{"state": state, "lease": "", "next_attempt": now.Add(delay), "finished_at": finished, "diagnostic": diagnostic, "status_code": result.StatusCode})
		return update.Error
	})
	if err != nil {
		return errors.WithStack(err)
	}
	if update.RowsAffected != 1 {
		return errors.WithStack(port.ErrWebhookLeaseLost)
	}
	return nil
}

// SweepWebhooks implements port.WebhookStore, by bounded batches each in its
// own statement.
//
// A paused delivery — disabled subscription or suspended tenant — is never
// claimed, whether pending or with an expired lease, and is only closed here
// once out of attempts or older than WebhookMaxAge. That bound is intended: a
// pause keeps the deliveries queued, and the capacity they hold stays within
// the share of their subscription.
func (s *Store) SweepWebhooks(ctx context.Context, finishedBefore time.Time) error {
	db, err := s.getDatabase(ctx)
	if err != nil {
		return errors.WithStack(err)
	}
	db = db.WithContext(ctx)
	now := db.NowFunc().UTC()
	exhausted := func(db *gorm.DB) *gorm.DB {
		return db.Model(&WebhookDelivery{}).Select("id").
			Where("state = ? OR (state = ? AND lease_until <= ?)", model.WebhookPending, model.WebhookLeased, now).
			Where("created_at <= ? OR attempts >= ?", now.Add(-model.WebhookMaxAge), model.WebhookMaxAttempts).
			Limit(webhookSweepBatch)
	}
	finished := func(db *gorm.DB) *gorm.DB {
		return db.Model(&WebhookDelivery{}).Select("id").Where("finished_at < ?", finishedBefore.UTC()).Limit(webhookSweepBatch)
	}
	steps := []func() *gorm.DB{
		func() *gorm.DB {
			return db.Model(&WebhookDelivery{}).Where("id IN (?)", exhausted(db)).
				Updates(map[string]any{"state": model.WebhookFailed, "finished_at": now, "diagnostic": "retry_exhausted", "lease": ""})
		},
		func() *gorm.DB { return db.Where("id IN (?)", finished(db)).Delete(&WebhookDelivery{}) },
	}
	for _, step := range steps {
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			var affected int64
			err := retryTransaction(ctx, func() error {
				result := step()
				affected = result.RowsAffected
				return result.Error
			})
			if err != nil {
				return errors.WithStack(err)
			}
			if affected < webhookSweepBatch {
				break
			}
		}
	}
	return nil
}

// WebhookStats implements port.WebhookStore.
func (s *Store) WebhookStats(ctx context.Context) (model.WebhookStats, error) {
	var out model.WebhookStats
	db, err := s.getDatabase(ctx)
	if err != nil {
		return out, errors.WithStack(err)
	}
	db = db.WithContext(ctx)
	now := db.NowFunc().UTC()
	var states []struct {
		State string
		Count int64
	}
	if err := db.Model(&WebhookDelivery{}).Select("state, COUNT(*) AS count").Group("state").Find(&states).Error; err != nil {
		return out, errors.WithStack(err)
	}
	for _, row := range states {
		switch row.State {
		case model.WebhookPending:
			out.Pending = row.Count
		case model.WebhookLeased:
			out.InFlight = row.Count
		case model.WebhookFailed:
			out.Failed = row.Count
		case model.WebhookDelivered:
			out.Delivered = row.Count
		}
	}
	for state, count := range map[string]*int64{model.WebhookHistoryLost: &out.HistoryLost, model.WebhookBackpressure: &out.Backpressure} {
		if err := db.Model(&WebhookSubscription{}).Where("state = ?", state).Count(count).Error; err != nil {
			return out, errors.WithStack(err)
		}
	}
	var oldest []WebhookDelivery
	if err := db.Select("event_at").Where("state IN ?", []string{model.WebhookPending, model.WebhookLeased}).Order("event_at").Limit(1).Find(&oldest).Error; err != nil {
		return out, errors.WithStack(err)
	}
	if len(oldest) == 1 {
		out.DeliveryLag = max(0, now.Sub(oldest[0].EventAt))
	}
	// The oldest event not yet prepared is the lowest first pending sequence
	// across the active subscriptions: one index seek per subscription, in a
	// single statement, then one lookup by primary key.
	var first sql.NullInt64
	if err := db.Raw(`SELECT MIN(pending.sequence) FROM (
		SELECT (SELECT MIN(e.sequence) FROM provisioning_events e WHERE e.tenant_id = s.tenant_id AND e.sequence > s.position) AS sequence
		FROM webhook_subscriptions s JOIN tenants t ON t.id = s.tenant_id
		WHERE s.enabled = ? AND s.state <> ? AND t.active <> 0) AS pending`, true, model.WebhookHistoryLost).Row().Scan(&first); err != nil {
		return out, errors.WithStack(err)
	}
	if first.Valid {
		var next []ProvisioningEvent
		if err := db.Select("created_at").Where("sequence = ?", first.Int64).Limit(1).Find(&next).Error; err != nil {
			return out, errors.WithStack(err)
		}
		if len(next) == 1 {
			out.MaterializationLag = max(0, now.Sub(next[0].CreatedAt))
		}
	}
	return out, nil
}

var _ port.WebhookStore = (*Store)(nil)
