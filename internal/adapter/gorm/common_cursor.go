package gorm

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
)

// Cursor v1 tokens are HMAC authenticated, instance-bound and opaque. Lists
// expire after 24h; event positions expire only when retained history is lost.
const CommonListCursorLifetime = 24 * time.Hour

type commonCursor struct {
	Version  int               `json:"v"`
	Kind     string            `json:"k"`
	Scope    model.CommonScope `json:"s"`
	Limit    int               `json:"l"`
	After    string            `json:"a"`
	Position int64             `json:"p"`
	Expires  int64             `json:"e"`
}

func encodeCursor(feed CommonFeed, c commonCursor) (string, error) {
	c.Version = 1
	raw, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(feed.CursorSecret))
	mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
func decodeCursor(feed CommonFeed, token string) (commonCursor, error) {
	var c commonCursor
	if len(token) > 4096 {
		return c, port.ErrInvalidCursor
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return c, port.ErrInvalidCursor
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if err != nil {
		return c, port.ErrInvalidCursor
	}
	sig, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil {
		return c, port.ErrInvalidCursor
	}
	mac := hmac.New(sha256.New, []byte(feed.CursorSecret))
	mac.Write(raw)
	if !hmac.Equal(sig, mac.Sum(nil)) || json.Unmarshal(raw, &c) != nil || c.Version != 1 {
		return c, port.ErrInvalidCursor
	}
	return c, nil
}
func (s *Store) ListCommon(ctx context.Context, scope model.CommonScope, token string, limit int) (model.CommonPage, error) {
	out := model.CommonPage{Items: []model.CommonItem{}}
	if limit < 1 || limit > 1000 {
		return out, port.ErrInvalid
	}
	if err := validateCommonScope(scope, ""); err != nil {
		return out, err
	}
	db, err := s.getDatabase(ctx)
	if err != nil {
		return out, err
	}
	db = db.WithContext(ctx)
	if err := commonParents(db, scope); err != nil {
		return out, err
	}
	var feed CommonFeed
	if err := db.First(&feed, 1).Error; err != nil {
		return out, err
	}
	c := commonCursor{Kind: "list", Scope: scope, Limit: limit, Expires: db.NowFunc().Add(CommonListCursorLifetime).Unix()}
	if token != "" {
		c, err = decodeCursor(feed, token)
		if err != nil {
			return out, err
		}
		if c.Kind != "list" || c.Scope != scope || c.Limit != limit {
			return out, port.ErrInvalidCursor
		}
		if c.Expires <= db.NowFunc().Unix() {
			return out, port.ErrCursorExpired
		}
	}
	// Canonical ASCII keys must not depend on the database's locale collation.
	col := "key COLLATE BINARY"
	if db.Dialector.Name() == "postgres" {
		col = `key COLLATE "C"`
	}
	var rows []CommonRecord
	q := scopeQuery(db, scope)
	if c.After != "" {
		q = q.Where(col+" > ?", c.After)
	}
	if err := q.Order(col + " ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return out, err
	}
	if len(rows) > limit {
		rows = rows[:limit]
		c.After = rows[len(rows)-1].Key
		next, err := encodeCursor(feed, c)
		if err != nil {
			return out, err
		}
		out.NextCursor = &next
	}
	for _, row := range rows {
		item, err := row.item()
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}
func (s *Store) CaptureCommonCursor(ctx context.Context) (string, error) {
	var token string
	err := s.identityTransaction(ctx, func(tx *Store) error {
		db, err := tx.getDatabase(ctx)
		if err != nil {
			return err
		}
		var feed CommonFeed
		if err := db.First(&feed, 1).Error; err != nil {
			return err
		}
		var clock PublicationClock
		if err := db.First(&clock, 1).Error; err != nil {
			return err
		}
		token, err = encodeCursor(feed, commonCursor{Kind: "events", Position: clock.Sequence})
		return err
	})
	return token, err
}
func (s *Store) ReadCommonEvents(ctx context.Context, token string, limit int) (model.CommonEventPage, error) {
	out := model.CommonEventPage{Items: []model.CommonEvent{}}
	if limit < 1 || limit > 1000 {
		return out, port.ErrInvalid
	}
	err := s.identityTransaction(ctx, func(tx *Store) error {
		// Holding the allocation lock also excludes retention while capturing the
		// horizon and its rows. There is no MAX(sequence) visibility race.
		db, err := tx.getDatabase(ctx)
		if err != nil {
			return err
		}
		var feed CommonFeed
		if err := db.First(&feed, 1).Error; err != nil {
			return err
		}
		c, err := decodeCursor(feed, token)
		if err != nil {
			return err
		}
		if c.Kind != "events" || c.Position < 0 {
			return port.ErrInvalidCursor
		}
		var clock PublicationClock
		if err := db.First(&clock, 1).Error; err != nil {
			return err
		}
		if c.Position > clock.Sequence {
			return port.ErrInvalidCursor
		}
		if c.Position < feed.Floor {
			return port.ErrCursorExpired
		}
		var rows []Publication
		if err := db.Where("sequence > ? AND sequence <= ?", c.Position, clock.Sequence).Order("sequence ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
			return err
		}
		out.Items = []model.CommonEvent{}
		out.HasMore = len(rows) > limit
		if out.HasMore {
			rows = rows[:limit]
			c.Position = rows[len(rows)-1].Sequence
		} else {
			c.Position = clock.Sequence
		}
		for _, row := range rows {
			var event model.CommonEvent
			if err := json.Unmarshal([]byte(row.Payload), &event); err != nil {
				return fmt.Errorf("decode publication: %w", err)
			}
			out.Items = append(out.Items, event)
		}
		out.NextCursor, err = encodeCursor(feed, c)
		return err
	})
	return out, err
}

// PurgeCommonEvents removes only a settled prefix older than before. Default
// policy is unlimited retention; operators may invoke this maintenance method.
// A future-dated fact stops the prefix even if later timestamps go backwards.
func (s *Store) PurgeCommonEvents(ctx context.Context, before time.Time) error {
	return s.identityTransaction(ctx, func(tx *Store) error {
		db, err := tx.getDatabase(ctx)
		if err != nil {
			return err
		}
		var clock PublicationClock
		if err := db.First(&clock, 1).Error; err != nil {
			return err
		}
		var firstRetained Publication
		err = db.Where("created_at >= ?", before).Order("sequence ASC").First(&firstRetained).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			return err
		}
		floor := clock.Sequence
		if err == nil {
			floor = firstRetained.Sequence - 1
		}
		// Retain the last actually removed event, not an intervening audit gap.
		// A checkpoint immediately after that event can still resume safely.
		var removed Publication
		err = db.Where("sequence <= ?", floor).Order("sequence DESC").First(&removed).Error
		if err == gorm.ErrRecordNotFound {
			return nil
		}
		if err != nil {
			return err
		}
		floor = removed.Sequence
		if err := db.Where("sequence <= ?", floor).Delete(&Publication{}).Error; err != nil {
			return err
		}
		return db.Model(&CommonFeed{}).Where("id = 1 AND floor < ?", floor).Update("floor", floor).Error
	})
}
