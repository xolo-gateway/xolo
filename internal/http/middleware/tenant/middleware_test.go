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

// stubTenantStore serves the tenants and domains declared by a test.
type stubTenantStore struct {
	port.TenantStore
	bySlug  map[string]model.Tenant
	domains map[string]model.Domain
	calls   int
}

func (s *stubTenantStore) GetTenantByID(_ context.Context, id model.TenantID) (model.Tenant, error) {
	for _, tenant := range s.bySlug {
		if tenant.ID() == id {
			return tenant, nil
		}
	}
	return nil, errors.WithStack(port.ErrNotFound)
}

func (s *stubTenantStore) GetDomain(_ context.Context, hostname string) (model.Domain, error) {
	domain, ok := s.domains[hostname]
	if !ok {
		return model.Domain{}, errors.WithStack(port.ErrNotFound)
	}
	return domain, nil
}

func (s *stubTenantStore) ListTenantDomains(context.Context, model.TenantID) ([]model.Domain, error) {
	return nil, nil
}

func (s *stubTenantStore) SaveDomain(context.Context, model.Domain) error { return nil }

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
	return &stubTenantStore{bySlug: bySlug, domains: map[string]model.Domain{}}
}

func newResolver(t *testing.T, store *stubTenantStore, conf config.Multitenancy, baseURL string) *tenant.Resolver {
	t.Helper()
	resolver, err := tenant.NewResolver(store, store, conf, baseURL)
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}
	return resolver
}

// serve runs the middleware for a host and reports the resolved tenant.
func serve(t *testing.T, resolver *tenant.Resolver, host string) (status int, resolved model.Tenant) {
	t.Helper()
	status, resolved, _ = serveWithBaseURL(t, resolver, host)
	return status, resolved
}

// serveWithBaseURL also reports the base URL the middleware left in context.
func serveWithBaseURL(t *testing.T, resolver *tenant.Resolver, host string) (status int, resolved model.Tenant, baseURL string) {
	t.Helper()

	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resolved = httpCtx.Tenant(r.Context())
		baseURL = httpCtx.BaseURL(r.Context()).String()
	})

	notFound := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = host
	// The HTTP server sets the configured base URL before tenant resolution.
	req = req.WithContext(httpCtx.SetBaseURL(req.Context(), "https://configured.example"))

	rec := httptest.NewRecorder()
	tenant.Middleware(resolver, notFound)(terminal).ServeHTTP(rec, req)

	return rec.Code, resolved, baseURL
}

func TestSingleTenant(t *testing.T) {
	conf := config.Multitenancy{Enabled: false, DefaultTenantSlug: model.DefaultTenantSlug}

	t.Run("serves the default tenant whatever the host", func(t *testing.T) {
		store := newStore(model.NewTenant(model.DefaultTenantSlug, "Default", ""))
		resolver := newResolver(t, store, conf, "")

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
		resolver := newResolver(t, store, conf, "")

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
		resolver := newResolver(t, store, conf, "")

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
	conf := config.Multitenancy{Enabled: true, DefaultTenantSlug: model.DefaultTenantSlug}

	acme := model.NewTenant("acme", "Acme", "")
	renamed := model.NewTenant("renamed", "Renamed", "")
	suspended := model.UpdateTenant(model.NewTenant("suspended", "Suspended", ""), model.WithTenantActive(false))
	store := newStore(acme, renamed, suspended, model.NewTenant(model.DefaultTenantSlug, "Default", ""))
	for host, domain := range map[string]model.Domain{
		"acme.xolo.example.com":        {TenantID: acme.ID(), Status: model.StatusActive},
		"llm.acme.example":             {TenantID: acme.ID(), Status: model.StatusActive},
		"old.acme.example":             {TenantID: acme.ID(), Status: model.StatusSuspended},
		"former-slug.xolo.example.com": {TenantID: renamed.ID(), Status: model.StatusActive},
		"suspended.xolo.example.com":   {TenantID: suspended.ID(), Status: model.StatusActive},
		"orphan.xolo.example.com":      {TenantID: model.NewTenantID(), Status: model.StatusActive},
	} {
		domain.Hostname = host
		store.domains[host] = domain
	}
	resolver := newResolver(t, store, conf, "https://xolo.example.com:8443/base")

	for name, testCase := range map[string]struct {
		host        string
		wantStatus  int
		wantSlug    string
		wantBaseURL string
	}{
		"declared domain":            {"acme.xolo.example.com", http.StatusOK, "acme", "https://acme.xolo.example.com:8443/base"},
		"second domain of a tenant":  {"llm.acme.example", http.StatusOK, "acme", "https://llm.acme.example:8443/base"},
		"client port is ignored":     {"acme.xolo.example.com:3002", http.StatusOK, "acme", "https://acme.xolo.example.com:8443/base"},
		"uppercase host":             {"ACME.Xolo.Example.Com", http.StatusOK, "acme", "https://acme.xolo.example.com:8443/base"},
		"domain survives a rename":   {"former-slug.xolo.example.com", http.StatusOK, "renamed", "https://former-slug.xolo.example.com:8443/base"},
		"suspended domain":           {"old.acme.example", http.StatusNotFound, "", ""},
		"deactivated tenant":         {"suspended.xolo.example.com", http.StatusNotFound, "", ""},
		"domain of a missing tenant": {"orphan.xolo.example.com", http.StatusNotFound, "", ""},
		"slug without a domain":      {"default.xolo.example.com", http.StatusNotFound, "", ""},
		"base url host":              {"xolo.example.com", http.StatusNotFound, "", ""},
		"bare ip":                    {"10.0.0.1", http.StatusNotFound, "", ""},
		"invalid host":               {"acme_xolo.example.com", http.StatusNotFound, "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			status, resolved, baseURL := serveWithBaseURL(t, resolver, testCase.host)

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
			if baseURL != testCase.wantBaseURL {
				t.Errorf("base url: got %q, want %q", baseURL, testCase.wantBaseURL)
			}
		})
	}
}

func TestNewResolverRequiresAbsoluteBaseURL(t *testing.T) {
	conf := config.Multitenancy{Enabled: true, DefaultTenantSlug: model.DefaultTenantSlug}
	for _, baseURL := range []string{"", "/", "xolo.example.com"} {
		if _, err := tenant.NewResolver(newStore(), newStore(), conf, baseURL); err == nil {
			t.Errorf("base url %q: got no error, want one", baseURL)
		}
	}
}
