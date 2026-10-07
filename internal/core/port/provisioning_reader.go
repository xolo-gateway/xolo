package port

import (
	"context"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// ProvisioningReader reads the projections and the event feed of the common
// contract. Reads never take the publication lock.
type ProvisioningReader interface {
	// ReadProjection returns ErrNotFound for a missing resource or parent.
	ReadProjection(ctx context.Context, scope model.CommonScope, key string) (model.CommonItem, error)
	// ListProjections returns ErrParentNotFound for a missing parent,
	// ErrInvalidCursor or ErrCursorExpired for an unusable cursor.
	ListProjections(ctx context.Context, scope model.CommonScope, cursor string, limit int) (model.CommonPage, error)
	// CaptureEventCursor designates the current end of the feed.
	CaptureEventCursor(ctx context.Context) (string, error)
	// ReadEvents returns the events committed after cursor, in commit order.
	ReadEvents(ctx context.Context, cursor string, limit int) (model.CommonEventPage, error)
	// PurgeEvents removes the oldest events created before the given time and
	// returns how many were removed. Cursors before them expire.
	PurgeEvents(ctx context.Context, before time.Time) (int64, error)
}
