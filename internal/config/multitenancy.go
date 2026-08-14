package config

import (
	"strings"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// TenantHostPlaceholder is the marker replaced by the tenant slug in
// HostPattern.
const TenantHostPlaceholder = "{tenant}"

// Multitenancy configures the tenant level. It is disabled by default: a
// standard instance owns a single tenant, created by the schema migration and
// never surfaced to its users — no subdomain, no UI, no change of URL.
//
// Once enabled, the tenant is identified by the request host, and a host that
// resolves to no tenant is a 404.
type Multitenancy struct {
	Enabled bool `env:"ENABLED" envDefault:"false"`

	// HostPattern is the hostname template identifying a tenant, for instance
	// "{tenant}.xolo.example.com". It may carry a port, which is ignored when
	// matching.
	HostPattern string `env:"HOST_PATTERN,expand"`

	// DefaultTenantSlug names the tenant served when multi-tenancy is disabled.
	DefaultTenantSlug string `env:"DEFAULT_TENANT_SLUG" envDefault:"default"`
}

// Validate refuses a configuration that enables multi-tenancy without a usable
// host pattern: without it no request could ever resolve a tenant, so every
// route would answer 404. An incomplete configuration is a startup failure,
// never a degraded mode.
func (c *Multitenancy) Validate() error {
	if slug := strings.TrimSpace(c.DefaultTenantSlug); slug == "" {
		return errors.New("XOLO_MULTITENANCY_DEFAULT_TENANT_SLUG can not be empty")
	} else if !model.IsValidSlug(slug) {
		return errors.Errorf("XOLO_MULTITENANCY_DEFAULT_TENANT_SLUG %q is not a valid slug", slug)
	}

	if !c.Enabled {
		return nil
	}

	pattern := strings.TrimSpace(c.HostPattern)
	if pattern == "" {
		return errors.New("XOLO_MULTITENANCY_HOST_PATTERN is required but not set when XOLO_MULTITENANCY_ENABLED is true")
	}

	if !strings.Contains(pattern, TenantHostPlaceholder) {
		return errors.Errorf("XOLO_MULTITENANCY_HOST_PATTERN must contain the %s placeholder (for instance %s.xolo.example.com)", TenantHostPlaceholder, TenantHostPlaceholder)
	}

	if strings.Count(pattern, TenantHostPlaceholder) > 1 {
		return errors.Errorf("XOLO_MULTITENANCY_HOST_PATTERN must contain the %s placeholder exactly once", TenantHostPlaceholder)
	}

	return nil
}
