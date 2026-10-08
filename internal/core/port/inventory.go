package port

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// InventoryReader reads every projection of the common contract on one
// snapshot, without the publication lock.
type InventoryReader interface {
	// ReadInventory first reports the feed source and the event cursor of
	// the snapshot, then every projection, parents before children. The
	// events after that cursor are exactly the changes the snapshot misses.
	ReadInventory(ctx context.Context, start func(source, cursor string) error, item func(family string, item model.CommonItem) error) error
}
