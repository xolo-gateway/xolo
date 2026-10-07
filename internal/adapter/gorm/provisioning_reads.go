package gorm

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
)

// ProvisioningListCursorLifetime bounds an enumeration from its first page.
// The clock only serves this expiry: it never orders nor validates writes.
const ProvisioningListCursorLifetime = 24 * time.Hour

// purgeBatchSize bounds the events removed by one purge transaction.
const purgeBatchSize = 1000

// provisioningCursor v1 tokens are HMAC authenticated and bound to the
// instance; their content is opaque but not encrypted.
type provisioningCursor struct {
	Version  int               `json:"v"`
	Kind     string            `json:"k"`
	Scope    model.CommonScope `json:"s"`
	Limit    int               `json:"l,omitempty"`
	After    string            `json:"a,omitempty"`
	Position int64             `json:"p,omitempty"`
	Expires  int64             `json:"e,omitempty"`
}

const (
	cursorList   = "list"
	cursorEvents = "events"
)

func cursorMAC(feed ProvisioningFeed, raw []byte) []byte {
	mac := hmac.New(sha256.New, []byte(feed.CursorSecret))
	mac.Write(raw)
	return mac.Sum(nil)
}

func encodeCursor(feed ProvisioningFeed, c provisioningCursor) (string, error) {
	c.Version = 1
	raw, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(cursorMAC(feed, raw)), nil
}

func decodeCursor(feed ProvisioningFeed, token string) (provisioningCursor, error) {
	var c provisioningCursor
	if len(token) > 4096 {
		return c, errors.WithStack(port.ErrInvalidCursor)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return c, errors.WithStack(port.ErrInvalidCursor)
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if err != nil {
		return c, errors.WithStack(port.ErrInvalidCursor)
	}
	sig, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil {
		return c, errors.WithStack(port.ErrInvalidCursor)
	}
	if !hmac.Equal(sig, cursorMAC(feed, raw)) || json.Unmarshal(raw, &c) != nil || c.Version != 1 {
		return c, errors.WithStack(port.ErrInvalidCursor)
	}
	return c, nil
}

func validateCommonScope(scope model.CommonScope, key string) error {
	if scope.TenantID != "" {
		if _, err := model.ParseTenantID(scope.TenantID); err != nil {
			return errors.WithStack(port.ErrInvalid)
		}
	}
	if scope.OrganizationID != "" {
		if _, err := model.ParseOrgID(scope.OrganizationID); err != nil {
			return errors.WithStack(port.ErrInvalid)
		}
	}
	switch scope.Family {
	case model.FamilyTenant:
		if scope.TenantID != "" || scope.OrganizationID != "" {
			return errors.WithStack(port.ErrInvalid)
		}
	case model.FamilyOrganization, model.FamilyMember, model.FamilyTenantDomain:
		if scope.TenantID == "" || scope.OrganizationID != "" {
			return errors.WithStack(port.ErrInvalid)
		}
	case model.FamilyOrganizationMembership:
		if scope.TenantID == "" || scope.OrganizationID == "" {
			return errors.WithStack(port.ErrInvalid)
		}
	default:
		return errors.WithStack(port.ErrInvalid)
	}
	if key == "" {
		return nil
	}
	var err error
	switch scope.Family {
	case model.FamilyTenantDomain:
		if host, err := model.NormalizeHostname(key); err != nil || host != key {
			return errors.WithStack(port.ErrInvalidHostname)
		}
		return nil
	case model.FamilyTenant:
		_, err = model.ParseTenantID(key)
	case model.FamilyOrganization:
		_, err = model.ParseOrgID(key)
	case model.FamilyMember, model.FamilyOrganizationMembership:
		_, err = model.ParseUserID(key)
	}
	if err != nil {
		return errors.WithStack(port.ErrInvalid)
	}
	return nil
}

// commonParents checks the chain of parents of a scope: the organization must
// belong to the tenant.
func commonParents(db *gorm.DB, scope model.CommonScope) error {
	var n int64
	if scope.TenantID != "" {
		if err := db.Table("tenants").Where("id = ?", scope.TenantID).Count(&n).Error; err != nil {
			return errors.WithStack(err)
		}
		if n != 1 {
			return errors.WithStack(port.ErrParentNotFound)
		}
	}
	if scope.OrganizationID != "" {
		if err := db.Table("organizations").Where("id = ? AND tenant_id = ?", scope.OrganizationID, scope.TenantID).Count(&n).Error; err != nil {
			return errors.WithStack(err)
		}
		if n != 1 {
			return errors.WithStack(port.ErrParentNotFound)
		}
	}
	return nil
}

// scopeQuery filters projections on the whole scope, tenant included: a
// collection never reaches into another tenant.
func scopeQuery(db *gorm.DB, scope model.CommonScope) *gorm.DB {
	q := db.Model(&ProvisioningProjection{}).Where("family = ? AND org_id = ?", scope.Family, scope.OrganizationID)
	if scope.Family != model.FamilyTenant {
		q = q.Where("tenant_id = ?", scope.TenantID)
	}
	return q
}

func readProjection(db *gorm.DB, scope model.CommonScope, key string) (model.CommonItem, error) {
	if err := validateCommonScope(scope, key); err != nil {
		return model.CommonItem{}, err
	}
	if err := commonParents(db, scope); err != nil {
		if errors.Is(err, port.ErrParentNotFound) {
			return model.CommonItem{}, errors.WithStack(port.ErrNotFound)
		}
		return model.CommonItem{}, err
	}
	var rows []ProvisioningProjection
	if err := scopeQuery(db, scope).Where("resource_key = ?", key).Limit(1).Find(&rows).Error; err != nil {
		return model.CommonItem{}, errors.WithStack(err)
	}
	if len(rows) == 0 {
		return model.CommonItem{}, errors.WithStack(port.ErrNotFound)
	}
	return rows[0].item()
}

// readTransaction runs fn on one snapshot: on PostgreSQL a read-only
// repeatable read transaction, on SQLite a deferred transaction.
func (s *Store) readTransaction(ctx context.Context, fn func(db *gorm.DB) error) error {
	db, err := s.getDatabase(ctx)
	if err != nil {
		return errors.WithStack(err)
	}
	if s.transactionBound {
		return fn(db.WithContext(ctx))
	}
	var opts *sql.TxOptions
	if isPostgres(db) {
		opts = &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	}
	return retryTransaction(ctx, func() error {
		return db.WithContext(ctx).Transaction(fn, opts)
	})
}

func provisioningFeed(db *gorm.DB) (ProvisioningFeed, error) {
	var feed ProvisioningFeed
	err := db.First(&feed, provisioningFeedID).Error
	return feed, errors.WithStack(err)
}

// ReadProjection implements port.ProvisioningReader.
func (s *Store) ReadProjection(ctx context.Context, scope model.CommonScope, key string) (model.CommonItem, error) {
	var item model.CommonItem
	err := s.readTransaction(ctx, func(db *gorm.DB) error {
		var err error
		item, err = readProjection(db, scope, key)
		return err
	})
	return item, err
}

// ReadProjection implements port.ProvisioningTx: the changes made so far are
// audited and published first, so the item carries the revision this
// transaction wrote.
func (tx *provisioningTx) ReadProjection(ctx context.Context, scope model.CommonScope, key string) (model.CommonItem, error) {
	if err := tx.flushMutations(ctx); err != nil {
		return model.CommonItem{}, err
	}
	return readProjection(tx.db.WithContext(ctx), scope, key)
}

// ListProjections implements port.ProvisioningReader with keyset pagination
// on the bytewise order of keys. Pages are not a snapshot of the collection:
// a consumer reconciles them with the event feed.
func (s *Store) ListProjections(ctx context.Context, scope model.CommonScope, token string, limit int) (model.CommonPage, error) {
	out := model.CommonPage{Items: []model.CommonItem{}}
	if limit < 1 || limit > 1000 {
		return out, errors.WithStack(port.ErrInvalid)
	}
	if err := validateCommonScope(scope, ""); err != nil {
		return out, err
	}
	err := s.readTransaction(ctx, func(db *gorm.DB) error {
		out = model.CommonPage{Items: []model.CommonItem{}}
		if err := commonParents(db, scope); err != nil {
			return err
		}
		feed, err := provisioningFeed(db)
		if err != nil {
			return err
		}
		now := db.NowFunc().Unix()
		c := provisioningCursor{Kind: cursorList, Scope: scope, Limit: limit, Expires: now + int64(ProvisioningListCursorLifetime/time.Second)}
		if token != "" {
			if c, err = decodeCursor(feed, token); err != nil {
				return err
			}
			if c.Kind != cursorList || c.Scope != scope || c.Limit != limit {
				return errors.WithStack(port.ErrInvalidCursor)
			}
			if c.Expires <= now {
				return errors.WithStack(port.ErrCursorExpired)
			}
		}
		q := scopeQuery(db, scope)
		if c.After != "" {
			q = q.Where("resource_key > ?", c.After)
		}
		var rows []ProvisioningProjection
		if err := q.Order("resource_key ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
			return errors.WithStack(err)
		}
		if len(rows) > limit {
			rows = rows[:limit]
			c.After = rows[len(rows)-1].ResourceKey
			next, err := encodeCursor(feed, c)
			if err != nil {
				return err
			}
			out.NextCursor = &next
		}
		for _, row := range rows {
			item, err := row.item()
			if err != nil {
				return err
			}
			out.Items = append(out.Items, item)
		}
		return nil
	})
	return out, err
}

// feedHorizon is the last position a reader may safely resume after. Sequences
// are allocated under the feed lock held until commit, so every position below
// a visible event is either visible or rolled back: there is never an
// in-flight transaction behind the horizon.
func feedHorizon(db *gorm.DB, feed ProvisioningFeed) (int64, error) {
	var last sql.NullInt64
	if err := db.Model(&ProvisioningEvent{}).Select("MAX(sequence)").Row().Scan(&last); err != nil {
		return 0, errors.WithStack(err)
	}
	return max(last.Int64, feed.Floor), nil
}

// CaptureEventCursor implements port.ProvisioningReader.
func (s *Store) CaptureEventCursor(ctx context.Context) (string, error) {
	var token string
	err := s.readTransaction(ctx, func(db *gorm.DB) error {
		feed, err := provisioningFeed(db)
		if err != nil {
			return err
		}
		horizon, err := feedHorizon(db, feed)
		if err != nil {
			return err
		}
		token, err = encodeCursor(feed, provisioningCursor{Kind: cursorEvents, Position: horizon})
		return err
	})
	return token, err
}

// ReadEvents implements port.ProvisioningReader.
func (s *Store) ReadEvents(ctx context.Context, token string, limit int) (model.CommonEventPage, error) {
	out := model.CommonEventPage{Items: []model.CommonEvent{}}
	if limit < 1 || limit > 1000 {
		return out, errors.WithStack(port.ErrInvalid)
	}
	err := s.readTransaction(ctx, func(db *gorm.DB) error {
		out = model.CommonEventPage{Items: []model.CommonEvent{}}
		feed, err := provisioningFeed(db)
		if err != nil {
			return err
		}
		c, err := decodeCursor(feed, token)
		if err != nil {
			return err
		}
		if c.Kind != cursorEvents || c.Position < 0 {
			return errors.WithStack(port.ErrInvalidCursor)
		}
		horizon, err := feedHorizon(db, feed)
		if err != nil {
			return err
		}
		if c.Position > horizon {
			return errors.WithStack(port.ErrInvalidCursor)
		}
		if c.Position < feed.Floor {
			return errors.WithStack(port.ErrCursorExpired)
		}
		var rows []ProvisioningEvent
		if err := db.Where("sequence > ?", c.Position).Order("sequence ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
			return errors.WithStack(err)
		}
		next := horizon
		if len(rows) > limit {
			rows = rows[:limit]
			out.HasMore = true
			next = rows[len(rows)-1].Sequence
		}
		for _, row := range rows {
			var event model.CommonEvent
			if err := json.Unmarshal([]byte(row.Payload), &event); err != nil {
				return errors.WithStack(err)
			}
			out.Items = append(out.Items, event)
		}
		out.NextCursor, err = encodeCursor(feed, provisioningCursor{Kind: cursorEvents, Position: next})
		return err
	})
	return out, err
}

// PurgeEvents implements port.ProvisioningReader. It removes the prefix of
// the feed created before the given time, stopping at the first event kept, so
// a clock step back never removes an event behind a retained one. Each batch
// is a short transaction; the floor only ever rises, and only here.
func (s *Store) PurgeEvents(ctx context.Context, before time.Time) (int64, error) {
	db, err := s.getDatabase(ctx)
	if err != nil {
		return 0, errors.WithStack(err)
	}
	var purged int64
	for {
		if err := ctx.Err(); err != nil {
			return purged, err
		}
		var removed int64
		done := false
		err := retryTransaction(ctx, func() error {
			removed, done = 0, false
			return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				feed, err := provisioningFeed(tx)
				if err != nil {
					return err
				}
				var rows []ProvisioningEvent
				if err := tx.Select("sequence", "created_at").Where("sequence > ?", feed.Floor).
					Order("sequence ASC").Limit(purgeBatchSize).Find(&rows).Error; err != nil {
					return errors.WithStack(err)
				}
				last := feed.Floor
				for _, row := range rows {
					if !row.CreatedAt.Before(before) {
						done = true
						break
					}
					last = row.Sequence
				}
				if len(rows) < purgeBatchSize {
					done = true
				}
				if last == feed.Floor {
					done = true
					return nil
				}
				// The floor rises with the deletion, in the same snapshot
				// for readers.
				if err := tx.Model(&ProvisioningFeed{}).Where("id = ? AND floor < ?", provisioningFeedID, last).Update("floor", last).Error; err != nil {
					return errors.WithStack(err)
				}
				result := tx.Where("sequence > ? AND sequence <= ?", feed.Floor, last).Delete(&ProvisioningEvent{})
				if result.Error != nil {
					return errors.WithStack(result.Error)
				}
				removed = result.RowsAffected
				return nil
			})
		})
		if err != nil {
			return purged, err
		}
		purged += removed
		if done {
			return purged, nil
		}
	}
}

var _ port.ProvisioningReader = (*Store)(nil)
