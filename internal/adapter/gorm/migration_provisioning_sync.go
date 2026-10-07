package gorm

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"

	"github.com/google/uuid"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const provisioningSyncMigrationID = "202610080001"

// projectionBackfillBatch bounds the keys loaded at once by the backfill.
const projectionBackfillBatch = 500

// migrateProvisioningSync creates the projections and the event feed, then
// projects every existing resource. Each projection receives its own feed
// position as revision; no event is emitted for the existing state, which
// consumers read through an inventory.
func migrateProvisioningSync(tx *gorm.DB) error {
	ctx := tx.Statement.Context
	slog.WarnContext(ctx, "migration "+provisioningSyncMigrationID+" requires ALL old servers to be stopped: an old server writes without publishing, so projections, ETags and the event feed would silently diverge")
	if err := tx.AutoMigrate(&ProvisioningProjection{}, &ProvisioningEvent{}, &ProvisioningFeed{}); err != nil {
		return errors.WithStack(err)
	}
	if isPostgres(tx) {
		// Keys are paginated in bytewise order, whatever the database locale.
		for _, statement := range []string{
			`ALTER TABLE provisioning_projections ALTER COLUMN resource_key TYPE text COLLATE "C"`,
			"CREATE SEQUENCE IF NOT EXISTS " + provisioningEventSequence,
		} {
			if err := tx.Exec(statement).Error; err != nil {
				return errors.WithStack(err)
			}
		}
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return errors.WithStack(err)
	}
	feed := ProvisioningFeed{ID: provisioningFeedID, Source: "urn:uuid:" + uuid.NewString(), CursorSecret: hex.EncodeToString(secret[:])}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&feed).Error; err != nil {
		return errors.WithStack(err)
	}
	for _, kind := range []string{"tenant", "domain", "organization", "user", "membership"} {
		if err := backfillProjections(tx, kind); err != nil {
			return err
		}
	}
	return nil
}

// backfillProjections projects every resource of a kind, by batches of keys.
// A membership crossing tenants is left unprojected and logged: it must not
// prevent the startup.
func backfillProjections(tx *gorm.DB, kind string) error {
	column := resourceKey(kind)
	after := ""
	for {
		var ids []string
		if err := tx.Table(resourceTables[kind]).Where(column+" > ?", after).Order(column).Limit(projectionBackfillBatch).Pluck(column, &ids).Error; err != nil {
			return errors.WithStack(err)
		}
		for _, id := range ids {
			key := mutationKey{kind, id}
			raw, err := mutationSnapshot(tx, key)
			if err != nil {
				return errors.WithStack(err)
			}
			projection, err := projectionOf(tx, key, raw)
			if errors.Is(err, port.ErrParentNotFound) {
				slog.WarnContext(tx.Statement.Context, "resource left out of the provisioning projections: its parents belong to another tenant",
					slog.String("kind", kind), slog.String("id", id))
				continue
			}
			if err != nil {
				return err
			}
			if projection == nil {
				continue
			}
			if projection.Revision, err = nextProvisioningSequence(tx); err != nil {
				return err
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(projection).Error; err != nil {
				return errors.WithStack(err)
			}
		}
		if len(ids) < projectionBackfillBatch {
			return nil
		}
		after = ids[len(ids)-1]
	}
}
