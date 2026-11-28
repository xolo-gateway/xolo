package gorm

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
)

// UsageTrace keeps the spans of one proxy call beside its usage record. It is
// written only when the spans say more than the record does, so most requests
// have none.
//
// The organization and the user are repeated from the record so that the
// lifecycle inventory scopes the row on its own, without reading
// usage_records, and so that the retention deletes by date without a join.
type UsageTrace struct {
	UsageRecordID string    `gorm:"primaryKey;autoIncrement:false"`
	OrgID         string    `gorm:"not null;index"`
	UserID        string    `gorm:"not null;default:''"`
	CreatedAt     time.Time `gorm:"not null;index"`
	// Spans is the JSON of the spans, see traceSpanRow.
	Spans     string `gorm:"type:text;not null"`
	Truncated bool   `gorm:"not null;default:false"`
}

// traceSpanRow is the stored form of a model.TraceSpan. Its durations are
// microseconds and its names short, so that the stored form stays stable
// whatever the model's Go types become.
type traceSpanRow struct {
	Kind       string            `json:"k"`
	Name       string            `json:"n,omitempty"`
	Parent     int               `json:"p"`
	StartUs    int64             `json:"s"`
	DurationUs int64             `json:"d"`
	Status     string            `json:"st"`
	HTTPStatus int               `json:"h,omitempty"`
	ErrorClass string            `json:"e,omitempty"`
	Attributes map[string]string `json:"a,omitempty"`
}

// usageTraceRow returns the row storing the trace a record carries, nil when
// it carries none.
func usageTraceRow(record model.UsageRecord, row *UsageRecord) (*UsageTrace, error) {
	carrier, ok := record.(model.UsageTraceCarrier)
	if !ok {
		return nil, nil
	}
	trace := carrier.Trace()
	if trace == nil || len(trace.Spans) == 0 {
		return nil, nil
	}
	data, err := encodeSpans(trace.Spans)
	if err != nil {
		return nil, err
	}
	// The record's date is set by its own Create, which runs first; the
	// model's is the fallback for a caller building the row before it.
	createdAt := row.CreatedAt
	if createdAt.IsZero() {
		createdAt = record.CreatedAt()
	}
	return &UsageTrace{
		UsageRecordID: row.ID,
		OrgID:         row.OrgID,
		UserID:        row.UserID,
		CreatedAt:     createdAt,
		Spans:         data,
		Truncated:     trace.Truncated,
	}, nil
}

func (r *UsageTrace) toModel() (*model.UsageTrace, error) {
	spans, err := decodeSpans(r.Spans)
	if err != nil {
		return nil, err
	}
	return &model.UsageTrace{Spans: spans, Truncated: r.Truncated}, nil
}

// encodeSpans returns the stored form of spans.
func encodeSpans(spans []model.TraceSpan) (string, error) {
	rows := make([]traceSpanRow, len(spans))
	for i, span := range spans {
		rows[i] = traceSpanRow{
			Kind:       string(span.Kind),
			Name:       span.Name,
			Parent:     span.Parent,
			StartUs:    span.Start.Microseconds(),
			DurationUs: span.Duration.Microseconds(),
			Status:     string(span.Status),
			HTTPStatus: span.HTTPStatus,
			ErrorClass: span.ErrorClass,
			Attributes: span.Attributes,
		}
	}
	data, err := json.Marshal(rows)
	if err != nil {
		return "", errors.WithStack(err)
	}
	return string(data), nil
}

// decodeSpans reads back the spans encodeSpans stored.
func decodeSpans(data string) ([]model.TraceSpan, error) {
	var rows []traceSpanRow
	if err := json.Unmarshal([]byte(data), &rows); err != nil {
		return nil, errors.Wrap(err, "could not decode trace spans")
	}
	spans := make([]model.TraceSpan, len(rows))
	for i, span := range rows {
		spans[i] = model.TraceSpan{
			Kind:       model.TraceSpanKind(span.Kind),
			Name:       span.Name,
			Parent:     span.Parent,
			Start:      time.Duration(span.StartUs) * time.Microsecond,
			Duration:   time.Duration(span.DurationUs) * time.Microsecond,
			Status:     model.TraceSpanStatus(span.Status),
			HTTPStatus: span.HTTPStatus,
			ErrorClass: span.ErrorClass,
			Attributes: span.Attributes,
		}
	}
	return spans, nil
}

// GetUsageTrace implements port.UsageStore.
func (s *Store) GetUsageTrace(ctx context.Context, id model.UsageRecordID) (*model.UsageTrace, error) {
	var trace *model.UsageTrace
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		var row UsageTrace
		if err := db.Where("usage_record_id = ?", string(id)).First(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.WithStack(port.ErrNotFound)
			}
			return errors.WithStack(err)
		}
		var err error
		trace, err = row.toModel()
		return err
	})
	return trace, err
}

// PurgeUsageTraces implements port.UsageStore. The traces of a frozen scope
// are left to the lifecycle purge, whose guards would refuse the delete.
// Each batch is its own statement, so a large backlog never holds a long
// transaction.
func (s *Store) PurgeUsageTraces(ctx context.Context, before time.Time) (int64, error) {
	var purged int64
	for {
		if err := ctx.Err(); err != nil {
			return purged, err
		}
		var removed int64
		err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
			batch := db.Model(&UsageTrace{}).Select("usage_record_id").
				Where("created_at < ?", before).
				Where(notFrozen("usage_traces", "usage_traces")).
				Limit(purgeBatchSize)
			result := db.Where("usage_record_id IN (?)", batch).Delete(&UsageTrace{})
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
