package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// WithProvisioningReader serves the projections and the event feed of the
// common contract.
func WithProvisioningReader(reader port.ProvisioningReader) ProvisioningServiceOptionFunc {
	return func(s *ProvisioningService) { s.reader = reader }
}

func (s *ProvisioningService) provisioningReader() (port.ProvisioningReader, error) {
	if s.reader == nil {
		return nil, errors.New("provisioning reads require a reader adapter")
	}
	return s.reader, nil
}

// GetCommon returns the projection of one resource; a missing resource or
// parent is ErrNotFound.
func (s *ProvisioningService) GetCommon(ctx context.Context, scope model.CommonScope, key string) (model.CommonItem, error) {
	reader, err := s.provisioningReader()
	if err != nil {
		return model.CommonItem{}, err
	}
	return reader.ReadProjection(ctx, scope, key)
}

// ListCommon returns one page of a collection.
func (s *ProvisioningService) ListCommon(ctx context.Context, scope model.CommonScope, cursor string, limit int) (model.CommonPage, error) {
	reader, err := s.provisioningReader()
	if err != nil {
		return model.CommonPage{}, err
	}
	return reader.ListProjections(ctx, scope, cursor, limit)
}

// CaptureEventCursor designates the current end of the event feed.
func (s *ProvisioningService) CaptureEventCursor(ctx context.Context) (string, error) {
	reader, err := s.provisioningReader()
	if err != nil {
		return "", err
	}
	return reader.CaptureEventCursor(ctx)
}

// ReadEvents returns the events committed after cursor, in commit order.
func (s *ProvisioningService) ReadEvents(ctx context.Context, cursor string, limit int) (model.CommonEventPage, error) {
	reader, err := s.provisioningReader()
	if err != nil {
		return model.CommonEventPage{}, err
	}
	return reader.ReadEvents(ctx, cursor, limit)
}

// PurgeEvents removes the events older than retention.
func (s *ProvisioningService) PurgeEvents(ctx context.Context, now time.Time, retention time.Duration) (int64, error) {
	reader, err := s.provisioningReader()
	if err != nil {
		return 0, err
	}
	return reader.PurgeEvents(ctx, now.Add(-retention))
}

// RunEventRetention purges the events older than retention at every
// interval, until ctx is done. Failures are logged and retried at the next
// tick: retention never stops the server.
func (s *ProvisioningService) RunEventRetention(ctx context.Context, retention, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		purged, err := s.PurgeEvents(ctx, time.Now(), retention)
		switch {
		case err != nil && ctx.Err() == nil:
			slog.ErrorContext(ctx, "provisioning event retention failed", slog.Any("error", errors.WithStack(err)))
		case purged > 0:
			slog.InfoContext(ctx, "provisioning events purged", slog.Int64("count", purged), slog.Duration("retention", retention))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
