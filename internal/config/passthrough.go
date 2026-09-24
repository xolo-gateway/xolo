package config

import (
	"net/url"
	"strings"

	"github.com/pkg/errors"
)

// Passthrough configures the credential-relay surface: instead of
// authenticating upstream with a provider key it owns, Xolo forwards a
// credential supplied by the client and records the resulting token usage.
//
// It exists for clients that already hold their own upstream credential and
// want the gateway to meter their traffic without being handed a provider key.
// The motivating case is an Anthropic-compatible CLI that puts its credential
// in Authorization and offers no way to move it.
//
// Disabled by default: enabling it means the instance relays credentials it
// does not own to a third party, so it is an explicit operator decision.
type Passthrough struct {
	Enabled bool `env:"ENABLED" envDefault:"false"`

	// MountPrefix is the path the passthrough surface is served under; clients
	// point their base URL at it. Must start and end with a slash.
	MountPrefix string `env:"MOUNT_PREFIX" envDefault:"/passthrough/"`

	// UpstreamBaseURL is the origin every relayed request is forwarded to.
	UpstreamBaseURL string `env:"UPSTREAM_BASE_URL" envDefault:"https://api.anthropic.com"`

	// CredentialHeader carries the Xolo API token. The passthrough cannot use
	// Authorization for it: that header already holds the upstream credential
	// the client wants relayed. See handler/passthrough.CredentialSwap.
	CredentialHeader string `env:"CREDENTIAL_HEADER" envDefault:"X-Xolo-Key"`

	// ProviderID is the Xolo provider relayed usage is attributed to. Its models
	// supply the tariff used to price each call, exactly as for proxied traffic.
	// Its stored API key is never read — the client supplies the credential.
	ProviderID string `env:"PROVIDER_ID"`

	// AllowedPaths are the upstream paths the relay accepts, after the mount
	// prefix is stripped. Anything else is refused, so an operator enabling the
	// relay for one endpoint does not implicitly open the whole upstream API.
	AllowedPaths []string `env:"ALLOWED_PATHS" envSeparator:"," envDefault:"/v1/messages"`

	// There is deliberately no timeout knob here: the relay reuses
	// XOLO_PROXY_UPSTREAM_TIMEOUT, which already bounds upstream calls for the
	// proxied path. One upstream is one timeout.
}

func (p Passthrough) Validate() error {
	if !p.Enabled {
		return nil
	}

	if p.ProviderID == "" {
		return errors.New("passthrough: PROVIDER_ID is required when the passthrough is enabled")
	}

	if p.CredentialHeader == "" {
		return errors.New("passthrough: CREDENTIAL_HEADER cannot be empty")
	}

	if !strings.HasPrefix(p.MountPrefix, "/") || !strings.HasSuffix(p.MountPrefix, "/") {
		return errors.Errorf("passthrough: MOUNT_PREFIX must start and end with a slash, got %q", p.MountPrefix)
	}

	u, err := url.Parse(p.UpstreamBaseURL)
	if err != nil {
		return errors.Wrapf(err, "passthrough: UPSTREAM_BASE_URL %q is not a valid URL", p.UpstreamBaseURL)
	}
	if u.Host == "" {
		return errors.Errorf("passthrough: UPSTREAM_BASE_URL %q has no host", p.UpstreamBaseURL)
	}
	// A relayed credential must never leave the machine in clear text. Loopback
	// is exempted so the surface stays testable against a local stub.
	if u.Scheme != "https" && !isLoopbackHost(u.Hostname()) {
		return errors.Errorf("passthrough: UPSTREAM_BASE_URL must use https, got %q", p.UpstreamBaseURL)
	}

	if len(p.AllowedPaths) == 0 {
		return errors.New("passthrough: ALLOWED_PATHS cannot be empty")
	}
	for _, path := range p.AllowedPaths {
		if !strings.HasPrefix(path, "/") {
			return errors.Errorf("passthrough: ALLOWED_PATHS entry %q must start with a slash", path)
		}
	}

	return nil
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
