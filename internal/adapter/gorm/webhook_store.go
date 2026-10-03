package gorm

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type WebhookSubscription struct {
	ID               string `gorm:"primaryKey"`
	TenantID         string `gorm:"index;not null"`
	Destination      string
	Events           string `gorm:"type:text"`
	Enabled          bool
	State            string
	Position         int64
	EncryptedSecrets string `gorm:"type:text"`
	SecretCount      int
	UpdatedAt        time.Time
}
type WebhookDelivery struct {
	EventAt        time.Time
	ID             string `gorm:"primaryKey"`
	SubscriptionID string `gorm:"uniqueIndex:idx_webhook_event;index"`
	EventID        string `gorm:"uniqueIndex:idx_webhook_event"`
	TenantID       string `gorm:"index"`
	Sequence       int64
	Body           string `gorm:"type:text"`
	State          string `gorm:"index:idx_webhook_ready"`
	Attempts       int
	NextAttempt    time.Time `gorm:"index:idx_webhook_ready"`
	Lease          string
	LeaseUntil     time.Time
	CreatedAt      time.Time
	FinishedAt     *time.Time `gorm:"index"`
	Diagnostic     string
	StatusCode     int
}

func migrateWebhooks(db *gorm.DB) error {
	return db.AutoMigrate(&WebhookSubscription{}, &WebhookDelivery{})
}
func webhookView(row WebhookSubscription) (model.WebhookSubscription, error) {
	out := model.WebhookSubscription{ID: row.ID, TenantID: row.TenantID, Destination: row.Destination, Enabled: row.Enabled, State: row.State, Position: row.Position, SecretCount: row.SecretCount, Owner: "instance", UpdatedAt: row.UpdatedAt}
	err := json.Unmarshal([]byte(row.Events), &out.Events)
	return out, err
}
func webhookParent(db *gorm.DB, tid string) error {
	if _, err := model.ParseTenantID(tid); err != nil {
		return port.ErrInvalid
	}
	var count int64
	if err := db.Table("tenants").Where("id = ?", tid).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return port.ErrParentNotFound
	}
	return nil
}
func webhookRow(db *gorm.DB, tid, id string) (WebhookSubscription, error) {
	var row WebhookSubscription
	if err := webhookParent(db, tid); err != nil {
		return row, err
	}
	if _, err := model.ParseTenantID(id); err != nil {
		return row, port.ErrInvalid
	}
	err := db.Where("id = ? AND tenant_id = ?", id, tid).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = port.ErrNotFound
	}
	return row, err
}
func (s *Store) PutWebhook(ctx context.Context, tid, id string, p model.WebhookSettings) (model.WebhookSubscription, error) {
	if err := s.checkOwnership(ctx, "subscription"); err != nil {
		return model.WebhookSubscription{}, err
	}
	var result model.WebhookSubscription
	err := s.identityTransaction(ctx, func(tx *Store) error {
		db, err := tx.getDatabase(ctx)
		if err != nil {
			return err
		}
		if err := requireLive(db, model.CommonScope{Family: "tenant"}, tid); err != nil {
			return err
		}

		row, err := webhookRow(db, tid, id)
		if err != nil && !errors.Is(err, port.ErrNotFound) {
			return err
		}
		if errors.Is(err, port.ErrNotFound) {
			var count int64
			if err := db.Model(&WebhookSubscription{}).Count(&count).Error; err != nil {
				return err
			}
			if count >= model.WebhookMaxSubscriptions {
				return port.ErrWebhookCapacity
			}
			if p.EncryptedSecrets == "" {
				return port.ErrInvalid
			}
			var clock PublicationClock
			if err := db.First(&clock, 1).Error; err != nil {
				return err
			}
			row = WebhookSubscription{ID: id, TenantID: tid, Position: clock.Sequence, State: "ready"}
			// Global UUIDs cannot be reassigned across tenants.
			if err := db.Model(&WebhookSubscription{}).Where("id = ?", id).Count(&count).Error; err != nil {
				return err
			}
			if count != 0 {
				return port.ErrAlreadyExists
			}
		}
		encoded, err := json.Marshal(p.Events)
		if err != nil {
			return err
		}
		row.Destination = p.Destination
		row.Events = string(encoded)
		row.Enabled = p.Enabled
		if p.EncryptedSecrets != "" {
			row.EncryptedSecrets = p.EncryptedSecrets
			row.SecretCount = p.SecretCount
		}
		if err := db.Save(&row).Error; err != nil {
			return err
		}
		if err := tx.auditControlOperation(ctx, "subscription", id, "put"); err != nil {
			return err
		}
		result, err = webhookView(row)
		return err
	})
	return result, err
}
func (s *Store) GetWebhook(ctx context.Context, tid, id string) (model.WebhookSubscription, error) {
	db, err := s.getDatabase(ctx)
	if err != nil {
		return model.WebhookSubscription{}, err
	}
	row, err := webhookRow(db.WithContext(ctx), tid, id)
	if err != nil {
		return model.WebhookSubscription{}, err
	}
	return webhookView(row)
}
func (s *Store) ListWebhooks(ctx context.Context, tid string) ([]model.WebhookSubscription, error) {
	out := []model.WebhookSubscription{}
	db, err := s.getDatabase(ctx)
	if err != nil {
		return out, err
	}
	db = db.WithContext(ctx)
	if err := webhookParent(db, tid); err != nil {
		return out, err
	}
	var rows []WebhookSubscription
	if err := db.Where("tenant_id = ?", tid).Order("id").Find(&rows).Error; err != nil {
		return out, err
	}
	for _, row := range rows {
		item, err := webhookView(row)
		if err != nil {
			return out, err
		}
		out = append(out, item)
	}
	return out, nil
}
func (s *Store) DeleteWebhook(ctx context.Context, tid, id string) error {
	if err := s.checkOwnership(ctx, "subscription"); err != nil {
		return err
	}
	return s.identityTransaction(ctx, func(tx *Store) error {
		db, err := tx.getDatabase(ctx)
		if err != nil {
			return err
		}
		if err := requireLive(db, model.CommonScope{Family: "tenant"}, tid); err != nil {
			return err
		}

		if _, err := webhookRow(db, tid, id); err != nil {
			return err
		}
		if err := db.Where("subscription_id = ? AND tenant_id = ?", id, tid).Delete(&WebhookDelivery{}).Error; err != nil {
			return err
		}
		if err := db.Where("id = ? AND tenant_id = ?", id, tid).Delete(&WebhookSubscription{}).Error; err != nil {
			return err
		}
		return tx.auditControlOperation(ctx, "subscription", id, "delete")
	})
}
func (s *Store) ResetWebhook(ctx context.Context, tid, id string) error {
	if err := s.checkOwnership(ctx, "subscription"); err != nil {
		return err
	}
	return s.identityTransaction(ctx, func(tx *Store) error {
		db, err := tx.getDatabase(ctx)
		if err != nil {
			return err
		}
		if err := requireLive(db, model.CommonScope{Family: "tenant"}, tid); err != nil {
			return err
		}

		if _, err := webhookRow(db, tid, id); err != nil {
			return err
		}
		var clock PublicationClock
		if err := db.First(&clock, 1).Error; err != nil {
			return err
		}
		if err := db.Where("subscription_id = ? AND tenant_id = ?", id, tid).Delete(&WebhookDelivery{}).Error; err != nil {
			return err
		}
		if err := db.Model(&WebhookSubscription{}).Where("id = ? AND tenant_id = ?", id, tid).Updates(map[string]any{"position": clock.Sequence, "state": "ready"}).Error; err != nil {
			return err
		}
		return tx.auditControlOperation(ctx, "subscription", id, "reset")
	})
}
func (s *Store) PrepareWebhooks(ctx context.Context, capacity int) error {
	if capacity < 1 {
		return port.ErrInvalid
	}
	return s.identityTransaction(ctx, func(tx *Store) error {
		db, err := tx.getDatabase(ctx)
		if err != nil {
			return err
		}
		now := db.NowFunc().UTC()
		if err := db.Where("finished_at < ?", now.Add(-model.WebhookRetention)).Delete(&WebhookDelivery{}).Error; err != nil {
			return err
		}
		if err := db.Model(&WebhookDelivery{}).Where("(state = ? OR (state = ? AND lease_until <= ?)) AND (created_at <= ? OR attempts >= ?)", "pending", "leased", now, now.Add(-model.WebhookMaxAge), model.WebhookMaxAttempts).Updates(map[string]any{"state": "failed", "finished_at": now, "diagnostic": "retry_exhausted", "lease": ""}).Error; err != nil {
			return err
		}
		var size int64
		if err := db.Model(&WebhookDelivery{}).Count(&size).Error; err != nil {
			return err
		}
		var feed CommonFeed
		if err := db.First(&feed, 1).Error; err != nil {
			return err
		}
		var horizon PublicationClock
		if err := db.First(&horizon, 1).Error; err != nil {
			return err
		}
		var subscriptions []WebhookSubscription
		if err := db.Where("enabled = ? AND state <> ? AND position < ?", true, "history_lost", horizon.Sequence).Order("position, id").Find(&subscriptions).Error; err != nil {
			return err
		}
		// Bound the lock hold time across the whole instance, not per subscription.
		// Oldest positions go first so a busy subscription cannot starve its peers.
		budget := 100
		for _, sub := range subscriptions {
			if err := webhookParent(db, sub.TenantID); err != nil {
				return err
			}
			if sub.Position < feed.Floor {
				if err := db.Model(&WebhookSubscription{}).Where("id = ?", sub.ID).Update("state", "history_lost").Error; err != nil {
					return err
				}
				continue
			}
			if budget == 0 {
				continue
			}
			pageLimit := budget
			var types []string
			if err := json.Unmarshal([]byte(sub.Events), &types); err != nil {
				return err
			}
			selected := map[string]bool{}
			for _, typ := range types {
				selected[typ] = true
			}
			var rows []Publication
			if err := db.Where("sequence > ? AND sequence <= ?", sub.Position, horizon.Sequence).Order("sequence").Limit(pageLimit).Find(&rows).Error; err != nil {
				return err
			}
			budget -= len(rows)
			position := sub.Position
			full := false
			for _, row := range rows {
				if row.TenantID == sub.TenantID {
					var event model.CommonEvent
					if err := json.Unmarshal([]byte(row.Payload), &event); err != nil {
						return err
					}
					if event.Data.Key.TenantID != sub.TenantID || event.Source != feed.Source {
						return port.ErrNotAllowed
					}
					if selected["*"] || selected[event.Type] {
						if size >= int64(capacity) {
							full = true
							break
						}
						d := WebhookDelivery{ID: uuid.NewString(), SubscriptionID: sub.ID, TenantID: sub.TenantID, EventID: event.ID, Sequence: row.Sequence, Body: row.Payload, State: "pending", NextAttempt: now, CreatedAt: now, EventAt: event.Time}
						res := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&d)
						if res.Error != nil {
							return res.Error
						}
						size += res.RowsAffected
					}
				}
				position = row.Sequence
			}
			if !full && len(rows) < pageLimit {
				position = horizon.Sequence
			}
			state := "ready"
			if full {
				state = "backpressure"
			}
			if err := db.Model(&WebhookSubscription{}).Where("id = ?", sub.ID).Updates(map[string]any{"position": position, "state": state}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
func (s *Store) ClaimWebhook(ctx context.Context) (*model.WebhookJob, error) {
	var result *model.WebhookJob
	err := s.identityTransaction(ctx, func(tx *Store) error {
		result = nil
		db, err := tx.getDatabase(ctx)
		if err != nil {
			return err
		}
		now := db.NowFunc().UTC()
		var row WebhookDelivery
		err = db.Table("webhook_deliveries AS d").Select("d.*").Joins("JOIN webhook_subscriptions AS s ON s.id = d.subscription_id AND s.tenant_id = d.tenant_id").Joins("JOIN tenants AS t ON t.id = s.tenant_id").Where("s.enabled = ? AND ((d.state = ? AND d.next_attempt <= ?) OR (d.state = ? AND d.lease_until <= ?)) AND d.attempts < ? AND d.created_at > ?", true, "pending", now, "leased", now, model.WebhookMaxAttempts, now.Add(-model.WebhookMaxAge)).Order("d.next_attempt, d.id").Limit(1).Find(&row).Error
		if err == nil && row.ID == "" {
			return nil
		}
		if err != nil {
			return err
		}
		// Scope is resolved under the reservation lock before credentials are loaded.
		sub, err := webhookRow(db, row.TenantID, row.SubscriptionID)
		if err != nil {
			return err
		}
		lease := uuid.NewString()
		if err := db.Model(&WebhookDelivery{}).Where("id = ?", row.ID).Updates(map[string]any{"state": "leased", "lease": lease, "lease_until": now.Add(model.WebhookLease), "attempts": row.Attempts + 1}).Error; err != nil {
			return err
		}
		result = &model.WebhookJob{ID: row.ID, TenantID: row.TenantID, SubscriptionID: row.SubscriptionID, EventID: row.EventID, Body: row.Body, Destination: sub.Destination, EncryptedSecrets: sub.EncryptedSecrets, Lease: lease, Attempts: row.Attempts + 1, CreatedAt: row.CreatedAt}
		return nil
	})
	return result, err
}
func (s *Store) FinishWebhook(ctx context.Context, job *model.WebhookJob, result model.WebhookResult) error {
	return s.identityTransaction(ctx, func(tx *Store) error {
		db, err := tx.getDatabase(ctx)
		if err != nil {
			return err
		}
		now := db.NowFunc().UTC()
		state := "pending"
		var finished *time.Time
		if result.Success {
			state = "delivered"
			finished = &now
		} else if job.Attempts >= model.WebhookMaxAttempts || !now.Before(job.CreatedAt.Add(model.WebhookMaxAge)) {
			state = "failed"
			finished = &now
		}
		delay := min(5*time.Second<<min(job.Attempts-1, 10), time.Hour)
		diagnostic := result.Diagnostic
		switch diagnostic {
		case "accepted", "http_status", "redirect", "response_too_large", "transport_error", "credentials_unavailable", "destination_rejected":
		default:
			diagnostic = "delivery_failed"
		}
		res := db.Model(&WebhookDelivery{}).Where("id = ? AND tenant_id = ? AND subscription_id = ? AND state = ? AND lease = ?", job.ID, job.TenantID, job.SubscriptionID, "leased", job.Lease).Updates(map[string]any{"state": state, "lease": "", "next_attempt": now.Add(delay), "finished_at": finished, "diagnostic": diagnostic, "status_code": result.StatusCode})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return port.ErrWebhookLeaseLost
		}
		return nil
	})
}
func (s *Store) WebhookStats(ctx context.Context) (model.WebhookStats, error) {
	out := model.WebhookStats{}
	db, err := s.getDatabase(ctx)
	if err != nil {
		return out, err
	}
	db = db.WithContext(ctx)
	for _, x := range []struct {
		state string
		n     *int64
	}{{"pending", &out.Pending}, {"leased", &out.InFlight}, {"failed", &out.Failed}, {"delivered", &out.Delivered}} {
		if err := db.Model(&WebhookDelivery{}).Where("state = ?", x.state).Count(x.n).Error; err != nil {
			return out, err
		}
	}
	if err := db.Model(&WebhookSubscription{}).Where("state = ?", "history_lost").Count(&out.HistoryLost).Error; err != nil {
		return out, err
	}
	var first WebhookDelivery
	err = db.Where("state IN ?", []string{"pending", "leased"}).Order("event_at").Limit(1).Find(&first).Error
	if err == nil && first.ID != "" {
		out.DeliveryLag = max(0, db.NowFunc().Sub(first.EventAt).Seconds())
	} else if err != nil {
		return out, err
	}
	var oldest Publication
	err = db.Table("publications AS p").Select("p.*").Where("EXISTS (SELECT 1 FROM webhook_subscriptions s WHERE s.enabled = ? AND s.position < p.sequence AND s.tenant_id = p.tenant_id)", true).Order("p.created_at").Limit(1).Find(&oldest).Error
	if err == nil && oldest.Sequence > 0 {
		out.MaterializationLag = max(0, db.NowFunc().Sub(oldest.CreatedAt).Seconds())
	} else if err != nil {
		return out, err
	}
	return out, nil
}

var _ port.WebhookStore = (*Store)(nil)

func (s *Store) ListWebhookDeliveries(ctx context.Context, tid, id string) ([]model.WebhookDeliveryStatus, error) {
	out := []model.WebhookDeliveryStatus{}
	db, err := s.getDatabase(ctx)
	if err != nil {
		return out, err
	}
	db = db.WithContext(ctx)
	if _, err := webhookRow(db, tid, id); err != nil {
		return out, err
	}
	err = db.Model(&WebhookDelivery{}).Select("id, event_id, state, attempts, next_attempt, created_at, finished_at, diagnostic, status_code").Where("tenant_id = ? AND subscription_id = ?", tid, id).Order("created_at DESC, id").Limit(100).Scan(&out).Error
	return out, err
}
