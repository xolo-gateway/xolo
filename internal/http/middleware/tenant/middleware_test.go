package tenant_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/middleware/tenant"
)

// stubTenantStore serves the tenants declared by a test, by slug.
type stubTenantStore struct {
	port.TenantStore
	bySlug map[string]model.Tenant
	calls  int
}

func (s *stubTenantStore) GetTenantBySlug(_ context.Context, slug string) (model.Tenant, error) {
	s.calls++

	tenant, ok := s.bySlug[slug]
	if !ok {
		return nil, errors.WithStack(port.ErrNotFound)
	}

	return tenant, nil
}

func newStore(tenants ...model.Tenant) *stubTenantStore {
	bySlug := make(map[string]model.Tenant, len(tenants))
	for _, tenant := range tenants {
		bySlug[tenant.Slug()] = tenant
	}
	return &stubTenantStore{bySlug: bySlug}
}

// serve runs the middleware for a host and reports the resolved tenant.
func serve(t *testing.T, resolver *tenant.Resolver, host string) (status int, resolved model.Tenant) {
	t.Helper()

	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resolved = httpCtx.Tenant(r.Context())
	})

	notFound := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = host

	rec := httptest.NewRecorder()
	tenant.Middleware(resolver, notFound)(terminal).ServeHTTP(rec, req)

	return rec.Code, resolved
}

func TestSingleTenant(t *testing.T) {
	conf := config.Multitenancy{Enabled: false, DefaultTenantSlug: model.DefaultTenantSlug}

	t.Run("serves the default tenant whatever the host", func(t *testing.T) {
		store := newStore(model.NewTenant(model.DefaultTenantSlug, "Default", ""))
		resolver := tenant.NewResolver(store, conf)

		for _, host := range []string{"xolo.example.com", "localhost:3002", "10.0.0.1", "anything.at.all"} {
			status, resolved := serve(t, resolver, host)

			if status != http.StatusOK {
				t.Errorf("host %q: status got %d, want %d", host, status, http.StatusOK)
			}
			if resolved == nil || resolved.Slug() != model.DefaultTenantSlug {
				t.Errorf("host %q: resolved %v, want the default tenant", host, resolved)
			}
		}
	})

	t.Run("resolves the default tenant only once", func(t *testing.T) {
		store := newStore(model.NewTenant(model.DefaultTenantSlug, "Default", ""))
		resolver := tenant.NewResolver(store, conf)

		for range 3 {
			serve(t, resolver, "xolo.example.com")
		}

		if store.calls != 1 {
			t.Errorf("store calls: got %d, want 1 (the resolution is memoized)", store.calls)
		}
	})

	t.Run("a store failure is not memoized", func(t *testing.T) {
		// No default tenant: every request must retry rather than latch the
		// failure for the lifetime of the process.
		store := newStore()
		resolver := tenant.NewResolver(store, conf)

		for range 3 {
			if status, _ := serve(t, resolver, "xolo.example.com"); status != http.StatusInternalServerError {
				t.Fatalf("status: got %d, want %d", status, http.StatusInternalServerError)
			}
		}

		if store.calls != 3 {
			t.Errorf("store calls: got %d, want 3", store.calls)
		}
	})
}

func TestMultiTenant(t *testing.T) {
	conf := config.Multitenancy{
		Enabled:           true,
		HostPattern:       "{tenant}.xolo.example.com",
		DefaultTenantSlug: model.DefaultTenantSlug,
	}

	suspended := model.UpdateTenant(model.NewTenant("suspended", "Suspended", ""), model.WithTenantActive(false))

	store := newStore(
		model.NewTenant("acme", "Acme", ""),
		model.NewTenant(model.DefaultTenantSlug, "Default", ""),
		suspended,
	)
	resolver := tenant.NewResolver(store, conf)

	for name, testCase := range map[string]struct {
		host       string
		wantStatus int
		wantSlug   string
	}{
		"known subdomain":              {"acme.xolo.example.com", http.StatusOK, "acme"},
		"known subdomain with port":    {"acme.xolo.example.com:3002", http.StatusOK, "acme"},
		"uppercase host":               {"ACME.Xolo.Example.Com", http.StatusOK, "acme"},
		"default tenant is no special": {"default.xolo.example.com", http.StatusOK, model.DefaultTenantSlug},
		"unknown subdomain":            {"nope.xolo.example.com", http.StatusNotFound, ""},
		"deactivated tenant":           {"suspended.xolo.example.com", http.StatusNotFound, ""},
		"bare domain":                  {"xolo.example.com", http.StatusNotFound, ""},
		"foreign domain":               {"acme.example.org", http.StatusNotFound, ""},
		"deeper subdomain":             {"a.b.xolo.example.com", http.StatusNotFound, ""},
		"empty label":                  {".xolo.example.com", http.StatusNotFound, ""},
		"bare ip":                      {"10.0.0.1", http.StatusNotFound, ""},
	} {
		t.Run(name, func(t *testing.T) {
			status, resolved := serve(t, resolver, testCase.host)

			if status != testCase.wantStatus {
				t.Fatalf("status: got %d, want %d", status, testCase.wantStatus)
			}

			if testCase.wantSlug == "" {
				if resolved != nil {
					t.Errorf("no tenant should have been injected, got %q", resolved.Slug())
				}
				return
			}

			if resolved == nil || resolved.Slug() != testCase.wantSlug {
				t.Errorf("resolved: got %v, want %q", resolved, testCase.wantSlug)
			}
		})
	}
}
