package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

type LifecycleService struct {
	store               port.LifecycleStore
	retention, interval time.Duration
}

func NewLifecycleService(store port.LifecycleStore, retention, interval time.Duration) *LifecycleService {
	return &LifecycleService{store: store, retention: retention, interval: interval}
}
func (s *LifecycleService) Delete(ctx context.Context, scope model.CommonScope, key string, c model.MatchCondition) (*model.Deletion, error) {
	if scope.Family == "tenant_domain" || scope.Family == "organization_membership" {
		return nil, s.store.DeleteCommonLeaf(ctx, scope, key, c)
	}
	d, err := s.store.ScheduleDeletion(ctx, scope, key, c, s.retention)
	return &d, err
}
func (s *LifecycleService) Read(ctx context.Context, scope model.CommonScope, key string) (model.Deletion, error) {
	return s.store.ReadDeletion(ctx, scope, key)
}
func (s *LifecycleService) Export(ctx context.Context, scope model.CommonScope, key string) ([]byte, error) {
	return s.store.ExportDeletion(ctx, scope, key)
}
func (s *LifecycleService) Confirm(ctx context.Context, scope model.CommonScope, key string, c model.MatchCondition, digest string) (model.Deletion, error) {
	if !c.Present || c.Any || len(c.Tags) == 0 {
		return model.Deletion{}, port.ErrConfirmationRequired
	}
	return s.store.ConfirmDeletion(ctx, scope, key, c, digest)
}
func (s *LifecycleService) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		rows, err := s.store.DueDeletions(ctx, 100)
		if err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "could not list eligible purges")
		}
		for _, d := range rows {
			scope := model.CommonScope{Family: d.Family, TenantID: d.TenantID}
			if d.Family == "tenant" {
				scope.TenantID = ""
			}
			if err := s.store.PurgeDeletion(ctx, scope, d.ResourceID); err != nil && ctx.Err() == nil {
				slog.ErrorContext(ctx, "resource purge failed", "family", d.Family)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
