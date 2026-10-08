package gorm

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
)

// inventoryBatchSize bounds the projections held in memory by an export.
var inventoryBatchSize = 500

// inventoryFamilies orders the exported families, parents before children.
var inventoryFamilies = []string{
	model.FamilyTenant,
	model.FamilyTenantDomain,
	model.FamilyOrganization,
	model.FamilyMember,
	model.FamilyOrganizationMembership,
}

// ReadInventory implements port.InventoryReader. The cursor and every page
// come from one read-only snapshot, which takes no lock: the writes
// committed after the snapshot are the events after the cursor.
//
// The transaction is never replayed: its callbacks may already have written
// to a stream. A failure leaves an export without trailer.
func (s *Store) ReadInventory(ctx context.Context, start func(source, cursor string) error, item func(family string, item model.CommonItem) error) error {
	db, err := s.getDatabase(ctx)
	if err != nil {
		return errors.WithStack(err)
	}
	var opts *sql.TxOptions
	if isPostgres(db) {
		opts = &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	}
	read := func(db *gorm.DB) error {
		feed, err := provisioningFeed(db)
		if err != nil {
			return err
		}
		horizon, err := feedHorizon(db, feed)
		if err != nil {
			return err
		}
		cursor, err := encodeCursor(feed, provisioningCursor{Kind: cursorEvents, Position: horizon})
		if err != nil {
			return err
		}
		if err := start(feed.Source, cursor); err != nil {
			return err
		}
		for _, family := range inventoryFamilies {
			var last *ProvisioningProjection
			for {
				q := db.Where("family = ?", family)
				if last != nil {
					q = q.Where("(tenant_id, org_id, resource_key) > (?, ?, ?)", last.TenantID, last.OrgID, last.ResourceKey)
				}
				var rows []ProvisioningProjection
				if err := q.Order("tenant_id, org_id, resource_key").Limit(inventoryBatchSize).Find(&rows).Error; err != nil {
					return errors.WithStack(err)
				}
				for _, row := range rows {
					out, err := row.item()
					if err != nil {
						return errors.WithStack(err)
					}
					if err := item(family, out); err != nil {
						return err
					}
				}
				if len(rows) < inventoryBatchSize {
					break
				}
				last = &rows[len(rows)-1]
			}
		}
		return nil
	}
	if s.transactionBound {
		return read(db.WithContext(ctx))
	}
	return errors.WithStack(db.WithContext(ctx).Transaction(read, opts))
}

var _ port.InventoryReader = &Store{}

// DetachReport describes what DetachControlPlane removed.
type DetachReport struct {
	SubscriptionsRemoved int64 `json:"subscriptions_removed"`
	DeliveriesRemoved    int64 `json:"deliveries_removed"`
}

// ErrNoUsableAdmin refuses a detachment that would leave nobody able to
// administer the instance.
var ErrNoUsableAdmin = errors.Wrap(port.ErrNotAllowed, "no usable platform administrator: an active admin of an active tenant with a sign-in link is required")

// DetachControlPlane removes what makes the control plane drive the
// instance: its webhook subscriptions, their secrets and pending deliveries.
// It is an offline operator operation: every writer must be stopped, and a
// delivery already sent cannot be recalled. Resources, IDs, declared
// identities, sign-in links, audit and the event feed are left untouched;
// the instance then restarts with a local or shared ownership policy.
func (s *Store) DetachControlPlane(ctx context.Context) (DetachReport, error) {
	var report DetachReport
	actor := model.ActorFromContext(ctx)
	if actor.URI == "" || actor.UserID != "" {
		return report, errors.Wrap(port.ErrNotAllowed, "detaching requires an operator actor")
	}
	err := s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		report = DetachReport{}
		var admins int64
		if err := db.Table("users").
			Joins("JOIN user_roles ON user_roles.user_id = users.id").
			Joins("JOIN tenants ON tenants.id = users.tenant_id").
			Where("user_roles.role = ? AND users.active = ? AND tenants.active <> 0", model.PlatformRoleAdmin, true).
			Where("users.provider <> '' AND users.subject <> '' AND users.provider <> ?", model.ApplicationProvider).
			Count(&admins).Error; err != nil {
			return errors.WithStack(err)
		}
		if admins == 0 {
			return errors.WithStack(ErrNoUsableAdmin)
		}
		// No checkOwnership(FamilySubscription): detaching is precisely
		// taking the subscriptions away from the control plane that owns them.
		deliveries := db.Where("1 = 1").Delete(&WebhookDelivery{})
		if deliveries.Error != nil {
			return errors.WithStack(deliveries.Error)
		}
		subscriptions := db.Where("1 = 1").Delete(&WebhookSubscription{})
		if subscriptions.Error != nil {
			return errors.WithStack(subscriptions.Error)
		}
		report.DeliveriesRemoved, report.SubscriptionsRemoved = deliveries.RowsAffected, subscriptions.RowsAffected
		actorJSON, err := json.Marshal(actor)
		if err != nil {
			return errors.WithStack(err)
		}
		after, err := json.Marshal(map[string]any{"action": "detach", "subscriptions_removed": report.SubscriptionsRemoved, "deliveries_removed": report.DeliveriesRemoved})
		if err != nil {
			return errors.WithStack(err)
		}
		// No TenantID nor OrgID: the operation spans the whole instance.
		audit := MutationAudit{ID: uuid.NewString(), Actor: string(actorJSON), RequestID: actor.RequestID, Resource: "control_plane", ResourceID: "instance", Before: "null", After: string(after)}
		return errors.WithStack(db.Create(&audit).Error)
	})
	return report, err
}
