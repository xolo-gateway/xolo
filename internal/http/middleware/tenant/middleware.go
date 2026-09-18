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
	store port.TenantStore

	// hostPrefix and hostSuffix frame the tenant slug inside the host. They are
	// derived once from the configured pattern: matching a host is then a
	// prefix/suffix test, with no regexp to compile per request.
	hostPrefix string
	hostSuffix string

	defaultSlug string
	multiTenant bool

	// singleTenantHost is the canonical host used in single-tenant mode. It is
	// derived from the configured base URL, never from the request: the only
	// tenant of a single-tenant deployment is served on whatever hostname the
	// operator chose, regardless of which Host header the client sends.
	singleTenantHost string

	// defaultTenant memoizes the single-tenant resolution: it never varies
	// across requests, so it is worth not hitting the store on every one. Only
	// a success is memoized — a transient store failure on the first request
	// must not disable the instance for the lifetime of the process.
	defaultMutex  sync.RWMutex
	defaultTenant model.Tenant
}

// NewResolver builds a tenant resolver from the multitenancy configuration
// and the configured public base URL. The base URL is only consulted in
// single-tenant mode, where it gives CanonicalHost a server-controlled host
// to return instead of echoing whatever Host header a client sent.
func NewResolver(store port.TenantStore, conf config.Multitenancy, baseURL string) *Resolver {
	prefix, suffix, _ := strings.Cut(stripPort(conf.HostPattern), config.TenantHostPlaceholder)

	return &Resolver{
		store:            store,
		hostPrefix:       strings.ToLower(prefix),
		hostSuffix:       strings.ToLower(suffix),
		defaultSlug:      conf.DefaultTenantSlug,
		multiTenant:      conf.Enabled,
		singleTenantHost: canonicalHostFromBaseURL(baseURL),
	}
}

// ErrNoTenant reports a request no tenant can be resolved for. It is answered
// with a 404: on a multi-tenant deployment, an unknown host must not reveal
// whether the instance exists at all.
var ErrNoTenant = errors.New("no tenant matches this request")

// Resolve returns the tenant addressed by the request.
func (r *Resolver) Resolve(ctx context.Context, host string) (model.Tenant, error) {
	if !r.multiTenant {
		return r.resolveDefault(ctx)
	}

	slug, ok := r.slugFromHost(host)
	if !ok {
		return nil, errors.WithStack(ErrNoTenant)
	}

	tenant, err := r.store.GetTenantBySlug(ctx, slug)
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

// CanonicalHost returns the normalized host designated by host, without
// querying the store: it answers "could this host name a tenant", not "does
// that tenant exist". In multi-tenant mode it extracts the tenant slug with
// the same validation used by Resolve, then rebuilds the host from the
// configured pattern — an empty or out-of-pattern input is rejected with
// ok=false. In single-tenant mode the request host is ignored entirely and
// the host of the configured base URL is returned: there is no tenant slug
// to extract, and the only safe answer is one derived from configuration
// rather than from the client. A missing or relative base URL is rejected
// with ok=false on this branch. The contract holds on both branches:
// neither a forged host, its casing, nor a client-supplied port can leak
// into a generated URL.
func (r *Resolver) CanonicalHost(host string) (string, bool) {
	if r.multiTenant {
		slug, ok := r.slugFromHost(host)
		if !ok {
			return "", false
		}

		return r.hostPrefix + slug + r.hostSuffix, true
	}

	if r.singleTenantHost == "" {
		return "", false
	}

	return r.singleTenantHost, true
}

// slugFromHost extracts the tenant slug framed by the configured pattern.
func (r *Resolver) slugFromHost(host string) (string, bool) {
	host = strings.ToLower(stripPort(host))

	if len(host) <= len(r.hostPrefix)+len(r.hostSuffix) {
		return "", false
	}
	if !strings.HasPrefix(host, r.hostPrefix) || !strings.HasSuffix(host, r.hostSuffix) {
		return "", false
	}

	slug := host[len(r.hostPrefix) : len(host)-len(r.hostSuffix)]

	// A subdomain label is a DNS label, which is exactly what a slug is: a host
	// carrying anything else can not designate a tenant, and rejecting it here
	// keeps the value out of the store query.
	if !model.IsValidSlug(slug) {
		return "", false
	}

	return slug, true
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

	tenant, err := r.store.GetTenantBySlug(ctx, r.defaultSlug)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	r.defaultMutex.Lock()
	r.defaultTenant = tenant
	r.defaultMutex.Unlock()

	return tenant, nil
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

	host := stripPort(parsed.Host)
	if strings.HasPrefix(parsed.Host, "[") && !strings.HasPrefix(host, "[") {
		// stripPort (via net.SplitHostPort) removed the brackets — re-add them
		// so the result stays a valid URL authority.
		host = "[" + host + "]"
	} else {
		host = bracketIfIPv6(host)
	}

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

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
