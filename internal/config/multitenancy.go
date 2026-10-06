package config

import (
	"strings"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// TenantHostPlaceholder is the marker replaced by the tenant slug in
// HostPattern.
const TenantHostPlaceholder = model.TenantHostPlaceholder

// Multitenancy configures the tenant level. It is disabled by default: a
// standard instance owns a single tenant, created by the schema migration and
// never surfaced to its users — no subdomain, no UI, no change of URL.
//
// Once enabled, the tenant is identified by the request host through the
// domains declared by the provisioning API, and a host that resolves to no
// active domain is a 404.
type Multitenancy struct {
	Enabled bool `env:"ENABLED" envDefault:"false"`

	// HostPattern is the legacy hostname template, for instance
	// "{tenant}.xolo.example.com". It only serves the upgrade: on the first
	// multi-tenant startup it is expanded once into a domain per existing
	// tenant. Routing then relies on the persisted domains alone.
	HostPattern string `env:"HOST_PATTERN,expand"`

	// DefaultTenantSlug names the tenant served when multi-tenancy is disabled.
	DefaultTenantSlug string `env:"DEFAULT_TENANT_SLUG" envDefault:"default"`
}

// Validate refuses an unusable default tenant slug or legacy host pattern. An
// incomplete configuration is a startup failure, never a degraded mode.
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
		return nil
	}

	if !strings.Contains(pattern, TenantHostPlaceholder) {
		return errors.Errorf("XOLO_MULTITENANCY_HOST_PATTERN must contain the %s placeholder (for instance %s.xolo.example.com)", TenantHostPlaceholder, TenantHostPlaceholder)
	}

	if strings.Count(pattern, TenantHostPlaceholder) > 1 {
		return errors.Errorf("XOLO_MULTITENANCY_HOST_PATTERN must contain the %s placeholder exactly once", TenantHostPlaceholder)
	}

	if _, err := model.ExpandHostPattern(pattern, "tenant"); err != nil {
		return errors.Errorf("XOLO_MULTITENANCY_HOST_PATTERN %q does not expand to a valid hostname", pattern)
	}

	return nil
}
