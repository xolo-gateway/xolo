package port

import (
	"context"
	"fmt"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

var (
	// ErrResourceDeleted refuses a write to a resource whose deletion is
	// recorded, or to anything it holds.
	ErrResourceDeleted = fmt.Errorf("resource deleted: %w", ErrNotAllowed)

	// ErrLifecycleDisabled refuses to record a deletion while the lifecycle
	// is disabled.
	ErrLifecycleDisabled = fmt.Errorf("lifecycle disabled: %w", ErrNotAllowed)
)

// LifecycleStore records deletions. A recorded deletion freezes the resource
// and its whole scope until it is purged.
type LifecycleStore interface {
	// FreezeResource records the deletion of a tenant, an organization or a
	// member, after checking condition against its current revision. A
	// repeated call returns the deletion already recorded.
	FreezeResource(ctx context.Context, scope model.CommonScope, key string, condition model.MatchCondition) (model.Deletion, error)
	// ReadDeletion returns the deletion recorded for a resource, ErrNotFound
	// when there is none.
	ReadDeletion(ctx context.Context, scope model.CommonScope, key string) (model.Deletion, error)
}
