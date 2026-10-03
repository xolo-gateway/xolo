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
	"time"

	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
)

// Resolver turns a request into a tenant.
type Resolver struct {
	store port.TenantStore

	domains interface {
		GetDomain(context.Context, string) (model.Domain, error)
	}
	defaultSlug      string
	multiTenant      bool
	singleTenantHost string
	baseURL          string
}

// NewResolver builds a tenant resolver from the multitenancy configuration
// and the configured public base URL. The shared host and generated URL
// scheme, port and path are controlled by configuration.
func NewResolver(store port.TenantStore, conf config.Multitenancy, baseURL string) *Resolver {
	domains, _ := store.(interface {
		GetDomain(context.Context, string) (model.Domain, error)
	})
	return &Resolver{store: store, domains: domains, defaultSlug: conf.DefaultTenantSlug, multiTenant: conf.Enabled, singleTenantHost: canonicalHostFromBaseURL(baseURL), baseURL: baseURL}

}

// ErrNoTenant reports a request no tenant can be resolved for. It is answered
// with a 404: on a multi-tenant deployment, an unknown host must not reveal
// whether the instance exists at all.
var ErrNoTenant = errors.New("no tenant matches this request")

// Resolve returns the tenant addressed by the request.
func (r *Resolver) Resolve(ctx context.Context, host string) (model.Tenant, error) {
	var tenant model.Tenant
	var err error
	normalized := strings.ToLower(stripPort(host))
	if !r.multiTenant && (r.singleTenantHost == "" || bracketIfIPv6(normalized) == r.singleTenantHost) {
		if shared, ok := r.store.(interface {
			GetSharedTenant(context.Context, string) (model.Tenant, error)
		}); ok {
			tenant, err = shared.GetSharedTenant(ctx, r.defaultSlug)
		} else {
			tenant, err = r.store.GetTenantBySlug(ctx, r.defaultSlug)
		}
	} else {
		if r.domains == nil {
			return nil, ErrNoTenant
		}
		hostname, e := model.NormalizeHostname(normalized)
		if e != nil {
			return nil, ErrNoTenant
		}
		domain, e := r.domains.GetDomain(ctx, hostname)
		if errors.Is(e, port.ErrNotFound) {
			return nil, ErrNoTenant
		}
		if e != nil {
			return nil, e
		}
		if domain.Status != model.StatusActive {
			return nil, ErrNoTenant
		}
		tenant, err = r.store.GetTenantByID(ctx, domain.TenantID)
	}
	if errors.Is(err, port.ErrNotFound) {
		return nil, ErrNoTenant
	}
	if err != nil {
		return nil, err
	}
	if !tenant.Active() {
		return nil, ErrNoTenant
	}
	return tenant, nil
}

// CanonicalHost verifies the persistent route before using a client host in a URL.
func (r *Resolver) CanonicalHost(host string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.CanonicalHostContext(ctx, host)
}
func (r *Resolver) CanonicalHostContext(ctx context.Context, host string) (string, bool) {
	if !r.multiTenant {
		return r.singleTenantHost, r.singleTenantHost != ""
	}
	if _, err := r.Resolve(ctx, host); err != nil {
		return "", false
	}
	h, err := model.NormalizeHostname(stripPort(host))
	return h, err == nil
}

// stripPort removes the ":port" suffix of a host, if any. It tolerates a host
// with no port, which net.SplitHostPort reports as an error. net.SplitHostPort
// unbrackets IPv6 literals, so callers that need to use the host as a URL
// authority must re-bracket the result themselves.
func stripPort(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// bracketIfIPv6 wraps a bare IPv6 literal (one containing ":") in square
// brackets. RFC 3986 §3.2.2 reserves colons as authority delimiters, so a
// value going into a URL host field must keep its brackets whenever the
// source did. A value already wrapped is returned unchanged.
func bracketIfIPv6(host string) string {
	if strings.HasPrefix(host, "[") {
		return host
	}
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

// canonicalHostFromBaseURL extracts the lower-cased host (port stripped) of a
// configured base URL. An empty, relative or malformed URL yields an empty
// string: the resolver then refuses to forge a canonical host in single-tenant
// mode rather than echoing the request. IPv6 literals are kept in brackets so
// the returned value is valid as a URL authority.
func canonicalHostFromBaseURL(baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return ""
	}

	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" {
		return ""
	}

	host := bracketIfIPv6(stripPort(parsed.Host))

	return strings.ToLower(host)
}

// Middleware injects the resolved tenant in the request context. notFound
// serves the requests no tenant could be resolved for, so the web UI and the
// API each answer in their own format.
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
			if base, err := url.Parse(resolver.baseURL); err == nil && base.Host != "" {
				host := resolver.singleTenantHost
				if resolver.multiTenant || bracketIfIPv6(strings.ToLower(stripPort(r.Host))) != resolver.singleTenantHost {
					host = strings.ToLower(stripPort(r.Host))
				}
				if port := base.Port(); port != "" {
					base.Host = net.JoinHostPort(strings.Trim(host, "[]"), port)
				} else {
					base.Host = bracketIfIPv6(host)
				}
				ctx = httpCtx.SetBaseURL(ctx, base.String())
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
