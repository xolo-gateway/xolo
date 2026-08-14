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

	// defaultTenant memoizes the single-tenant resolution: it never varies
	// across requests, so it is worth not hitting the store on every one. Only
	// a success is memoized — a transient store failure on the first request
	// must not disable the instance for the lifetime of the process.
	defaultMutex  sync.RWMutex
	defaultTenant model.Tenant
}

func NewResolver(store port.TenantStore, conf config.Multitenancy) *Resolver {
	prefix, suffix, _ := strings.Cut(stripPort(conf.HostPattern), config.TenantHostPlaceholder)

	return &Resolver{
		store:       store,
		hostPrefix:  strings.ToLower(prefix),
		hostSuffix:  strings.ToLower(suffix),
		defaultSlug: conf.DefaultTenantSlug,
		multiTenant: conf.Enabled,
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
// with no port, which net.SplitHostPort reports as an error.
func stripPort(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
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
