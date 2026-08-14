package context

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

const keyTenant contextKey = "tenant"

// Tenant returns the tenant the request is addressed to. It is always set on a
// request that reached a handler — the tenant middleware runs before everything
// else and answers 404 when it can not resolve one — but it returns nil rather
// than panicking so a handler exercised outside the middleware chain (tests,
// background tasks) degrades instead of crashing.
func Tenant(ctx context.Context) model.Tenant {
	tenant, ok := ctx.Value(keyTenant).(model.Tenant)
	if !ok {
		return nil
	}

	return tenant
}

// TenantID returns the identifier of the current tenant, or an empty one when
// no tenant is set. It is the form most stores and filters expect.
func TenantID(ctx context.Context) model.TenantID {
	tenant := Tenant(ctx)
	if tenant == nil {
		return ""
	}

	return tenant.ID()
}

func SetTenant(ctx context.Context, tenant model.Tenant) context.Context {
	return context.WithValue(ctx, keyTenant, tenant)
}
