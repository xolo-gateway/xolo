// Package tenant resolves the tenant a request is addressed to and injects it
// in the request context.
//
// It is the outermost middleware of the HTTP chain: authentication resolves a
// user within a tenant, so the tenant must be known before anything else runs.
package tenant

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
)

// Resolver turns a request into a tenant.
type Resolver struct {
	tenants port.TenantStore
	domains port.DomainStore

	defaultSlug string
	multiTenant bool

	// baseURL is the configured public base URL. In multi-tenant mode, the
	// base URL of a request keeps its scheme, port and path and takes the
	// validated domain as host.
	baseURL *url.URL

	// defaultTenant memoizes the single-tenant resolution: it never varies
	// across requests, so it is worth not hitting the store on every one. Only
	// a success is memoized — a transient store failure on the first request
	// must not disable the instance for the lifetime of the process.
	defaultMutex  sync.RWMutex
	defaultTenant model.Tenant
}

// NewResolver builds a tenant resolver from the multitenancy configuration
// and the configured public base URL. In multi-tenant mode a request host is
// routed through the persisted domains; the base URL must then be absolute.
func NewResolver(tenants port.TenantStore, domains port.DomainStore, conf config.Multitenancy, baseURL string) (*Resolver, error) {
	resolver := &Resolver{
		tenants:     tenants,
		domains:     domains,
		defaultSlug: conf.DefaultTenantSlug,
		multiTenant: conf.Enabled,
	}
	if !conf.Enabled {
		return resolver, nil
	}
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, errors.Wrapf(err, "could not parse base url %q", baseURL)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.Errorf("base url %q must be absolute (scheme and host) when multi-tenancy is enabled", baseURL)
	}
	resolver.baseURL = parsed
	return resolver, nil
}

// ErrNoTenant reports a request no tenant can be resolved for. It is answered
// with a 404: on a multi-tenant deployment, an unknown host must not reveal
// whether the instance exists at all.
var ErrNoTenant = errors.New("no tenant matches this request")

// Resolve returns the tenant addressed by the request host.
func (r *Resolver) Resolve(ctx context.Context, host string) (model.Tenant, error) {
	if !r.multiTenant {
		return r.resolveDefault(ctx)
	}

	hostname, ok := requestHostname(host)
	if !ok {
		return nil, errors.WithStack(ErrNoTenant)
	}

	domain, err := r.domains.GetDomain(ctx, hostname)
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			return nil, errors.WithStack(ErrNoTenant)
		}
		return nil, errors.WithStack(err)
	}
	if domain.Status != model.StatusActive {
		return nil, errors.WithStack(ErrNoTenant)
	}

	tenant, err := r.tenants.GetTenantByID(ctx, domain.TenantID)
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			return nil, errors.WithStack(ErrNoTenant)
		}
		return nil, errors.WithStack(err)
	}

	// A deactivated tenant is indistinguishable from an unknown one: suspending
	// a customer must not leave its login page reachable.
	if !tenant.Active() {
		return nil, errors.WithStack(ErrNoTenant)
	}

	return tenant, nil
}

// tenantBaseURL builds the public base URL of a request whose host resolved
// to a tenant. Only the normalized hostname of the request is kept: the
// scheme, the port and the path come from the configured base URL, so neither
// a client-supplied port nor the casing of the Host header can leak into a
// generated URL.
func (r *Resolver) tenantBaseURL(host string) (string, bool) {
	if !r.multiTenant {
		return "", false
	}
	hostname, ok := requestHostname(host)
	if !ok {
		return "", false
	}
	perHost := *r.baseURL
	perHost.Host = hostname
	if port := r.baseURL.Port(); port != "" {
		perHost.Host = net.JoinHostPort(hostname, port)
	}
	return perHost.String(), true
}

// requestHostname normalizes the hostname of a Host header. IP literals and
// anything else that is not a DNS hostname name no domain.
func requestHostname(host string) (string, bool) {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	hostname, err := model.NormalizeHostname(host)
	if err != nil {
		return "", false
	}
	return hostname, true
}

// resolveDefault returns the tenant every request lands on when multi-tenancy
// is disabled.
func (r *Resolver) resolveDefault(ctx context.Context) (model.Tenant, error) {
	r.defaultMutex.RLock()
	cached := r.defaultTenant
	r.defaultMutex.RUnlock()

	if cached != nil {
		return cached, nil
	}

	tenant, err := r.tenants.GetTenantBySlug(ctx, r.defaultSlug)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	r.defaultMutex.Lock()
	r.defaultTenant = tenant
	r.defaultMutex.Unlock()

	return tenant, nil
}

// Middleware injects the resolved tenant in the request context and, in
// multi-tenant mode, the base URL of the domain the request came in on, so
// links, redirects and OAuth callbacks stay on that host. notFound serves the
// requests no tenant could be resolved for, so the web UI and the API each
// answer in their own format.
func Middleware(resolver *Resolver, notFound http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			tenant, err := resolver.Resolve(ctx, r.Host)
			if err != nil {
				if errors.Is(err, ErrNoTenant) {
					notFound.ServeHTTP(w, r)
					return
				}

				slog.ErrorContext(ctx, "could not resolve tenant",
					slog.String("host", r.Host), slogx.Error(errors.WithStack(err)))
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				return
			}

			ctx = httpCtx.SetTenant(ctx, tenant)
			if baseURL, ok := resolver.tenantBaseURL(r.Host); ok {
				ctx = httpCtx.SetBaseURL(ctx, baseURL)
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
