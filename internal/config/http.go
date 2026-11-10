package config

import (
	"net/netip"
	"strings"
	"time"

	"github.com/pkg/errors"
)

type HTTP struct {
	BaseURL string `env:"BASE_URL,expand" envDefault:"/"`
	Address string `env:"ADDRESS,expand" envDefault:":3002"`
	// ShutdownTimeout bounds the graceful shutdown: on SIGTERM/SIGINT the
	// listener closes at once and in-flight requests (streamed completions
	// included) get up to this long to finish before being cut. Keep the
	// container stop grace period (docker `stop_grace_period`, systemd
	// `TimeoutStopSec`) above this value.
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT,expand" envDefault:"30s"`
	Authn           Authn         `envPrefix:"AUTHN_"`
	Session         Session       `envPrefix:"SESSION_"`
	RateLimit       HTTPRateLimit `envPrefix:"RATE_LIMIT_"`
}
type Authn struct {
	Providers       AuthProviders `envPrefix:"PROVIDERS_"`
	DefaultAdmins   []string      `env:"DEFAULT_ADMINS" envSeparator:","`
	ActiveByDefault bool          `env:"ACTIVE_BY_DEFAULT" envDefault:"false"`
	// AutoCreateUsers controls whether an identity unknown to Xolo gets an
	// account on its first successful authentication. When false, only
	// pre-provisioned identities can sign in, except the addresses listed in
	// DefaultAdmins (so a fresh instance can be bootstrapped), applications and
	// the addresses named by a pending targeted invitation. See
	// bridge.Options.AutoCreateUsers.
	AutoCreateUsers       bool          `env:"AUTO_CREATE_USERS" envDefault:"true"`
	CookiesToCheck        []string      `env:"COOKIES_TO_CHECK" envSeparator:"," envDefault:"oauth_id_token"`
	OIDCTokenExpiryLeeway time.Duration `env:"OIDCTOKEN_EXPIRY_LEEWAY" envDefault:"0"`
	OAuth2Token           OAuth2Token   `envPrefix:"OAUTH2TOKEN_"`
	// OIDCProviders is the list of named OIDC identity providers. It is not
	// parsed via struct tags (the env lib has no slice-of-struct support): it is
	// populated in config.Parse() from XOLO_HTTP_AUTHN_OIDC_PROVIDERS (CSV of
	// IDs) + per-ID env prefixes XOLO_HTTP_AUTHN_OIDC_PROVIDER_<ID>_*.
	OIDCProviders []NamedOIDCProvider `env:"-"`
}

// OAuth2Token configures authentication of API requests by validating an opaque
// OAuth2 access token against the identity provider (RFC 7662 introspection
// when available, otherwise the OIDC UserInfo endpoint). This is required for
// tokens that carry no self-contained identity (no `sub`), unlike OIDC ID
// tokens handled by the oidctoken authenticator. Per-provider scope/audience
// requirements live on each OIDC provider (see OIDCProvider).
type OAuth2Token struct {
	// Enabled turns on the opaque access-token authenticator. It only activates
	// for providers whose discovery document exposes an introspection or
	// userinfo endpoint with client credentials.
	Enabled bool `env:"ENABLED" envDefault:"false"`
	// CacheTTL bounds how long a successful validation result is cached (keyed
	// by the token). Capped further by the token's own `exp` when known. Keep it
	// short to limit the window during which a revoked token stays accepted.
	CacheTTL time.Duration `env:"CACHE_TTL" envDefault:"60s"`
	// CacheSize is the maximum number of validation results kept in memory.
	CacheSize int `env:"CACHE_SIZE" envDefault:"1024"`
}

// NamedOIDCProvider is one entry of the OIDC providers list, keyed by a stable
// ID reused as the goth provider name and the authn user provider.
type NamedOIDCProvider struct {
	ID string
	OIDCProvider
}

type Session struct {
	Keys   []string `env:"KEYS" envSeparator:","`
	Cookie Cookie   `envPrefix:"COOKIE_"`
}

type Cookie struct {
	Path     string        `env:"PATH" envDefault:"/"`
	HTTPOnly bool          `env:"HTTP_ONLY" envDefault:"true"`
	Secure   bool          `env:"SECURE" envDefault:"false"`
	MaxAge   time.Duration `env:"MAX_AGE" envDefault:"24h"`
}

type AuthProviders struct {
	Google OAuth2Provider `envPrefix:"GOOGLE_"`
	Github OAuth2Provider `envPrefix:"GITHUB_"`
	Gitea  GiteaProvider  `envPrefix:"GITEA_"`
	OIDC   OIDCProvider   `envPrefix:"OIDC_"`
}

type OAuth2Provider struct {
	Key    string   `env:"KEY"`
	Secret string   `env:"SECRET"`
	Scopes []string `env:"SCOPES" envSeparator:"," envDefault:"profile,openid,email"`
}

type OIDCProvider struct {
	OAuth2Provider
	DiscoveryURL string `env:"DISCOVERY_URL"`
	Icon         string `env:"ICON" envDefault:"log-in"`
	Label        string `env:"LABEL"`
	// RequiredScope, when set, requires an introspected access token to include
	// this scope (space-separated `scope` claim). Only enforced on the
	// introspection path — userinfo does not expose scopes.
	RequiredScope string `env:"REQUIRED_SCOPE"`
	// RequiredAudience, when set, requires an introspected access token's `aud`
	// to include this value. Only enforced on the introspection path.
	RequiredAudience string `env:"REQUIRED_AUDIENCE"`
	// AllowedAudiences, when set, requires an ID token presented as a bearer
	// token or cookie to have at least one of these values in its `aud`. Empty
	// accepts any audience from the provider's issuer.
	AllowedAudiences []string `env:"ALLOWED_AUDIENCES" envSeparator:","`
}

type GiteaProvider struct {
	OAuth2Provider
	DiscoveryURL string `env:"DISCOVERY_URL"`
	TokenURL     string `env:"TOKEN_URL"`
	AuthURL      string `env:"AUTH_URL"`
	ProfileURL   string `env:"PROFILE_URL"`
	Label        string `env:"LABEL"`
}

type HTTPRateLimit struct {
	TrustHeaders bool `env:"TRUST_HEADERS" envDefault:"true"`
	// TrustedProxies lists the addresses or CIDR ranges of the proxies in front
	// of the instance (an ingress controller, a load balancer). Set, the client
	// address is the right-most X-Forwarded-For entry outside them, which a
	// client outside them cannot forge; empty, it is the left-most entry, which
	// any client can. A range that also covers clients (a whole pod network
	// whose pods call the gateway) lets those clients forge it again: list the
	// proxies' own addresses. An IPv6 range spanning the IPv4-mapped space
	// (::/0, ::ffff:0:0/96) covers the IPv4 addresses too.
	TrustedProxies  []string      `env:"TRUSTED_PROXIES" envSeparator:","`
	RequestInterval time.Duration `env:"REQUEST_INTERVAL,expand" envDefault:"1s"`
	RequestMaxBurst int           `env:"REQUEST_MAX_BURST,expand" envDefault:"10"`
	CacheSize       int           `env:"CACHE_SIZE,expand" envDefault:"50"`
	CacheTTL        time.Duration `env:"CACHE_TTL,expand" envDefault:"1h"`
}

// ipv4MappedSpace is ::ffff:0:0/96, where IPv4 addresses live in IPv6.
var ipv4MappedSpace = netip.MustParsePrefix("::ffff:0:0/96")

// TrustedProxyPrefixes parses TrustedProxies, a bare address standing for
// itself alone.
func (r HTTPRateLimit) TrustedProxyPrefixes() ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(r.TrustedProxies))
	for _, raw := range r.TrustedProxies {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(raw); err == nil {
			// Addresses are unmapped before matching: a mapped range must be
			// unmapped too, or it would never match.
			if prefix.Addr().Is4In6() {
				if prefix.Bits() < 96 {
					return nil, errors.Errorf("XOLO_HTTP_RATE_LIMIT_TRUSTED_PROXIES: %q spans beyond the IPv4-mapped range", raw)
				}
				prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
			}
			prefixes = append(prefixes, prefix.Masked())
			// An IPv6 range spanning the whole IPv4-mapped space, such as ::/0,
			// also covers the IPv4 addresses, which are matched unmapped.
			if prefix.Addr().Is6() && prefix.Bits() <= 96 && prefix.Contains(ipv4MappedSpace.Addr()) {
				prefixes = append(prefixes, netip.PrefixFrom(netip.IPv4Unspecified(), 0))
			}
			continue
		}
		addr, err := netip.ParseAddr(raw)
		if err != nil {
			return nil, errors.Errorf("XOLO_HTTP_RATE_LIMIT_TRUSTED_PROXIES: %q is neither an address nor a CIDR range", raw)
		}
		// A zone would be dropped by the prefix and match the address on any
		// interface: refuse it rather than widen the trust silently.
		if addr.Zone() != "" {
			return nil, errors.Errorf("XOLO_HTTP_RATE_LIMIT_TRUSTED_PROXIES: %q carries a zone, which is not supported", raw)
		}
		addr = addr.Unmap()
		prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return prefixes, nil
}
