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
		factory, provider, withJWKS, err := buildGiteaProvider(ctx, slog.Default(), discoveryClient, conf.HTTP.Authn.Providers.Gitea)
		if err != nil {
			return nil, errors.Wrap(err, "could not configure gitea provider")
		}

		factories["gitea"] = factory
		providers = append(providers, provider)
		// buildGiteaProvider returns nil for withJWKS on the
		// empty-DiscoveryURL path; skip the append in that case so we do
		// not register an inert descriptor that would iterate per
		// request.
		if withJWKS != nil {
			providersWithJWKS = append(providersWithJWKS, *withJWKS)
		}
	}

	for _, np := range conf.HTTP.Authn.OIDCProviders {
		if np.Key == "" || np.Secret == "" {
			continue
		}

		factory, provider, withJWKS, err := buildOIDCProvider(ctx, slog.Default(), discoveryClient, np)
		if err != nil {
			return nil, errors.Wrapf(err, "could not configure oidc provider %q", np.ID)
		}

		factories[np.ID] = factory
		providers = append(providers, provider)
		providersWithJWKS = append(providersWithJWKS, *withJWKS)
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
// DiscoveryURL is optional: when it is set, the document is downloaded once
// at startup and validated with validateOIDCDiscovery (issuer,
// authorization_endpoint, token_endpoint remain mandatory; jwks_uri is
// optional and triggers a warning when missing or unusable). When it is not
// set, the interactive-login factory is built from the static AUTH_URL /
// TOKEN_URL / PROFILE_URL configuration.
//
// The discovery-populated path returns a populated *oidc.ProviderWithJWKS so
// the JWKS registry is in scope for the introspection / UserInfo
// authenticators. The empty-DiscoveryURL path returns nil instead: every
// endpoint field would be empty (no Issuer was discovered, no JWKSURL, no
// IntrospectionEndpoint, no UserInfoEndpoint), so the entry would be
// inert for both oidctoken (skips on empty JWKSURL) and
// ProvidersForTokenValidation (skips on empty IntrospectionURL and
// UserInfoURL), and adding it would cost an iteration + a debug log per
// API token for no observable effect. The caller skips the append when
// the descriptor is nil.
func buildGiteaProvider(
	ctx context.Context,
	logger *slog.Logger,
	discoveryClient *http.Client,
	gp config.GiteaProvider,
) (oidcProviderFactory, oidc.Provider, *oidc.ProviderWithJWKS, error) {
	key := string(gp.Key)
	secret := string(gp.Secret)
	scopes := gp.Scopes
	cfgAuthURL := string(gp.AuthURL)
	cfgTokenURL := string(gp.TokenURL)
	profileURL := string(gp.ProfileURL)
	discoveryURL := string(gp.DiscoveryURL)

	// Gitea is reachable either through its discovery document or through a
	// fully populated static AUTH_URL / TOKEN_URL configuration. Refuse to
	// boot with neither: gitea.NewCustomisedURL accepts the URL set as-is and
	// the failure would only surface on the first login, as an opaque redirect
	// error against an IdP that never receives a request.
	if discoveryURL == "" && (cfgAuthURL == "" || cfgTokenURL == "") {
		return nil, oidc.Provider{}, nil, errors.New(
			"gitea provider requires either DISCOVERY_URL or both AUTH_URL and TOKEN_URL",
		)
	}

	var (
		discovery             *OIDCDiscovery
		issuer                string
		jwksURL               string
		introspectionEndpoint string
		userInfoEndpoint      string
	)

	authURL := cfgAuthURL
	tokenURL := cfgTokenURL

	if discoveryURL != "" {
		if err := requireAbsoluteHTTPURL(discoveryURL, "DISCOVERY_URL"); err != nil {
			return nil, oidc.Provider{}, nil, errors.WithStack(err)
		}
		var err error
		discovery, err = fetchOIDCDiscovery(ctx, discoveryClient, discoveryURL)
		if err != nil {
			return nil, oidc.Provider{}, nil, errors.WithStack(err)
		}
		if err := validateOIDCDiscovery(discovery); err != nil {
			return nil, oidc.Provider{}, nil, errors.WithStack(err)
		}

		issuer = discovery.Issuer
		jwksURL = tolerableJWKSURI(ctx, logger, "gitea provider", "gitea", discoveryURL, discovery.JWKSURI, discovery.IntrospectionEndpoint, discovery.UserInfoEndpoint)
		introspectionEndpoint = discovery.IntrospectionEndpoint
		userInfoEndpoint = discovery.UserInfoEndpoint

		// Discovery populated: validateOIDCDiscovery guarantees
		// authorization_endpoint and token_endpoint are non-empty absolute
		// URLs, so the discovered values always win over the static config.
		// The cfgAuthURL/cfgTokenURL fallback above only matters on the
		// empty-DiscoveryURL path (already validated).
		authURL = discovery.AuthURL
		tokenURL = discovery.TokenURL
		// PROFILE_URL stays optional in the static config: when it is empty,
		// fall back to the discovered userinfo_endpoint so an operator that
		// publishes a full OIDC document does not have to redeclare it.
		//
		// When PROFILE_URL is set on the discovery path it still has to be a
		// well-formed absolute http(s) URL — otherwise the failure surfaces
		// only at first login, as an opaque goth error against a profile
		// endpoint that never receives a request.
		if profileURL != "" {
			if err := requireAbsoluteHTTPURL(profileURL, "PROFILE_URL"); err != nil {
				return nil, oidc.Provider{}, nil, errors.WithStack(err)
			}
		} else {
			profileURL = discovery.UserInfoEndpoint
			if profileURL == "" {
				return nil, oidc.Provider{}, nil, errors.New(
					"gitea provider requires PROFILE_URL or a discovery document that publishes userinfo_endpoint",
				)
			}
		}
	} else {
		// Validate the static config first so the fallback warning below
		// reflects an actual fallback reaching boot, not a would-be fallback
		// that the very next check ends up refusing.
		if err := requireAbsoluteHTTPURL(cfgAuthURL, "AUTH_URL"); err != nil {
			return nil, oidc.Provider{}, nil, errors.WithStack(err)
		}
		if err := requireAbsoluteHTTPURL(cfgTokenURL, "TOKEN_URL"); err != nil {
			return nil, oidc.Provider{}, nil, errors.WithStack(err)
		}
		// PROFILE_URL is optional (goth treats it as a UserInfo fallback that
		// the OIDC gitea provider does not always need), but when present it
		// must be a well-formed absolute http(s) URL.
		if profileURL != "" {
			if err := requireAbsoluteHTTPURL(profileURL, "PROFILE_URL"); err != nil {
				return nil, oidc.Provider{}, nil, errors.WithStack(err)
			}
		}

		logger.WarnContext(
			ctx,
			"gitea provider has no discovery url; JWT id-token validation is disabled and interactive login falls back to the static AUTH_URL / TOKEN_URL configuration",
			slog.String("provider", "gitea"),
		)
	}

	factory := func(callbackURL string) (goth.Provider, error) {
		// gitea.NewCustomisedURL returns *Provider only: it does not surface a
		// construction error, so the boot-time validation above is what keeps a
		// misconfigured Gitea from reaching this point.
		return gitea.NewCustomisedURL(key, secret, callbackURL, authURL, tokenURL, profileURL, scopes...), nil
	}

	provider := oidc.Provider{
		ID:    "gitea",
		Label: string(gp.Label),
		Icon:  "gitlab",
	}

	// Only construct the JWKS-registry descriptor on the discovery-populated
	// path: the empty-DiscoveryURL path returns nil so the caller skips the
	// inert append (every endpoint field would be empty and every token
	// request would otherwise pay for an iteration + a debug log).
	var withJWKS *oidc.ProviderWithJWKS
	if discoveryURL != "" {
		withJWKS = &oidc.ProviderWithJWKS{
			ID:               "gitea",
			Label:            string(gp.Label),
			Icon:             "gitlab",
			DiscoveryURL:     discoveryURL,
			Issuer:           issuer,
			JWKSURL:          jwksURL,
			IntrospectionURL: introspectionEndpoint,
			UserInfoURL:      userInfoEndpoint,
			ClientID:         key,
			ClientSecret:     secret,
		}
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
	logger *slog.Logger,
	discoveryClient *http.Client,
	np config.NamedOIDCProvider,
) (oidcProviderFactory, oidc.Provider, *oidc.ProviderWithJWKS, error) {
	discoveryURL := string(np.DiscoveryURL)
	key := string(np.Key)
	secret := string(np.Secret)

	if err := requireAbsoluteHTTPURL(discoveryURL, "DISCOVERY_URL"); err != nil {
		return nil, oidc.Provider{}, nil, errors.WithStack(err)
	}
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
	// login. When it is absent or not an absolute http(s) url, tolerableJWKSURI
	// emits one startup warning naming the provider and leaves JWKSURL empty
	// on the descriptor. The provider stays in the registry, and:
	//
	//   - oidctoken.validateToken skips providers with an empty JWKSURL
	//     (returns errInvalidToken, which the authentication loop continues
	//     on), so JWT id-token validation is silently disabled here.
	//   - oauth2token.ProvidersForTokenValidation filters on
	//     IntrospectionURL / UserInfoURL directly, not on JWKSURL, so
	//     introspection / UserInfo keep working when the discovery document
	//     exposes either endpoint. This is the case issue #27 targets.
	jwksURL := tolerableJWKSURI(ctx, logger, "oidc provider", np.ID, discoveryURL, discovery.JWKSURI, discovery.IntrospectionEndpoint, discovery.UserInfoEndpoint)

	withJWKS := &oidc.ProviderWithJWKS{
		ID:               np.ID,
		Label:            np.Label,
		Icon:             np.Icon,
		DiscoveryURL:     discoveryURL,
		Issuer:           discovery.Issuer,
		JWKSURL:          jwksURL,
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

// tolerableJWKSURI returns the jwks_uri unchanged when it is a non-empty
// absolute http(s) URL, and "" otherwise. When it returns "", the supplied
// logger has emitted a startup warning so the operator can see which
// provider lost JWT id-token validation. The empty result lets the
// oidctoken loop skip the provider cleanly (errInvalidToken on every
// token) while still keeping the provider registered for introspection /
// UserInfo purposes.
//
// Production callers pass slog.Default(); tests pass a *slog.Logger
// configured with a captureHandler so they never need to mutate the
// process-global slog.Default (which races with any t.Parallel test that
// also captures logs).
//
// introspectionURL and userInfoURL are surfaced as slog attributes on the
// warning record (omitted when empty) so operators can verify whether the IdP
// actually publishes them: a missing jwks_uri only "still active" when at
// least one of those is present and the API-token authenticator that consumes
// them is enabled.
func tolerableJWKSURI(ctx context.Context, logger *slog.Logger, providerKind, providerID, discoveryURL, raw, introspectionURL, userInfoURL string) string {
	extraAttrs := make([]any, 0, 4)
	if introspectionURL != "" {
		extraAttrs = append(extraAttrs, slog.String("introspection_endpoint", introspectionURL))
	}
	if userInfoURL != "" {
		extraAttrs = append(extraAttrs, slog.String("userinfo_endpoint", userInfoURL))
	}

	if raw == "" {
		attrs := []any{
			slog.String("provider", providerID),
			slog.String("discovery_url", discoveryURL),
		}
		logger.WarnContext(
			ctx,
			providerKind+" discovery document has no jwks_uri; JWT id-token validation is disabled for this provider",
			append(attrs, extraAttrs...)...,
		)

		return ""
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		// net/url.Parse never returns a non-nil *URL alongside a non-nil
		// error, so we can return early without touching `parsed`. Keeping
		// the variable declaration avoids a shadow of `err` in the success
		// branch below.
		attrs := []any{
			slog.String("provider", providerID),
			slog.String("discovery_url", discoveryURL),
			slog.String("jwks_uri", raw),
			slog.String("parse_error", err.Error()),
		}
		logger.WarnContext(
			ctx,
			providerKind+" discovery document jwks_uri is not a parseable url; JWT id-token validation is disabled for this provider",
			append(attrs, extraAttrs...)...,
		)

		return ""
	}

	scheme := strings.ToLower(parsed.Scheme)
	if (scheme != "http" && scheme != "https") || parsed.Host == "" {
		attrs := []any{
			slog.String("provider", providerID),
			slog.String("discovery_url", discoveryURL),
			slog.String("jwks_uri", raw),
		}
		logger.WarnContext(
			ctx,
			providerKind+" discovery document jwks_uri is not an absolute http(s) url; JWT id-token validation is disabled for this provider",
			append(attrs, extraAttrs...)...,
		)

		return ""
	}

	return raw
}

// requireAbsoluteHTTPURL enforces the same absolute-http(s) shape as
// validateOIDCDiscovery on a single configuration value. The same helper
// gates AUTH_URL / TOKEN_URL / PROFILE_URL / DISCOVERY_URL on both the
// static-config and discovery-populated paths in buildGiteaProvider and
// buildOIDCProvider, so an operator gets a uniform
// "<FIELD> must be an absolute http(s) url" message regardless of which
// branch (parse failure, non-http scheme, or missing host) rejected the
// value. The underlying net/url parse error, when present, is wrapped as
// the error cause so it stays accessible via errors.Cause for debugging
// without leaking into the operator-facing sentence.
func requireAbsoluteHTTPURL(value, field string) error {
	parsed, err := url.Parse(value)
	if err != nil {
		return errors.Wrapf(
			err,
			"%s must be an absolute http(s) url",
			field,
		)
	}

	// RFC 3986 marks URL schemes case-insensitive; lowercase before compare.
	if scheme := strings.ToLower(parsed.Scheme); (scheme != "http" && scheme != "https") || parsed.Host == "" {
		return errors.Errorf("%s must be an absolute http(s) url", field)
	}

	return nil
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
		// jwks_uri is intentionally absent from this list: the caller routes it
		// through tolerableJWKSURI, which logs a startup warning and returns ""
		// for both a missing and an unusable value. Boot is never blocked on
		// jwks_uri — the only effect is that JWT id-token validation is
		// disabled, while introspection / UserInfo remain available.
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

		isHTTP := func() bool {
			switch strings.ToLower(parsed.Scheme) {
			case "http", "https":
				return true
			}
			return false
		}()
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
