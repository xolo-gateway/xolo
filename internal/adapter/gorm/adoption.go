package gorm

import (
	"context"
	"encoding/json"
	"time"

	"github.com/xolo-gateway/xolo/internal/adoption"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

func (s *Store) ExportAdoption(ctx context.Context) ([]byte, error) {
	var out []byte
	err := s.identityTransaction(ctx, func(tx *Store) error {
		// Capture before reading records under the publication lock. This stronger
		// snapshot guarantees completeness even while other replicas are writing.
		cursor, err := tx.CaptureCommonCursor(ctx)
		if err != nil {
			return err
		}
		db, err := tx.getDatabase(ctx)
		if err != nil {
			return err
		}
		var feed CommonFeed
		if err := db.First(&feed, 1).Error; err != nil {
			return err
		}
		var rows []CommonRecord
		if err := db.Where("family IN ?", adoption.Families).Order("family, tenant_id, organization_id, key").Find(&rows).Error; err != nil {
			return err
		}
		payload := adoption.Payload{Source: feed.Source, Cursor: cursor, Records: []adoption.Record{}}
		for _, row := range rows {
			item, err := row.item()
			if err != nil {
				return err
			}
			payload.Records = append(payload.Records, adoption.Record{Family: row.Family, CommonItem: item})
		}
		out, err = adoption.Encode(payload)
		return err
	})
	return out, err
}

// DetachControlPlane is an offline operator operation. All writers must be
// stopped; a delivery already sent over the network cannot be recalled.
func (s *Store) DetachControlPlane(ctx context.Context) error {
	actor := model.ActorFromContext(ctx)
	if actor.URI == "" || actor.UserID != "" {
		return port.ErrNotAllowed
	}
	return s.identityTransaction(ctx, func(tx *Store) error {
		db, err := tx.getDatabase(ctx)
		if err != nil {
			return err
		}
		var admins int64
		if err := db.Table("users").Joins("JOIN user_roles ON user_roles.user_id = users.id").Joins("JOIN tenants ON tenants.id = users.tenant_id").Where("user_roles.role = ? AND users.active = ? AND tenants.active = ?", model.PlatformRoleAdmin, true, 1).Count(&admins).Error; err != nil {
			return err
		}
		if admins == 0 {
			return port.ErrNotAllowed
		}
		for _, table := range []any{&WebhookDelivery{}, &WebhookSubscription{}} {
			if err := db.Where("1 = 1").Delete(table).Error; err != nil {
				return err
			}
		}
		if err := db.Exec("UPDATE publication_clocks SET sequence = sequence + 1 WHERE id = 1").Error; err != nil {
			return err
		}
		var clock PublicationClock
		if err := db.First(&clock, 1).Error; err != nil {
			return err
		}
		encoded, err := json.Marshal(tx.mutations.actor)
		if err != nil {
			return err
		}
		return db.Create(&MutationAudit{Sequence: clock.Sequence, CreatedAt: time.Now().UTC(), Actor: string(encoded), RequestID: tx.mutations.actor.RequestID, Resource: "control_plane", ResourceID: "instance", Before: `{"attached":true}`, After: `{"attached":false}`}).Error
	})
}
