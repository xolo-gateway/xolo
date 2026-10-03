package service

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

func (s *ProvisioningService) commonStore() port.CommonStore {
	return s.transactions.(port.CommonStore)
}
func (s *ProvisioningService) ReadCommon(ctx context.Context, scope model.CommonScope, key string) (model.CommonItem, error) {
	return s.commonStore().ReadCommon(ctx, scope, key)
}
func (s *ProvisioningService) ListCommon(ctx context.Context, scope model.CommonScope, cursor string, limit int) (model.CommonPage, error) {
	return s.commonStore().ListCommon(ctx, scope, cursor, limit)
}
func (s *ProvisioningService) CaptureCommonCursor(ctx context.Context) (string, error) {
	return s.commonStore().CaptureCommonCursor(ctx)
}
func (s *ProvisioningService) ReadCommonEvents(ctx context.Context, cursor string, limit int) (model.CommonEventPage, error) {
	return s.commonStore().ReadCommonEvents(ctx, cursor, limit)
}
func (s *ProvisioningService) WriteCommon(ctx context.Context, scope model.CommonScope, key string, condition model.MatchCondition, fn func(*ProvisioningService) error) (model.CommonItem, error) {
	return s.commonStore().WriteCommon(ctx, scope, key, condition, func(tx port.ProvisioningTx) error {
		bound := *s
		bound.transactions = tx.(port.ProvisioningTransaction)
		return fn(&bound)
	})
}

func (s *ProvisioningService) OwnershipPolicy() model.OwnershipPolicy {
	if p, ok := s.transactions.(interface{ OwnershipPolicy() model.OwnershipPolicy }); ok {
		return p.OwnershipPolicy()
	}
	return model.OwnershipPolicy{}.Effective()
}
