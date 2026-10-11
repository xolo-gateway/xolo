package port

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

type QuotaStore interface {
	SetQuota(ctx context.Context, quota model.Quota) error
	GetQuota(ctx context.Context, scope model.QuotaScope, scopeID string) (model.Quota, error)
	// ListQuotas returns the quotas set on the given ids of one scope, in one
	// read. An id without quota is left out.
	ListQuotas(ctx context.Context, scope model.QuotaScope, scopeIDs []string) ([]model.Quota, error)
}
