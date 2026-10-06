package config

import (
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/pkg/errors"
)

// ProvisionningAPI configures the instance Provisionning API. It is served on
// its own listener and port, and authenticates its clients with mutual TLS only:
// no OIDC, no session, no user token.
//
// It is disabled by default: an instance that does not need machine
// provisioning never opens that port.
type ProvisionningAPI struct {
	AuthorizedURIs  []string      `env:"AUTHORIZED_URIS" envSeparator:","`
	RateLimit       float64       `env:"RATE_LIMIT" envDefault:"10"`
	RateBurst       int           `env:"RATE_BURST" envDefault:"20"`
	Enabled         bool          `env:"ENABLED" envDefault:"false"`
	Address         string        `env:"ADDRESS,expand" envDefault:":3003"`
	TLSCertFile     string        `env:"TLS_CERT_FILE,expand"`
	TLSKeyFile      string        `env:"TLS_KEY_FILE,expand"`
	TLSClientCAFile string        `env:"TLS_CLIENT_CA_FILE,expand"`
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"10s"`
}

// Validate refuses a configuration that would enable the Provisionning API
// without the material needed to enforce mutual TLS. There is no anonymous
// fallback: an incomplete configuration is a startup failure, not a degraded mode.
func (c *ProvisionningAPI) Validate() error {
	if !c.Enabled {
		return nil
	}

	for _, required := range []struct {
		name  string
		value string
	}{
		{"XOLO_PROVISIONNING_API_TLS_CERT_FILE", c.TLSCertFile},
		{"XOLO_PROVISIONNING_API_TLS_KEY_FILE", c.TLSKeyFile},
		{"XOLO_PROVISIONNING_API_TLS_CLIENT_CA_FILE", c.TLSClientCAFile},
	} {
		if required.value == "" {
			return errors.Errorf("%s is required but not set when XOLO_PROVISIONNING_API_ENABLED is true", required.name)
		}
	}

	if c.Address == "" {
		return errors.New("XOLO_PROVISIONNING_API_ADDRESS is required but not set when XOLO_PROVISIONNING_API_ENABLED is true")
	}

	if len(c.AuthorizedURIs) == 0 {
		return errors.New("XOLO_PROVISIONNING_API_AUTHORIZED_URIS is required")
	}
	seen := map[string]bool{}
	for _, raw := range c.AuthorizedURIs {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || (u.Host == "" && u.Opaque == "" && u.Path == "") || strings.ContainsAny(raw, " \t\r\n") || seen[raw] {
			return errors.New("XOLO_PROVISIONNING_API_AUTHORIZED_URIS must contain distinct absolute URIs")
		}
		seen[raw] = true
	}
	if c.RateLimit <= 0 || math.IsNaN(c.RateLimit) || math.IsInf(c.RateLimit, 0) || c.RateBurst < 1 {
		return errors.New("XOLO_PROVISIONNING_API_RATE_LIMIT and RATE_BURST must be positive")
	}
	return nil
}
