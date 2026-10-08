package oidc

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/bornholm/go-x/slogx"
	"github.com/gorilla/sessions"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/common"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn/oauth2token"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn/oidctoken"
)

type ProviderWithJWKS struct {
	ID           string
	Label        string
	Icon         string
	DiscoveryURL string
	Issuer       string
	JWKSURL      string
	// ProvesIssuer tells that every sign-in through this provider comes from
	// Issuer, so it can match an identity declared by provisioning. It is
	// false for a provider without a genuine issuer (GitHub OAuth).
	ProvesIssuer bool
	// IntrospectionURL, ClientID and ClientSecret, when set, enable RFC 7662
	// access-token introspection for this provider (see ProvidersWithIntrospection).
	IntrospectionURL string
	ClientID         string
	ClientSecret     string
	// UserInfoURL, when set, validates opaque access tokens (when no
	// introspection endpoint is available) and enriches introspected identities
	// missing an email or display name.
	UserInfoURL string
	// RequiredScope / RequiredAudience are per-provider requirements enforced on
	// the introspection path.
	RequiredScope    string
	RequiredAudience string
}

type Handler struct {
	mux               *http.ServeMux
	sessionStore      sessions.Store
	sessionName       string
	providers         []Provider
	providersWithJWKS []ProviderWithJWKS
	resolveProvider   ProviderResolver
	sessions          port.SessionRegistry
	sessionTTL        time.Duration
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func NewHandler(sessionStore sessions.Store, funcs ...OptionFunc) *Handler {
	opts := NewOptions(funcs...)
	h := &Handler{
		mux:               http.NewServeMux(),
		sessionStore:      sessionStore,
		sessionName:       opts.SessionName,
		providers:         opts.Providers,
		providersWithJWKS: opts.ProvidersWithJWKS,
		resolveProvider:   opts.ResolveProvider,
		sessions:          opts.Sessions,
		sessionTTL:        opts.SessionTTL,
	}

	// Called by the identity provider, without cookie: it designates the
	// configured provider ID, never the goth provider of a host. Mounted apart
	// from the rest of the handler, outside the per-IP rate limiter: see
	// internal/setup/http_server.go.
	h.mux.HandleFunc("POST /providers/{provider}/backchannel-logout", h.handleBackchannelLogout)

	h.mux.HandleFunc("GET /login", h.getLoginPage)
	h.mux.Handle("GET /providers/{provider}", h.withContextProvider(http.HandlerFunc(h.handleProvider)))
	h.mux.Handle("GET /providers/{provider}/callback", h.withContextProvider(http.HandlerFunc(h.handleProviderCallback)))
	h.mux.HandleFunc("GET /logout", h.handleLogout)
	h.mux.Handle("GET /providers/{provider}/logout", h.withContextProvider(http.HandlerFunc(h.handleProviderLogout)))

	return h
}

func (h *Handler) ProvidersWithJWKS() []oidctoken.Provider {
	providers := make([]oidctoken.Provider, 0, len(h.providersWithJWKS))
	for _, p := range h.providersWithJWKS {
		providers = append(providers, oidctoken.Provider{
			ID:           p.ID,
			Label:        p.Label,
			Icon:         p.Icon,
			DiscoveryURL: p.DiscoveryURL,
			Issuer:       p.Issuer,
			JWKSURL:      p.JWKSURL,
			ProvesIssuer: p.ProvesIssuer,
		})
	}
	return providers
}

// ProvidersForTokenValidation returns, for each configured provider able to
// validate an incoming opaque access token — i.e. exposing an introspection
// endpoint (preferred) or a userinfo endpoint (fallback, e.g. Auth0) — an
// oauth2token.Provider keyed on the same provider ID used for interactive logins.
func (h *Handler) ProvidersForTokenValidation() []oauth2token.Provider {
	providers := make([]oauth2token.Provider, 0, len(h.providersWithJWKS))
	for _, p := range h.providersWithJWKS {
		hasIntrospection := p.IntrospectionURL != "" && p.ClientID != ""
		if !hasIntrospection && p.UserInfoURL == "" {
			continue
		}
		providers = append(providers, oauth2token.Provider{
			ID:               p.ID,
			Issuer:           provenIssuerOf(p),
			IntrospectionURL: p.IntrospectionURL,
			ClientID:         p.ClientID,
			ClientSecret:     p.ClientSecret,
			UserInfoURL:      p.UserInfoURL,
			RequiredScope:    p.RequiredScope,
			RequiredAudience: p.RequiredAudience,
		})
	}
	return providers
}

func provenIssuerOf(p ProviderWithJWKS) string {
	if p.ProvesIssuer {
		return p.Issuer
	}
	return ""
}

// IdentityIssuers maps each provider proving an issuer to that issuer.
func (h *Handler) IdentityIssuers() model.IdentityIssuers {
	issuers := model.IdentityIssuers{}
	for _, p := range h.providersWithJWKS {
		if p.ProvesIssuer && p.Issuer != "" {
			issuers[p.ID] = p.Issuer
		}
	}
	return issuers
}

// provenIssuer returns the issuer the provider proves, if any.
func (h *Handler) provenIssuer(providerID string) (string, bool) {
	return h.IdentityIssuers().Issuer(providerID)
}

var _ http.Handler = &Handler{}

// withContextProvider names the goth provider the request must be served by.
// gothic looks up the "provider" and ":provider" query parameters before the
// context value, so both are stripped here: otherwise any client could pick the
// provider of another host. Only then does the context value win over the
// route parameter, which is what lets a multi-tenant instance answer on a
// host-scoped provider while the route keeps the bare provider ID.
func (h *Handler) withContextProvider(next http.Handler) http.Handler {
	fn := func(w http.ResponseWriter, r *http.Request) {
		provider := r.PathValue("provider")

		if h.resolveProvider != nil {
			resolved, err := h.resolveProvider(provider, httpCtx.BaseURL(r.Context()).String())
			if err != nil {
				if errors.Is(err, ErrProviderNotFound) {
					common.HandleError(w, r, common.NewHTTPError(http.StatusNotFound))
					return
				}

				slog.ErrorContext(
					r.Context(),
					"could not resolve oidc provider",
					slog.String("provider", provider),
					slogx.Error(errors.WithStack(err)),
				)
				common.HandleError(w, r, common.NewHTTPError(http.StatusBadGateway))
				return
			}

			provider = resolved
		}

		query := r.URL.Query()
		query.Del("provider")
		query.Del(":provider")

		r = r.Clone(context.WithValue(r.Context(), "provider", provider))
		r.URL.RawQuery = query.Encode()
		next.ServeHTTP(w, r)
	}

	return http.HandlerFunc(fn)
}
