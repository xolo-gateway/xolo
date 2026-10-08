package service

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// LifecycleService records the deletion of tenants, organizations and
// members. Recording a deletion freezes the resource and everything it holds
// until its purge.
type LifecycleService struct {
	store port.LifecycleStore
}

func NewLifecycleService(store port.LifecycleStore) *LifecycleService {
	return &LifecycleService{store: store}
}

// Freeze records the deletion of the resource, after checking condition
// against its current revision.
func (s *LifecycleService) Freeze(ctx context.Context, scope model.CommonScope, key string, condition model.MatchCondition) (model.Deletion, error) {
	return s.store.FreezeResource(model.EnsureActor(ctx), scope, key, condition)
}

// Read returns the deletion recorded for the resource.
func (s *LifecycleService) Read(ctx context.Context, scope model.CommonScope, key string) (model.Deletion, error) {
	return s.store.ReadDeletion(ctx, scope, key)
}
