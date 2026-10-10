package gorm

import (
	"context"
	stderrors "errors"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
)

// defaultPurgeBatch bounds the rows one purge transaction removes when the
// store is not configured otherwise.
const defaultPurgeBatch = 1000

// erasedActor replaces, in the audits, the actor whose member was purged.
const erasedActor = `{"uri":"urn:xolo:actor:erased"}`

// WithPurgeBatch bounds the rows one purge transaction removes.
func WithPurgeBatch(rows int) StoreOption {
	return func(opts *storeOptions) {
		if rows > 0 {
			opts.purgeBatch = rows
		}
	}
}

// DueDeletions implements port.LifecycleStore. The deletions held by a
// deleted tenant are left out: the purge of the tenant takes them along.
func (s *Store) DueDeletions(ctx context.Context, limit int) ([]model.Deletion, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.WithStack(port.ErrInvalid)
	}
	var rows []ResourceDeletion
	err := s.readTransaction(ctx, func(db *gorm.DB) error {
		return errors.WithStack(db.Where("confirmed_at IS NOT NULL AND purged_at IS NULL AND purge_after <= ?", db.NowFunc().UTC()).
			Where("family = ? OR NOT EXISTS (SELECT 1 FROM resource_deletions t WHERE t.family = ? AND t.resource_id = resource_deletions.tenant_id)", model.FamilyTenant, model.FamilyTenant).
			Order("attempts, purge_after, family, resource_id").Limit(limit).Find(&rows).Error)
	})
	if err != nil {
		return nil, err
	}
	out := make([]model.Deletion, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.view())
	}
	return out, nil
}

// PurgeDeletion implements port.LifecycleStore. It runs in short
// transactions, each taking the row of the deletion FOR UPDATE, so that two
// replicas never purge the same deletion at once, and no other lock than
// those of the rows it removes:
//
//  1. the projections of the scope, their events and the deliveries queued
//     for them are removed, and the deletion of the resource is published;
//     a tenant also retires the identifiers of its organizations and members;
//  2. the rows of the inventory are removed, table by table, children
//     first, in batches;
//  3. the audits of the scope are removed, those of the operations of a
//     purged member anonymized, and the deletion is marked purged.
//
// Every step is idempotent: an interrupted purge resumes where it stopped.
// The feed floor never moves: the events removed leave a gap no consumer
// needs, and the subscriptions of the other tenants keep their history.
func (s *Store) PurgeDeletion(ctx context.Context, deletion model.Deletion) error {
	target := ResourceDeletion{Family: deletion.Family, ResourceID: deletion.ResourceID}
	err := s.purge(ctx, target)
	if err == nil || errors.Is(err, port.ErrPurgeNotReady) || errors.Is(err, port.ErrNotFound) || ctx.Err() != nil {
		return err
	}
	// The failure is recorded apart from the purge, which rolled back.
	failed := s.purgeStep(ctx, target, false, func(db *gorm.DB, _ ResourceDeletion) error {
		return errors.WithStack(db.Model(&ResourceDeletion{}).Where("family = ? AND resource_id = ?", target.Family, target.ResourceID).
			Updates(map[string]any{"attempts": gorm.Expr("attempts + 1"), "diagnostic": model.DeletionPurgeFailed}).Error)
	})
	return stderrors.Join(err, failed)
}

func (s *Store) purge(ctx context.Context, target ResourceDeletion) error {
	if err := s.purgeStep(ctx, target, true, retireProjections); err != nil {
		return err
	}
	batch := s.purgeBatch
	if batch <= 0 {
		batch = defaultPurgeBatch
	}
	for _, table := range purgeOrder {
		for {
			var removed int64
			err := s.purgeStep(ctx, target, true, func(db *gorm.DB, d ResourceDeletion) error {
				var err error
				removed, err = purgeRows(db, lifecycleTableOf(table), d, batch)
				return err
			})
			if err != nil {
				return err
			}
			if removed < int64(batch) {
				break
			}
		}
	}
	return s.purgeStep(ctx, target, true, func(db *gorm.DB, d ResourceDeletion) error {
		return finishPurge(ctx, db, d)
	})
}

// errPurged stops a purge step on a deletion already purged.
var errPurged = errors.New("deletion already purged")

// purgeStep runs fn in one transaction holding the row of the deletion, once
// the deletion is checked due. Purging, it lets fn write the frozen rows of
// the tenant: the guards skip the deletions this transaction marks purging,
// and the mark is cleared before commit, so no other transaction sees it.
// A deletion already purged ends the purge without error.
func (s *Store) purgeStep(ctx context.Context, target ResourceDeletion, purging bool, fn func(*gorm.DB, ResourceDeletion) error) error {
	db, err := s.getDatabase(ctx)
	if err != nil {
		return errors.WithStack(err)
	}
	err = retryTransaction(ctx, func() error {
		return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if isPostgres(tx) {
				// Purging marks every deletion of the tenant: their rows are
				// locked in one order, so that two purges of a tenant queue
				// instead of deadlocking.
				var locked []string
				if err := tx.Raw("SELECT resource_id FROM resource_deletions WHERE tenant_id = (SELECT tenant_id FROM resource_deletions WHERE family = ? AND resource_id = ?)"+
					" ORDER BY family, resource_id FOR UPDATE", target.Family, target.ResourceID).Scan(&locked).Error; err != nil {
					return errors.WithStack(err)
				}
			}
			var rows []ResourceDeletion
			if err := tx.Where("family = ? AND resource_id = ?", target.Family, target.ResourceID).Limit(1).Find(&rows).Error; err != nil {
				return errors.WithStack(err)
			}
			if len(rows) == 0 {
				return errors.WithStack(port.ErrNotFound)
			}
			d := rows[0]
			if !purging {
				return fn(tx, d)
			}
			if d.PurgedAt != nil {
				return errPurged
			}
			if err := purgeReady(tx, d); err != nil {
				return err
			}
			mark := func(on bool) error {
				return errors.WithStack(tx.Model(&ResourceDeletion{}).Where("tenant_id = ?", d.TenantID).Update("purging", on).Error)
			}
			if err := mark(true); err != nil {
				return err
			}
			if err := fn(tx, d); err != nil {
				return err
			}
			return mark(false)
		})
	})
	if errors.Is(err, errPurged) {
		return nil
	}
	return err
}

// purgeReady refuses a deletion not confirmed, whose retention has not
// elapsed, or held by a deleted tenant, whose purge takes it along.
func purgeReady(db *gorm.DB, d ResourceDeletion) error {
	if d.ConfirmedAt == nil || db.NowFunc().UTC().Before(d.PurgeAfter) {
		return errors.WithStack(port.ErrPurgeNotReady)
	}
	if d.Family == model.FamilyTenant {
		return nil
	}
	var n int64
	if err := db.Model(&ResourceDeletion{}).Where("family = ? AND resource_id = ?", model.FamilyTenant, d.TenantID).Count(&n).Error; err != nil {
		return errors.WithStack(err)
	}
	if n > 0 {
		return errors.Wrap(port.ErrPurgeNotReady, "the tenant is deleted")
	}
	return nil
}

// purgeRows removes up to batch rows of the table in the scope of the
// deletion, and returns how many it removed.
func purgeRows(db *gorm.DB, t lifecycleTable, d ResourceDeletion, batch int) (int64, error) {
	if !db.Migrator().HasTable(t.table) {
		return 0, nil
	}
	predicate, args := t.scopePredicate(d)
	if predicate == "1 = 0" {
		return 0, nil
	}
	key := t.keyColumns()
	columns := strings.Join(key, ", ")
	if len(key) > 1 {
		columns = "(" + columns + ")"
	}
	statement := "DELETE FROM " + t.table + " WHERE " + columns + " IN (SELECT " + qualified("r", key) + " FROM " + t.table + " r WHERE " + predicate + " LIMIT ?)"
	result := db.Exec(statement, append(args, batch)...)
	return result.RowsAffected, errors.WithStack(result.Error)
}

// projectionScope selects, over a table of projections or of events, those
// of the resources of the scope of a deletion. It runs before the rows are
// removed: the identifiers of quotas and alerts are read from them.
func projectionScope(d ResourceDeletion) (string, []any) {
	quotas, quotaArgs := lifecycleTableOf("quota").scopePredicate(d)
	switch d.Family {
	case model.FamilyTenant:
		return "tenant_id = ?", []any{d.ResourceID}
	case model.FamilyOrganization:
		return "tenant_id = ? AND (org_id = ? OR (family = ? AND resource_key = ?) OR (family = ? AND resource_key IN (SELECT r.id FROM quota r WHERE " + quotas + ")))",
			append([]any{d.TenantID, d.ResourceID, model.FamilyOrganization, d.ResourceID, model.FamilyQuota}, quotaArgs...)
	}
	alerts, alertArgs := lifecycleTableOf("alerts").scopePredicate(d)
	args := []any{d.TenantID, model.FamilyMember, model.FamilyOrganizationMembership, d.ResourceID, model.FamilyQuota}
	args = append(append(append(args, quotaArgs...), model.FamilyAlert), alertArgs...)
	return "tenant_id = ? AND ((family IN (?, ?) AND resource_key = ?)" +
		" OR (family = ? AND resource_key IN (SELECT r.id FROM quota r WHERE " + quotas + "))" +
		" OR (family = ? AND resource_key IN (SELECT r.id FROM alerts r WHERE " + alerts + ")))", args
}

// retireProjections removes the projections of the scope, the events about
// them and the deliveries queued for those events, then publishes the
// deletion. A tenant publishes its own deletion only, an organization those
// of the resources that do not hang from it, a member all of its own. A
// tenant also retires the identifiers of its organizations and members: the
// guards keep refusing them once purged.
func retireProjections(db *gorm.DB, d ResourceDeletion) error {
	if d.Family == model.FamilyMember {
		// The teams outlive the member: its team memberships are removed and
		// the teams republished before the projections of the scope go.
		if err := detachTeamMemberships(db.Statement.Context, db, d.ResourceID); err != nil {
			return err
		}
	}
	scope, args := projectionScope(d)
	var published []ProvisioningProjection
	q := db.Where(scope, args...)
	switch d.Family {
	case model.FamilyTenant:
		q = db.Where(projectionWhere, model.FamilyTenant, d.ResourceID, "", d.ResourceID)
	case model.FamilyOrganization:
		q = q.Where("org_id <> ?", d.ResourceID)
	}
	if err := q.Order("family, org_id, resource_key").Find(&published).Error; err != nil {
		return errors.WithStack(err)
	}
	if err := db.Where(scope, args...).Delete(&ProvisioningEvent{}).Error; err != nil {
		return errors.WithStack(err)
	}
	if err := dropOrphanDeliveries(db, d.TenantID); err != nil {
		return err
	}
	if err := db.Where(scope, args...).Delete(&ProvisioningProjection{}).Error; err != nil {
		return errors.WithStack(err)
	}
	if d.Family == model.FamilyTenant {
		if err := retireTenantChildren(db, d); err != nil {
			return err
		}
	}
	if len(published) == 0 {
		return nil
	}
	if err := lockProvisioningFeed(db); err != nil {
		return err
	}
	publish, err := newEventAppender(db.Statement.Context, db)
	if err != nil {
		return err
	}
	for _, p := range published {
		sequence, err := nextProvisioningSequence(db)
		if err != nil {
			return err
		}
		if err := publish(sequence, p.id(), model.CommonEventDeleted, ""); err != nil {
			return err
		}
	}
	return nil
}

// dropOrphanDeliveries removes the webhook deliveries of the tenant whose
// event the purge removed. Above the feed floor, an event is only ever
// missing because a purge removed it: the retention raises the floor with
// the events it removes, and keeps their deliveries. The webhook worker
// prepares deliveries without the feed lock: the purge drops them again once
// its rows are gone, in case one was prepared in between.
func dropOrphanDeliveries(db *gorm.DB, tenantID string) error {
	return errors.WithStack(db.Exec("DELETE FROM webhook_deliveries WHERE tenant_id = ?"+
		" AND sequence > (SELECT floor FROM provisioning_feeds WHERE id = ?)"+
		" AND NOT EXISTS (SELECT 1 FROM provisioning_events e WHERE e.sequence = webhook_deliveries.sequence)",
		tenantID, provisioningFeedID).Error)
}

// retireTenantChildren records the organizations and the members of a
// tenant as deleted with it, so that their identifiers stay retired.
func retireTenantChildren(db *gorm.DB, d ResourceDeletion) error {
	now := db.NowFunc().UTC().Truncate(time.Microsecond)
	for _, child := range []struct{ family, table string }{{model.FamilyOrganization, "organizations"}, {model.FamilyMember, "users"}} {
		if err := db.Exec("INSERT INTO resource_deletions (family, resource_id, tenant_id, deleted_at, purge_after, purged_with, export_sha256, attempts, diagnostic, purging)"+
			" SELECT ?, c.id, ?, ?, ?, ?, '', 0, '', ? FROM "+child.table+" c WHERE c.tenant_id = ?"+
			" AND NOT EXISTS (SELECT 1 FROM resource_deletions d WHERE d.family = ? AND d.resource_id = c.id)",
			child.family, d.ResourceID, now, now, d.ResourceID, true, d.ResourceID, child.family).Error; err != nil {
			return errors.WithStack(err)
		}
	}
	return nil
}

// finishPurge removes the audits of the scope, anonymizes the operations of a
// purged member, detaches it from the shared alerts it owned, and marks the
// deletion purged, along with those of a purged tenant.
func finishPurge(ctx context.Context, db *gorm.DB, d ResourceDeletion) error {
	about, args := auditScope(db, d)
	if d.Family == model.FamilyMember {
		about, args = memberAudits(db, d)
		about = "r.tenant_id = ? AND " + about
		args = append([]any{d.TenantID}, args...)
	}
	if err := db.Exec("DELETE FROM "+auditTable+" WHERE id IN (SELECT r.id FROM "+auditTable+" r WHERE r.resource <> ? AND "+about+")",
		append([]any{deletionAuditResource}, args...)...).Error; err != nil {
		return errors.WithStack(err)
	}
	if d.Family == model.FamilyMember {
		if err := db.Exec("UPDATE "+auditTable+" SET actor = ? WHERE tenant_id = ? AND "+jsonField(db, "actor", "user_id")+" = ?",
			erasedActor, d.TenantID, d.ResourceID).Error; err != nil {
			return errors.WithStack(err)
		}
		if err := detachAlertOwner(ctx, db, d.ResourceID); err != nil {
			return err
		}
	}
	if err := dropOrphanDeliveries(db, d.TenantID); err != nil {
		return err
	}
	now := db.NowFunc().UTC().Truncate(time.Microsecond)
	if err := db.Model(&ResourceDeletion{}).Where("family = ? AND resource_id = ?", d.Family, d.ResourceID).
		Updates(map[string]any{"purged_at": now, "diagnostic": model.DeletionPurged}).Error; err != nil {
		return errors.WithStack(err)
	}
	if d.Family == model.FamilyTenant {
		if err := db.Model(&ResourceDeletion{}).Where("tenant_id = ? AND purged_at IS NULL", d.ResourceID).
			Updates(map[string]any{"purged_at": now, "purged_with": d.ResourceID, "diagnostic": model.DeletionPurged}).Error; err != nil {
			return errors.WithStack(err)
		}
	}
	return auditDeletion(ctx, db, d, map[string]any{"action": "purged"})
}

// detachTeamMemberships removes a purged member from the teams it belonged
// to, with its budgets inside them. The teams belong to their organization and
// outlive it. The changes are published.
func detachTeamMemberships(ctx context.Context, db *gorm.DB, userID string) error {
	membershipIDs := db.Model(&Membership{}).Select("id").Where("user_id = ?", userID)
	var teams []struct {
		TeamID       string
		MembershipID string
	}
	if err := db.Table("team_members").Select("team_id, membership_id").Where("membership_id IN (?)", membershipIDs).
		Order("team_id").Scan(&teams).Error; err != nil {
		return errors.WithStack(err)
	}
	if len(teams) == 0 {
		return nil
	}
	// The worker holds no write authority: the purge was authorized when the
	// deletion was recorded and confirmed.
	recorder := newMutationRecorder(db, nil)
	for _, t := range teams {
		if err := recorder.track("team", t.TeamID); err != nil {
			return err
		}
		if err := recorder.trackTeamMemberQuotas(t.TeamID, t.MembershipID); err != nil {
			return err
		}
	}
	if err := deleteTeamMembershipsWithin(db, db.Model(&Membership{}).Select("id").Where("user_id = ?", userID)); err != nil {
		return err
	}
	return recorder.flush(ctx)
}

// detachAlertOwner removes a purged member from the shared alerts it owned:
// they belong to their organization and outlive it. The change is published.
func detachAlertOwner(ctx context.Context, db *gorm.DB, userID string) error {
	var ids []string
	if err := db.Table("alerts").Where("owner_id = ? AND scope <> ?", userID, string(model.AlertScopePersonal)).Order("id").Pluck("id", &ids).Error; err != nil {
		return errors.WithStack(err)
	}
	if len(ids) == 0 {
		return nil
	}
	// The worker holds no write authority: the purge was authorized when the
	// deletion was recorded and confirmed.
	recorder := newMutationRecorder(db, nil)
	for _, id := range ids {
		if err := recorder.track("alert", id); err != nil {
			return err
		}
	}
	if err := db.Table("alerts").Where("id IN ?", ids).Update("owner_id", "").Error; err != nil {
		return errors.WithStack(err)
	}
	return recorder.flush(ctx)
}
