package port

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// TenantStore persists the outermost isolation boundary. Tenant slugs are
// unique instance-wide: in multi-tenant mode they are the hostname label used
// to route a request, so two tenants can never share one.
type TenantStore interface {
	CreateTenant(ctx context.Context, tenant model.Tenant) error
	GetTenantByID(ctx context.Context, id model.TenantID) (model.Tenant, error)
	GetTenantBySlug(ctx context.Context, slug string) (model.Tenant, error)
	ListTenants(ctx context.Context, opts ListTenantsOptions) ([]model.Tenant, int64, error)
	SaveTenant(ctx context.Context, tenant model.Tenant) error

	// DeleteTenant removes the tenant and everything it owns: every
	// organization with its whole org-scoped footprint, then every user of the
	// tenant and its dependencies.
	DeleteTenant(ctx context.Context, id model.TenantID) error
}

type ListTenantsOptions struct {
	Page  *int
	Limit *int
}
