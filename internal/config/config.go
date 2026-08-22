package config

import (
	"encoding/hex"
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
	if err := validateSecretKey(c.SecretKey); err != nil {
		return errors.WithStack(err)
	}

	if err := c.ProvisionningAPI.Validate(); err != nil {
		return errors.WithStack(err)
	}

	if err := c.Multitenancy.Validate(); err != nil {
		return errors.WithStack(err)
	}

	return nil
}
