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
	// ResolveEffectiveQuota merges user and org quotas, taking the minimum non-nil value at each period.
	// The second return value is the raw org quota the resolver loaded to feed the merge
	// (nil when no org quota is on file). The enforcer consumes it instead of issuing a
	// second GetQuota on the hot path (issue #82).
	ResolveEffectiveQuota(ctx context.Context, userID model.UserID, orgID model.OrgID) (*model.EffectiveQuota, model.Quota, error)
	// ResolveEffectiveQuotaForApplication merges application and org quotas, taking the minimum non-nil value at each period.
	// The second return value is the raw org quota the resolver loaded to feed the merge,
	// for the same dedup reason as ResolveEffectiveQuota (issue #82).
	ResolveEffectiveQuotaForApplication(ctx context.Context, appID model.ApplicationID, orgID model.OrgID) (*model.EffectiveQuota, model.Quota, error)
}
