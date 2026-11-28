package gorm

import (
	"context"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
)

// RequestFailure is a call the provider failed before answering, see
// model.RequestFailure. It is a table of its own, apart from usage_records, so
// that no count of requests, budget or plan ever reads it.
//
// idx_request_failures_org_created and idx_request_failures_user_created serve
// the request logs, which list an organization's or a user's failures over a
// period, the most recent first.
type RequestFailure struct {
	ID             string    `gorm:"primaryKey;autoIncrement:false"`
	CreatedAt      time.Time `gorm:"not null;index;index:idx_request_failures_org_created,priority:2;index:idx_request_failures_user_created,priority:2"`
	UserID         string    `gorm:"not null;default:'';index:idx_request_failures_user_created,priority:1"`
	ApplicationID  string    `gorm:"not null;default:''"`
	OrgID          string    `gorm:"not null;index:idx_request_failures_org_created,priority:1"`
	TeamID         string    `gorm:"not null;default:''"`
	ProviderID     string    `gorm:"not null;default:''"`
	ModelID        string    `gorm:"not null;default:''"`
	ProxyModelName string    `gorm:"not null;default:''"`
	AuthTokenID    string    `gorm:"not null;default:''"`
	ProviderKeyID  string    `gorm:"not null;default:''"`
	HTTPStatus     int       `gorm:"not null;default:0"`
	ErrorClass     string    `gorm:"not null;default:''"`
	RoutingUs      *int64
	FirstOutputUs  *int64
	UpstreamUs     *int64
	Streamed       bool   `gorm:"not null;default:false"`
	SessionID      string `gorm:"not null;default:''"`
	// Spans is the JSON of the trace's spans, see traceSpanRow; NULL when the
	// failure kept no trace.
	Spans          *string `gorm:"type:text"`
	TraceTruncated bool    `gorm:"not null;default:false"`
}

func fromRequestFailure(f *model.RequestFailure) (*RequestFailure, error) {
	row := &RequestFailure{
		ID:             string(f.ID),
		CreatedAt:      f.CreatedAt,
		UserID:         string(f.UserID),
		ApplicationID:  string(f.ApplicationID),
		OrgID:          string(f.OrgID),
		TeamID:         string(f.TeamID),
		ProviderID:     string(f.ProviderID),
		ModelID:        string(f.ModelID),
		ProxyModelName: f.ProxyModelName,
		AuthTokenID:    f.AuthTokenID,
		ProviderKeyID:  string(f.ProviderKeyID),
		HTTPStatus:     f.HTTPStatus,
		ErrorClass:     f.ErrorClass,
		RoutingUs:      durationToMicros(f.Timing.Routing),
		FirstOutputUs:  durationToMicros(f.Timing.FirstOutput),
		UpstreamUs:     durationToMicros(f.Timing.Upstream),
		Streamed:       f.Streamed,
		SessionID:      f.SessionID,
	}
	if f.Trace != nil && len(f.Trace.Spans) > 0 {
		spans, err := encodeSpans(f.Trace.Spans)
		if err != nil {
			return nil, err
		}
		row.Spans = &spans
		row.TraceTruncated = f.Trace.Truncated
	}
	return row, nil
}

func (r *RequestFailure) toModel() (*model.RequestFailure, error) {
	f := &model.RequestFailure{
		ID:             model.RequestFailureID(r.ID),
		CreatedAt:      r.CreatedAt,
		UserID:         model.UserID(r.UserID),
		ApplicationID:  model.ApplicationID(r.ApplicationID),
		OrgID:          model.OrgID(r.OrgID),
		TeamID:         model.TeamID(r.TeamID),
		ProviderID:     model.ProviderID(r.ProviderID),
		ModelID:        model.LLMModelID(r.ModelID),
		ProxyModelName: r.ProxyModelName,
		AuthTokenID:    r.AuthTokenID,
		ProviderKeyID:  model.ProviderKeyID(r.ProviderKeyID),
		HTTPStatus:     r.HTTPStatus,
		ErrorClass:     r.ErrorClass,
		Timing: model.UsageTiming{
			Routing:     microsToDuration(r.RoutingUs),
			FirstOutput: microsToDuration(r.FirstOutputUs),
			Upstream:    microsToDuration(r.UpstreamUs),
		},
		Streamed:  r.Streamed,
		SessionID: r.SessionID,
	}
	if r.Spans != nil {
		spans, err := decodeSpans(*r.Spans)
		if err != nil {
			return nil, err
		}
		f.Trace = &model.UsageTrace{Spans: spans, Truncated: r.TraceTruncated}
	}
	return f, nil
}

// RecordRequestFailure implements port.UsageStore.
func (s *Store) RecordRequestFailure(ctx context.Context, failure *model.RequestFailure) error {
	row, err := fromRequestFailure(failure)
	if err != nil {
		return err
	}
	return s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Create(row).Error)
	})
}

// QueryRequestFailures implements port.UsageStore.
func (s *Store) QueryRequestFailures(ctx context.Context, filter port.RequestFailureFilter) ([]*model.RequestFailure, error) {
	var failures []*model.RequestFailure
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		query := db.Model(&RequestFailure{})
		if filter.OrgID != nil {
			query = query.Where("org_id = ?", string(*filter.OrgID))
		}
		if filter.UserID != nil {
			query = query.Where("user_id = ?", string(*filter.UserID))
		}
		if filter.Since != nil {
			query = query.Where("created_at >= ?", *filter.Since)
		}
		if filter.Until != nil {
			query = query.Where("created_at < ?", *filter.Until)
		}
		if filter.Limit != nil {
			query = query.Limit(*filter.Limit)
		}
		if filter.Offset != nil {
			query = query.Offset(*filter.Offset)
		}
		var rows []RequestFailure
		if err := query.Order("created_at DESC").Order("id DESC").Find(&rows).Error; err != nil {
			return errors.WithStack(err)
		}
		failures = make([]*model.RequestFailure, 0, len(rows))
		for i := range rows {
			failure, err := rows[i].toModel()
			if err != nil {
				return err
			}
			failures = append(failures, failure)
		}
		return nil
	})
	return failures, err
}

// PurgeRequestFailures implements port.UsageStore, like PurgeUsageTraces.
func (s *Store) PurgeRequestFailures(ctx context.Context, before time.Time) (int64, error) {
	var purged int64
	for {
		if err := ctx.Err(); err != nil {
			return purged, err
		}
		var removed int64
		err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
			batch := db.Model(&RequestFailure{}).Select("id").
				Where("created_at < ?", before).
				Where(notFrozen("request_failures", "request_failures")).
				Limit(purgeBatchSize)
			result := db.Where("id IN (?)", batch).Delete(&RequestFailure{})
			removed = result.RowsAffected
			return errors.WithStack(result.Error)
		})
		if err != nil {
			return purged, err
		}
		purged += removed
		if removed < purgeBatchSize {
			return purged, nil
		}
	}
}
