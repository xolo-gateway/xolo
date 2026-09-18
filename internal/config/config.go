package config

import (
	"encoding/hex"
	"net/url"
	"os"
	"strings"

	"github.com/caarlos0/env/v11"
	"github.com/pkg/errors"
)

type Config struct {
	Logger           Logger             `envPrefix:"LOGGER_"`
	HTTP             HTTP               `envPrefix:"HTTP_"`
	Storage          Storage            `envPrefix:"STORAGE_"`
	TaskRunner       TaskRunner         `envPrefix:"TASK_RUNNER_"`
	ExchangeRate     ExchangeRateConfig `envPrefix:"EXCHANGE_RATE_"`
	Plugins          PluginsConfig      `envPrefix:"PLUGINS_"`
	Proxy            ProxyConfig        `envPrefix:"PROXY_"`
	Events           EventsConfig       `envPrefix:"EVENTS_"`
	ProvisionningAPI ProvisionningAPI   `envPrefix:"PROVISIONNING_API_"`
	Multitenancy     Multitenancy       `envPrefix:"MULTITENANCY_"`
	// SecretKey is a 32-byte hex string used for AES-GCM encryption of provider API keys.
	SecretKey string `env:"SECRET_KEY"`
}

func Parse() (*Config, error) {
	conf, err := env.ParseAsWithOptions[Config](env.Options{
		Prefix: "XOLO_",
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	providers, err := parseOIDCProviders()
	if err != nil {
		return nil, errors.WithStack(err)
	}
	conf.HTTP.Authn.OIDCProviders = providers

	if err := conf.Validate(); err != nil {
		return nil, errors.WithStack(err)
	}

	return &conf, nil
}

// parseOIDCProviders builds the list of named OIDC providers. The env library
// has no slice-of-struct support, so the list is encoded as a CSV of IDs in
// XOLO_HTTP_AUTHN_OIDC_PROVIDERS, each provider's fields living under the prefix
// XOLO_HTTP_AUTHN_OIDC_PROVIDER_<ID>_ (ID upper-cased). For backward
// compatibility, when no list is set but the legacy single OIDC slot
// (XOLO_HTTP_AUTHN_PROVIDERS_OIDC_*) has a client key, it is exposed as a single
// provider with ID "openid-connect".
func parseOIDCProviders() ([]NamedOIDCProvider, error) {
	const idsEnv = "XOLO_HTTP_AUTHN_OIDC_PROVIDERS"

	raw := strings.TrimSpace(os.Getenv(idsEnv))
	if raw == "" {
		legacy, err := env.ParseAsWithOptions[OIDCProvider](env.Options{
			Prefix: "XOLO_HTTP_AUTHN_PROVIDERS_OIDC_",
		})
		if err != nil {
			return nil, errors.WithStack(err)
		}
		if legacy.Key == "" {
			return nil, nil
		}
		return []NamedOIDCProvider{{ID: "openid-connect", OIDCProvider: legacy}}, nil
	}

	var providers []NamedOIDCProvider
	for _, id := range strings.Split(raw, ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		prefix := "XOLO_HTTP_AUTHN_OIDC_PROVIDER_" + strings.ToUpper(id) + "_"
		pc, err := env.ParseAsWithOptions[OIDCProvider](env.Options{Prefix: prefix})
		if err != nil {
			return nil, errors.Wrapf(err, "could not parse OIDC provider %q", id)
		}
		providers = append(providers, NamedOIDCProvider{ID: id, OIDCProvider: pc})
	}

	return providers, nil
}

// secretKeyBytes is the key length AES-GCM is used with throughout the
// codebase. Anything shorter silently downgrades the cipher; anything invalid
// only surfaces at the first encryption, long after start-up.
const secretKeyBytes = 32

// validateSecretKey asserts the secret key decodes to exactly the key length
// the AES-GCM helpers expect.
func validateSecretKey(secretKey string) error {
	if secretKey == "" {
		return errors.New("XOLO_SECRET_KEY is required but not set (must be a 32-byte hex string, e.g. generated with: openssl rand -hex 32)")
	}

	key, err := hex.DecodeString(secretKey)
	if err != nil {
		return errors.New("XOLO_SECRET_KEY must be a hex-encoded string (e.g. generated with: openssl rand -hex 32)")
	}

	if len(key) != secretKeyBytes {
		return errors.Errorf("XOLO_SECRET_KEY must decode to %d bytes, got %d (e.g. generated with: openssl rand -hex 32)", secretKeyBytes, len(key))
	}

	return nil
}

func (c *Config) Validate() error {
	// Normalize XOLO_HTTP_BASE_URL once so the value that gets validated is
	// exactly the value every downstream consumer uses (http.WithBaseURL,
	// newTenantBaseURLResolver, oidcCallbackURL). Without this, a stray space
	// picked up from a .env file or a ConfigMap would clear config validation
	// here and then blow up further down — in multi-tenant mode either with
	// "could not parse base url" (when net/url rejects the input) or with
	// "must be absolute (scheme and host) when multi-tenancy is enabled"
	// (when net/url accepts it but with empty Scheme/Host), and in
	// single-tenant mode — where the absolute-URL check is skipped — silently
	// propagating into every generated redirect_uri. See issue #28.
	c.HTTP.BaseURL = normalizeBaseURL(c.HTTP.BaseURL)

	if err := validateSecretKey(c.SecretKey); err != nil {
		return errors.WithStack(err)
	}

	if err := c.ProvisionningAPI.Validate(); err != nil {
		return errors.WithStack(err)
	}

	if err := c.Multitenancy.Validate(); err != nil {
		return errors.WithStack(err)
	}

	if err := validateMultitenantBaseURL(c.HTTP.BaseURL, c.Multitenancy.Enabled); err != nil {
		return errors.WithStack(err)
	}

	return nil
}

// normalizeBaseURL trims surrounding whitespace and a trailing slash so the
// stored value is independent of how it was written in the environment.
//
// The whitespace trim is the fix for issue #28: a stray space picked up from
// a .env file or a ConfigMap would otherwise clear Validate() and then fail
// downstream in newTenantBaseURLResolver (or, in single-tenant mode, leak
// into every generated URL).
//
// The trailing-slash trim is a small, related normalization that goes beyond
// issue #28's stated scope: a trailing '/' would otherwise leak into every
// URL built from the base (see oidcCallbackURL, which had to defensively
// re-trim it).
//
// The return "/" fallback covers three reachable inputs:
//   - envDefault "/", the value XOLO_HTTP_BASE_URL defaults to;
//   - whitespace-only inputs (" ", a lone newline, a stray ConfigMap line);
//   - XOLO_HTTP_BASE_URL='${VAR}' with VAR unset. caarlos0/env v11.3.1
//     expands the env tag after substituting envDefault, so an unset
//     expansion delivers "" here rather than "/" — without the fallback,
//     url.Parse("").JoinPath(...) drops the leading slash and the browser
//     resolves redirects/invite links against the current request directory
//     instead of the root. No production code builds config.Config with a
//     literal "" BaseURL, so the fallback is uniformly safe.
func normalizeBaseURL(raw string) string {
	if cleaned := strings.TrimRight(strings.TrimSpace(raw), "/"); cleaned != "" {
		return cleaned
	}
	return "/"
}

// validateMultitenantBaseURL enforces the one cross-section rule multi-tenancy
// adds: the base URL must be absolute. In single-tenant mode a relative value
// is fine — the whole instance lives on one host and its links can stay
// relative. In multi-tenant mode it is the template every tenant URL is derived
// from, scheme included, and it is what OAuth callbacks are built on; a
// relative value would produce callbacks no identity provider can return to.
//
// The caller is expected to have run the value through normalizeBaseURL first;
// the absolute-URL check therefore operates on the canonical form. Whitespace
// trimming that used to live here has been moved to normalizeBaseURL at the
// top of Validate so the same canonical form reaches every consumer
// (http.WithBaseURL, newTenantBaseURLResolver, oidcCallbackURL) — do not add
// a defensive TrimSpace here, it would silently duplicate work and obscure
// the single-source-of-truth contract.
func validateMultitenantBaseURL(baseURL string, multitenancyEnabled bool) error {
	if !multitenancyEnabled {
		return nil
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return errors.Errorf("XOLO_HTTP_BASE_URL %q is not a valid URL", baseURL)
	}

	if parsed.Scheme == "" || parsed.Host == "" {
		return errors.Errorf("XOLO_HTTP_BASE_URL must be absolute (for instance https://xolo.example.com) when XOLO_MULTITENANCY_ENABLED is true, got %q", baseURL)
	}

	return nil
}
