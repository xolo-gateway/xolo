package tenant_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/middleware/tenant"
)

type stubTenantStore struct {
	port.TenantStore
	tenants map[model.TenantID]model.Tenant
	domains map[string]model.Domain
	err     error
}

func (s *stubTenantStore) GetTenantByID(ctx context.Context, id model.TenantID) (model.Tenant, error) {
	if s.err != nil {
		return nil, s.err
	}
	v, ok := s.tenants[id]
	if !ok {
		return nil, port.ErrNotFound
	}
	return v, nil
}
func (s *stubTenantStore) GetTenantBySlug(ctx context.Context, slug string) (model.Tenant, error) {
	if s.err != nil {
		return nil, s.err
	}
	for _, v := range s.tenants {
		if v.Slug() == slug {
			return v, nil
		}
	}
	return nil, port.ErrNotFound
}
func (s *stubTenantStore) GetDomain(ctx context.Context, host string) (model.Domain, error) {
	if s.err != nil {
		return model.Domain{}, s.err
	}
	v, ok := s.domains[host]
	if !ok {
		return v, port.ErrNotFound
	}
	return v, nil
}
func TestPersistentTenantRouting(t *testing.T) {
	account := model.NewTenant("acme", "Acme", "")
	def := model.NewTenant("default", "Default", "")
	s := &stubTenantStore{tenants: map[model.TenantID]model.Tenant{account.ID(): account, def.ID(): def}, domains: map[string]model.Domain{"customer.example.org": {Hostname: "customer.example.org", TenantID: account.ID(), Status: model.StatusActive}}}
	resolver := tenant.NewResolver(s, config.Multitenancy{Enabled: true, HostPattern: "{tenant}.xolo.test", DefaultTenantSlug: "default"}, "https://shared.test:3002/gateway")
	var resolved model.Tenant
	var base string
	h := tenant.Middleware(resolver, http.NotFoundHandler())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resolved = httpCtx.Tenant(r.Context())
		base = httpCtx.BaseURL(r.Context()).String()
	}))
	call := func(host string) int {
		req := httptest.NewRequest("GET", "/", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	require.Equal(t, 200, call("CUSTOMER.example.org:9999"))
	require.Equal(t, account.ID(), resolved.ID())
	require.Equal(t, "https://customer.example.org:3002/gateway", base)
	host, ok := resolver.CanonicalHost("CUSTOMER.example.org:9999")
	require.True(t, ok)
	require.Equal(t, "customer.example.org", host)
	for _, host := range []string{"acme.xolo.test", "shared.test", "customer.example.org.evil", "customer.example.org.", "127.0.0.1"} {
		require.Equal(t, 404, call(host))
		_, ok := resolver.CanonicalHost(host)
		require.False(t, ok)
	}
	s.tenants[account.ID()] = model.UpdateTenant(account, model.WithTenantActive(false))
	require.Equal(t, 404, call("customer.example.org"))
	s.tenants[account.ID()] = account
	s.domains["customer.example.org"] = model.Domain{Hostname: "customer.example.org", TenantID: account.ID(), Status: model.StatusSuspended}
	require.Equal(t, 404, call("customer.example.org"))
	mono := tenant.NewResolver(s, config.Multitenancy{DefaultTenantSlug: "default"}, "https://shared.test")
	_, err := mono.Resolve(t.Context(), "shared.test:443")
	require.NoError(t, err)
	s.tenants[def.ID()] = model.UpdateTenant(def, model.WithTenantActive(false))
	_, err = mono.Resolve(t.Context(), "shared.test")
	require.ErrorIs(t, err, tenant.ErrNoTenant)
}
