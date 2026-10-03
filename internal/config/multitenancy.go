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
// Once enabled, persistent active domain records resolve hosts to tenants.
// A host with no active route returns 404.
type Multitenancy struct {
	Enabled bool `env:"ENABLED" envDefault:"false"`

	// HostPattern is an optional legacy hostname template. It is materialized
	// once into explicit domains during upgrade, never used for request routing.
	HostPattern string `env:"HOST_PATTERN,expand"`

	// DefaultTenantSlug names the tenant served when multi-tenancy is disabled.
	DefaultTenantSlug string `env:"DEFAULT_TENANT_SLUG" envDefault:"default"`
}

// Validate checks the shared tenant setting and any legacy migration pattern.
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
		return nil // Explicit domains do not require a hostname pattern.
	}

	if !strings.Contains(pattern, TenantHostPlaceholder) {
		return errors.Errorf("XOLO_MULTITENANCY_HOST_PATTERN must contain the %s placeholder (for instance %s.xolo.example.com)", TenantHostPlaceholder, TenantHostPlaceholder)
	}

	if strings.Count(pattern, TenantHostPlaceholder) > 1 {
		return errors.Errorf("XOLO_MULTITENANCY_HOST_PATTERN must contain the %s placeholder exactly once", TenantHostPlaceholder)
	}

	return nil
}
