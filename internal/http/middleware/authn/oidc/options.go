package oidc

import (
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn/oidc/component"
)

type Provider = component.Provider

// ErrProviderNotFound reports that a provider ID is not configured. Resolver
// errors that do not wrap this sentinel are operational failures and must not
// be presented as an unknown route.
var ErrProviderNotFound = errors.New("oidc provider not found")

// ProviderResolver returns the name the goth provider serving providerID is
// registered under for a request whose public base URL is baseURL, registering
// it if needed. An unknown provider must return an error wrapping
// ErrProviderNotFound. It exists so a multi-tenant instance can give each
// tenant host its own redirect URI; a single-tenant instance leaves it nil and
// keeps the providers registered once at startup.
type ProviderResolver func(providerID string, baseURL string) (string, error)

type Options struct {
	Sessions          port.SessionRegistry
	Providers         []component.Provider
	ProvidersWithJWKS []ProviderWithJWKS
	SessionName       string
	ResolveProvider   ProviderResolver
}

type OptionFunc func(opts *Options)

func NewOptions(funcs ...OptionFunc) *Options {
	opts := &Options{
		Providers:   make([]Provider, 0),
		SessionName: "xolo_auth_oidc",
	}

	for _, fn := range funcs {
		fn(opts)
	}

	return opts
}

func WithProviders(providers ...Provider) OptionFunc {
	return func(opts *Options) {
		opts.Providers = providers
	}
}

func WithSessionName(sessionName string) OptionFunc {
	return func(opts *Options) {
		opts.SessionName = sessionName
	}
}

// WithProviderResolver binds providers to the host a request came in on. Leave
// it unset to serve every request from the providers registered at startup.
func WithProviderResolver(resolve ProviderResolver) OptionFunc {
	return func(opts *Options) {
		opts.ResolveProvider = resolve
	}
}

func WithProvidersWithJWKS(providers []ProviderWithJWKS) OptionFunc {
	return func(opts *Options) {
		(opts).ProvidersWithJWKS = providers
	}
}

func WithSessionRegistry(registry port.SessionRegistry) OptionFunc {
	return func(o *Options) { o.Sessions = registry }
}
