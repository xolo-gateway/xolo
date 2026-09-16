package setup

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/markbates/goth"
	"github.com/markbates/goth/gothic"
	"github.com/markbates/goth/providers/gitea"
	"github.com/markbates/goth/providers/github"
	"github.com/markbates/goth/providers/google"
	"github.com/markbates/goth/providers/openidConnect"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn/oidc"
)

type OIDCDiscovery struct {
	Issuer                string `json:"issuer"`
	JWKSURI               string `json:"jwks_uri"`
	AuthURL               string `json:"authorization_endpoint"`
	TokenURL              string `json:"token_endpoint"`
	UserInfoEndpoint      string `json:"userinfo_endpoint"`
	IntrospectionEndpoint string `json:"introspection_endpoint"`
	EndSessionEndpoint    string `json:"end_session_endpoint"`
}

const oidcDiscoveryTimeout = 10 * time.Second

// oidcProviderFactory builds a goth provider bound to one callback URL. The
// callback URL is the only part of a provider that varies between tenants, so
// providers are described as factories rather than instantiated once: a
// multi-tenant instance needs one instance per hostname it serves, because a
// goth provider freezes its redirect URI at construction.
type oidcProviderFactory func(callbackURL string) (goth.Provider, error)

// oidcCallbackURL is the redirect URI a provider is registered under. It has to
// match, byte for byte, the route the handler mounts and the URI declared at
// the identity provider.
func oidcCallbackURL(baseURL string, providerID string) string {
	return fmt.Sprintf(
		"%s/auth/oidc/providers/%s/callback",
		strings.TrimRight(baseURL, "/"),
		providerID,
	)
}

func getOIDCAuthnHandlerFromConfig(ctx context.Context, conf *config.Config) (*oidc.Handler, error) {
	sessionStore, err := getSessionStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	// Configure providers

	factories := make(map[string]oidcProviderFactory)
	providers := make([]oidc.Provider, 0)
	providersWithJWKS := make([]oidc.ProviderWithJWKS, 0)
	discoveryClient := newOIDCDiscoveryHTTPClient()

	if conf.HTTP.Authn.Providers.Google.Key != "" && conf.HTTP.Authn.Providers.Google.Secret != "" {
		key := string(conf.HTTP.Authn.Providers.Google.Key)
		secret := string(conf.HTTP.Authn.Providers.Google.Secret)
		scopes := conf.HTTP.Authn.Providers.Google.Scopes

		factories["google"] = func(callbackURL string) (goth.Provider, error) {
			return google.New(key, secret, callbackURL, scopes...), nil
		}

		providers = append(providers, oidc.Provider{
			ID:    "google",
			Label: "Google",
			Icon:  "log-in",
		})

		providersWithJWKS = append(providersWithJWKS, oidc.ProviderWithJWKS{
			ID:      "google",
			Label:   "Google",
			Icon:    "log-in",
			Issuer:  "https://accounts.google.com",
			JWKSURL: "https://www.googleapis.com/oauth2/v3/certs",
		})
	}

	if conf.HTTP.Authn.Providers.Github.Key != "" && conf.HTTP.Authn.Providers.Github.Secret != "" {
		key := string(conf.HTTP.Authn.Providers.Github.Key)
		secret := string(conf.HTTP.Authn.Providers.Github.Secret)
		scopes := conf.HTTP.Authn.Providers.Github.Scopes

		factories["github"] = func(callbackURL string) (goth.Provider, error) {
			return github.New(key, secret, callbackURL, scopes...), nil
		}

		providers = append(providers, oidc.Provider{
			ID:    "github",
			Label: "Github",
			Icon:  "github",
		})

		issuer := "https://github.com"
		if conf.HTTP.BaseURL != "" && conf.HTTP.BaseURL != "/" {
			issuer = conf.HTTP.BaseURL
		}
		providersWithJWKS = append(providersWithJWKS, oidc.ProviderWithJWKS{
			ID:      "github",
			Label:   "Github",
			Icon:    "github",
			Issuer:  issuer,
			JWKSURL: "https://token.actions.githubusercontent.com/.well-known/jwks",
		})
	}

	if conf.HTTP.Authn.Providers.Gitea.Key != "" && conf.HTTP.Authn.Providers.Gitea.Secret != "" {
		factory, provider, withJWKS, err := buildGiteaProvider(ctx, discoveryClient, conf.HTTP.Authn.Providers.Gitea)
		if err != nil {
			return nil, errors.WithStack(err)
		}

		factories["gitea"] = factory
		providers = append(providers, provider)
		if withJWKS != nil {
			providersWithJWKS = append(providersWithJWKS, *withJWKS)
		}
	}

	for _, np := range conf.HTTP.Authn.OIDCProviders {
		if np.Key == "" || np.Secret == "" {
			continue
		}

		factory, provider, withJWKS, err := buildOIDCProvider(ctx, discoveryClient, np)
		if err != nil {
			return nil, errors.Wrapf(err, "could not configure oidc provider %q", np.ID)
		}

		factories[np.ID] = factory
		providers = append(providers, provider)
		if withJWKS != nil {
			providersWithJWKS = append(providersWithJWKS, *withJWKS)
		}
	}

	opts := []oidc.OptionFunc{
		oidc.WithProviders(providers...),
		oidc.WithProvidersWithJWKS(providersWithJWKS),
	}

	if err := buildStartupOIDCProviders(
		factories,
		conf.HTTP.BaseURL,
		!conf.Multitenancy.Enabled,
	); err != nil {
		return nil, errors.WithStack(err)
	}

	if conf.Multitenancy.Enabled {
		// Each tenant is served on its own hostname, so each needs its own
		// redirect URI: a provider registered once at startup would send every
		// tenant back to a single host, where its session — bound to both the
		// hostname and the tenant — could not be used.
		opts = append(opts, oidc.WithProviderResolver(newHostScopedProviders(factories).Resolve))
	}

	gothic.Store = sessionStore

	handler := oidc.NewHandler(
		sessionStore,
		opts...,
	)

	return handler, nil
}

func getRandomBytes(n int) ([]byte, error) {
	data := make([]byte, n)

	read, err := rand.Read(data)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	if read != n {
		return nil, errors.Errorf("could not read %d bytes", n)
	}

	return data, nil
}

// buildGiteaProvider configures the built-in Gitea provider, mirroring
// buildOIDCProvider so the two stay symmetric. The provider ID is fixed to
// "gitea" because it is identified by configuration slot, not by name.
//
// As of the Gitea validation hardening, Gitea requires a configured
// DiscoveryURL just like named OIDC providers: the document is downloaded
// once at startup, validated with validateOIDCDiscovery (issuer,
// authorization_endpoint, token_endpoint remain mandatory, jwks_uri is
// optional and triggers a warning when missing), and its endpoints are
// reused by every tenant-scoped instance.
//
// Operators who previously configured Gitea with only AUTH_URL / TOKEN_URL /
// PROFILE_URL must now publish a discovery document on their Gitea instance
// (https://gitea.example.com/.well-known/openid-configuration) or configure
// the equivalent AUTH_URL / TOKEN_URL and a self-hosted /.well-known/ path.
func buildGiteaProvider(
	ctx context.Context,
	discoveryClient *http.Client,
	gp config.GiteaProvider,
) (oidcProviderFactory, oidc.Provider, *oidc.ProviderWithJWKS, error) {
	key := string(gp.Key)
	secret := string(gp.Secret)
	scopes := gp.Scopes
	authURL := string(gp.AuthURL)
	tokenURL := string(gp.TokenURL)
	profileURL := string(gp.ProfileURL)
	discoveryURL := string(gp.DiscoveryURL)

	if discoveryURL == "" {
		return nil, oidc.Provider{}, nil, errors.New("gitea provider discovery url is required")
	}

	discovery, err := fetchOIDCDiscovery(ctx, discoveryClient, discoveryURL)
	if err != nil {
		return nil, oidc.Provider{}, nil, errors.WithStack(err)
	}
	if err := validateOIDCDiscovery(discovery); err != nil {
		return nil, oidc.Provider{}, nil, errors.WithStack(err)
	}

	factory := func(callbackURL string) (goth.Provider, error) {
		return gitea.NewCustomisedURL(key, secret, callbackURL, authURL, tokenURL, profileURL, scopes...), nil
	}

	provider := oidc.Provider{
		ID:    "gitea",
		Label: string(gp.Label),
		Icon:  "gitlab",
	}

	// jwks_uri is optional: a non-conformant IdP can still drive interactive
	// login. When it is absent, we boot the instance, warn the operator, and
	// leave the provider out of the JWKS registry. Note that the same slice
	// feeds both the oidctoken and oauth2token authenticators
	// (see oidc.Handler.ProvidersWithJWKS and ProvidersForTokenValidation),
	// so omitting a provider disables API-token validation against it for
	// both: there is no introspection-only path through this slice.
	if discovery.JWKSURI == "" {
		slog.WarnContext(
			ctx,
			"gitea provider discovery document has no jwks_uri; API token validation is disabled for this provider",
			slog.String("provider", "gitea"),
			slog.String("discovery_url", discoveryURL),
		)

		return factory, provider, nil, nil
	}

	withJWKS := &oidc.ProviderWithJWKS{
		ID:               "gitea",
		Label:            string(gp.Label),
		Icon:             "gitlab",
		DiscoveryURL:     discoveryURL,
		Issuer:           discovery.Issuer,
		JWKSURL:          discovery.JWKSURI,
		IntrospectionURL: discovery.IntrospectionEndpoint,
		UserInfoURL:      discovery.UserInfoEndpoint,
		ClientID:         key,
		ClientSecret:     secret,
	}

	return factory, provider, withJWKS, nil
}

// buildOIDCProvider configures a single named OIDC provider: the factory
// building its goth provider (for interactive login), its login-button
// descriptor, and its JWKS/introspection/userinfo descriptor used by the
// oidctoken and oauth2token authenticators. Discovery is mandatory and happens
// once at startup. The provider ID is reused as the goth name so it stays
// consistent across the interactive-login and API-token paths.
func buildOIDCProvider(
	ctx context.Context,
	discoveryClient *http.Client,
	np config.NamedOIDCProvider,
) (oidcProviderFactory, oidc.Provider, *oidc.ProviderWithJWKS, error) {
	discoveryURL := string(np.DiscoveryURL)
	key := string(np.Key)
	secret := string(np.Secret)

	discovery, err := fetchOIDCDiscovery(ctx, discoveryClient, discoveryURL)
	if err != nil {
		return nil, oidc.Provider{}, nil, errors.WithStack(err)
	}
	if err := validateOIDCDiscovery(discovery); err != nil {
		return nil, oidc.Provider{}, nil, errors.WithStack(err)
	}

	// The discovery document is read and validated once, here, and its
	// endpoints are reused by every tenant-scoped instance. No network request
	// is therefore needed on the first login for a newly seen tenant.
	factory := func(callbackURL string) (goth.Provider, error) {
		provider, err := openidConnect.NewCustomisedURL(
			key,
			secret,
			callbackURL,
			discovery.AuthURL,
			discovery.TokenURL,
			discovery.Issuer,
			discovery.UserInfoEndpoint,
			discovery.EndSessionEndpoint,
			np.Scopes...,
		)
		if err != nil {
			return nil, errors.WithStack(err)
		}

		return provider, nil
	}

	provider := oidc.Provider{
		ID:    np.ID,
		Label: np.Label,
		Icon:  np.Icon,
	}

	// jwks_uri is optional: a non-conformant IdP can still drive interactive
	// login. When it is absent, we boot the instance, warn the operator, and
	// leave the provider out of the JWKS registry. Note that the same slice
	// feeds both the oidctoken and oauth2token authenticators
	// (see oidc.Handler.ProvidersWithJWKS and ProvidersForTokenValidation),
	// so omitting a provider disables API-token validation against it for
	// both: there is no introspection-only path through this slice.
	if discovery.JWKSURI == "" {
		slog.WarnContext(
			ctx,
			"oidc provider discovery document has no jwks_uri; API token validation is disabled for this provider",
			slog.String("provider", np.ID),
			slog.String("discovery_url", discoveryURL),
		)

		return factory, provider, nil, nil
	}

	withJWKS := &oidc.ProviderWithJWKS{
		ID:               np.ID,
		Label:            np.Label,
		Icon:             np.Icon,
		DiscoveryURL:     discoveryURL,
		Issuer:           discovery.Issuer,
		JWKSURL:          discovery.JWKSURI,
		IntrospectionURL: discovery.IntrospectionEndpoint,
		UserInfoURL:      discovery.UserInfoEndpoint,
		ClientID:         key,
		ClientSecret:     secret,
		RequiredScope:    np.RequiredScope,
		RequiredAudience: np.RequiredAudience,
	}

	return factory, provider, withJWKS, nil
}

func newOIDCDiscoveryHTTPClient() *http.Client {
	return &http.Client{Timeout: oidcDiscoveryTimeout}
}

func fetchOIDCDiscovery(
	ctx context.Context,
	client *http.Client,
	discoveryURL string,
) (*OIDCDiscovery, error) {
	if discoveryURL == "" {
		return nil, nil
	}
	if client == nil {
		return nil, errors.New("oidc discovery http client is required")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.Errorf("discovery fetch failed with status %d", resp.StatusCode)
	}

	var discovery OIDCDiscovery
	if err := json.NewDecoder(resp.Body).Decode(&discovery); err != nil {
		return nil, errors.WithStack(err)
	}

	return &discovery, nil
}

func validateOIDCDiscovery(discovery *OIDCDiscovery) error {
	if discovery == nil {
		return errors.New("oidc discovery url is required")
	}

	endpoints := []struct {
		name     string
		value    string
		required bool
	}{
		{name: "issuer", value: discovery.Issuer, required: true},
		{name: "authorization_endpoint", value: discovery.AuthURL, required: true},
		{name: "token_endpoint", value: discovery.TokenURL, required: true},
		// jwks_uri is intentionally not required: a non-conformant IdP that
		// issues opaque tokens plus introspection can still drive interactive
		// login, and the absence only disables API-token validation against
		// this provider. The caller is expected to log a startup warning.
		{name: "jwks_uri", value: discovery.JWKSURI},
		{name: "userinfo_endpoint", value: discovery.UserInfoEndpoint},
		{name: "introspection_endpoint", value: discovery.IntrospectionEndpoint},
		{name: "end_session_endpoint", value: discovery.EndSessionEndpoint},
	}

	for _, endpoint := range endpoints {
		if endpoint.value == "" {
			if endpoint.required {
				return errors.Errorf("oidc discovery document is missing %q", endpoint.name)
			}

			continue
		}

		parsed, err := url.Parse(endpoint.value)
		if err != nil {
			return errors.Wrapf(err, "oidc discovery field %q is not a valid url", endpoint.name)
		}

		isHTTP := parsed.Scheme == "http" || parsed.Scheme == "https"
		if !isHTTP || parsed.Host == "" {
			return errors.Errorf(
				"oidc discovery field %q must be an absolute http(s) url",
				endpoint.name,
			)
		}
	}

	return nil
}

// buildStartupOIDCProviders invokes every factory once during startup. In
// single-tenant mode those exact instances are registered; in multi-tenant
// mode they are discarded because each tenant needs an instance carrying its
// own callback URL. The factories themselves do little that can fail: a
// misconfigured named OIDC provider is caught earlier, by the mandatory
// discovery in buildOIDCProvider. This call keeps any factory that could fail
// from surfacing only on the first login of a tenant.
func buildStartupOIDCProviders(
	factories map[string]oidcProviderFactory,
	baseURL string,
	register bool,
) error {
	providers := make([]goth.Provider, 0, len(factories))

	for id, factory := range factories {
		provider, err := factory(oidcCallbackURL(baseURL, id))
		if err != nil {
			return errors.Wrapf(err, "could not configure oidc provider %q", id)
		}

		// Providers name themselves from their own type, and openidConnect
		// mangles custom names. Callback URLs and routes use the ID verbatim, so
		// force the name back to it.
		provider.SetName(id)
		providers = append(providers, provider)
	}

	if register {
		goth.UseProviders(providers...)
	}

	return nil
}
