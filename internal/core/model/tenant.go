package model

import (
	"time"

	"github.com/google/uuid"
)

type TenantID string

func NewTenantID() TenantID {
	return TenantID(uuid.NewString())
}

// DefaultTenantSlug is the slug of the tenant every Xolo instance owns. It is
// created by the schema migration, owns every pre-existing organization and
// user, and is the tenant transparently used when multi-tenancy is disabled.
const DefaultTenantSlug = "default"

// Tenant is the outermost isolation boundary: it owns organizations and users.
// In single-tenant mode — the default — a single tenant named after
// DefaultTenantSlug exists and is never surfaced to the user.
type Tenant interface {
	WithID[TenantID]

	Slug() string
	Name() string
	Description() string
	Active() bool
	CreatedAt() time.Time
	UpdatedAt() time.Time
}

type BaseTenant struct {
	id          TenantID
	slug        string
	name        string
	description string
	active      bool
	createdAt   time.Time
	updatedAt   time.Time
}

func (t *BaseTenant) ID() TenantID         { return t.id }
func (t *BaseTenant) Slug() string         { return t.slug }
func (t *BaseTenant) Name() string         { return t.name }
func (t *BaseTenant) Description() string  { return t.description }
func (t *BaseTenant) Active() bool         { return t.active }
func (t *BaseTenant) CreatedAt() time.Time { return t.createdAt }
func (t *BaseTenant) UpdatedAt() time.Time { return t.updatedAt }

var _ Tenant = &BaseTenant{}

func NewTenant(slug, name, description string) *BaseTenant {
	now := time.Now()
	return &BaseTenant{
		id:          NewTenantID(),
		slug:        slug,
		name:        name,
		description: description,
		active:      true,
		createdAt:   now,
		updatedAt:   now,
	}
}

type TenantOption func(*BaseTenant)

func WithTenantName(name string) TenantOption { return func(t *BaseTenant) { t.name = name } }
func WithTenantDescription(desc string) TenantOption {
	return func(t *BaseTenant) { t.description = desc }
}
func WithTenantActive(active bool) TenantOption { return func(t *BaseTenant) { t.active = active } }

// UpdateTenant copies the tenant and applies the options. The UUID is immutable.
func UpdateTenant(tenant Tenant, opts ...TenantOption) *BaseTenant {
	b := &BaseTenant{
		id:          tenant.ID(),
		slug:        tenant.Slug(),
		name:        tenant.Name(),
		description: tenant.Description(),
		active:      tenant.Active(),
		createdAt:   tenant.CreatedAt(),
		updatedAt:   time.Now(),
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

func (v *BaseTenant) SetID(id TenantID) { v.id = id }

func WithTenantSlug(slug string) TenantOption { return func(t *BaseTenant) { t.slug = slug } }
