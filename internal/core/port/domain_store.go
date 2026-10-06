package port

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// DomainStore persists the hostnames routing requests to tenants. A hostname
// belongs to at most one tenant.
type DomainStore interface {
	GetDomain(ctx context.Context, hostname string) (model.Domain, error)
	ListTenantDomains(ctx context.Context, tenantID model.TenantID) ([]model.Domain, error)
	SaveDomain(ctx context.Context, domain model.Domain) error
}
