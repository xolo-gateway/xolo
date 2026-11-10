package http

import (
	"net/http"
	"time"

	"github.com/rs/cors"
)

type Options struct {
	Address string
	// ShutdownTimeout is how long in-flight requests may run once the context
	// is canceled before the server closes their connections.
	ShutdownTimeout time.Duration
	BaseURL         string
	// BaseURLResolver returns the public base URL a given request must build its
	// links and redirects against. It defaults to the static BaseURL; a
	// multi-tenant deployment overrides it so every tenant is served links on its
	// own host instead of the instance-wide one.
	BaseURLResolver func(r *http.Request) string
	Mounts          map[string]http.Handler
	Routes          map[string]http.Handler
	CORS            cors.Options

	// Middlewares wrap the whole mux, outside every mount. The first one is the
	// outermost. Tenant resolution lives here: authentication resolves a user
	// within a tenant, so the tenant has to be known before any route runs.
	Middlewares []func(http.Handler) http.Handler

	// ReadinessCheck backs the readiness probe (see health.go).
	ReadinessCheck ReadinessCheck
}

type OptionFunc func(opts *Options)

func NewOptions(funcs ...OptionFunc) *Options {
	opts := &Options{
		Address:         ":3002",
		ShutdownTimeout: 30 * time.Second,
		BaseURL:         "",
		Mounts:          map[string]http.Handler{},
		Routes:          map[string]http.Handler{},
		CORS: cors.Options{
			AllowedOrigins:   []string{"*"},
			AllowCredentials: true,
			Debug:            false,
		},
	}
	for _, fn := range funcs {
		fn(opts)
	}

	// Resolved after the options ran so the fallback closes over the final
	// BaseURL, whichever order WithBaseURL and WithBaseURLResolver were given in.
	if opts.BaseURLResolver == nil {
		opts.BaseURLResolver = func(*http.Request) string {
			return opts.BaseURL
		}
	}

	return opts
}

func WithMount(prefix string, handler http.Handler) OptionFunc {
	return func(opts *Options) {
		opts.Mounts[prefix] = handler
	}
}

// WithRoute registers a handler for an exact pattern (method + path), e.g.
// "GET /api/v1/models". Unlike WithMount, no path stripping is applied, and
// the pattern takes precedence over any prefix mount that would also match.
func WithRoute(pattern string, handler http.Handler) OptionFunc {
	return func(opts *Options) {
		opts.Routes[pattern] = handler
	}
}

func WithShutdownTimeout(timeout time.Duration) OptionFunc {
	return func(opts *Options) {
		opts.ShutdownTimeout = timeout
	}
}

func WithBaseURL(baseURL string) OptionFunc {
	return func(opts *Options) {
		opts.BaseURL = baseURL
	}
}

// WithBaseURLResolver derives the public base URL from the request instead of
// using a single instance-wide value. Set it when the same process serves
// several hostnames, so links, redirects and OAuth callbacks stay on the host
// the request came in on.
func WithBaseURLResolver(resolve func(r *http.Request) string) OptionFunc {
	return func(opts *Options) {
		opts.BaseURLResolver = resolve
	}
}

func WithAddress(addr string) OptionFunc {
	return func(opts *Options) {
		opts.Address = addr
	}
}

// WithMiddleware appends a middleware wrapping the entire server, outside every
// mount and route. They apply in declaration order, the first being outermost.
func WithMiddleware(middlewares ...func(http.Handler) http.Handler) OptionFunc {
	return func(opts *Options) {
		opts.Middlewares = append(opts.Middlewares, middlewares...)
	}
}

func WithCORS(options cors.Options) OptionFunc {
	return func(opts *Options) {
		opts.CORS = options
	}
}
