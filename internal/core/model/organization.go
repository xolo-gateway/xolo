package model

import (
	"time"

	"github.com/rs/xid"
)

type OrgID string

func NewOrgID() OrgID {
	return OrgID(xid.New().String())
}

type Organization interface {
	WithID[OrgID]

	// TenantID is the owning tenant. Slugs are unique per tenant, never
	// instance-wide.
	TenantID() TenantID
	Slug() string
	Name() string
	Description() string
	Active() bool
	Currency() string
	CreatedAt() time.Time
	UpdatedAt() time.Time
	ShareQuotaEqually() bool
}

type BaseOrganization struct {
	id                  OrgID
	tenantID            TenantID
	slug                string
	name                string
	description         string
	active              bool
	currency            string
	createdAt           time.Time
	updatedAt           time.Time
	shareQuotaEqually   bool
}

func (o *BaseOrganization) ID() OrgID           { return o.id }
func (o *BaseOrganization) TenantID() TenantID   { return o.tenantID }
func (o *BaseOrganization) Slug() string         { return o.slug }
func (o *BaseOrganization) Name() string         { return o.name }
func (o *BaseOrganization) Description() string  { return o.description }
func (o *BaseOrganization) Active() bool         { return o.active }
func (o *BaseOrganization) Currency() string     { return o.currency }
func (o *BaseOrganization) CreatedAt() time.Time { return o.createdAt }
func (o *BaseOrganization) UpdatedAt() time.Time { return o.updatedAt }
func (o *BaseOrganization) ShareQuotaEqually() bool { return o.shareQuotaEqually }

var _ Organization = &BaseOrganization{}

func NewOrganization(tenantID TenantID, slug, name, description string, currency ...string) *BaseOrganization {
	cur := DefaultCurrency
	if len(currency) > 0 && currency[0] != "" {
		cur = currency[0]
	}
	return &BaseOrganization{
		id:          NewOrgID(),
		tenantID:    tenantID,
		slug:        slug,
		name:        name,
		description: description,
		active:      true,
		currency:    cur,
		createdAt:   time.Now(),
		updatedAt:   time.Now(),
	}
}

type OrgOption func(*BaseOrganization)

func WithOrgName(name string) OrgOption        { return func(o *BaseOrganization) { o.name = name } }
func WithOrgDescription(desc string) OrgOption { return func(o *BaseOrganization) { o.description = desc } }
func WithOrgActive(active bool) OrgOption      { return func(o *BaseOrganization) { o.active = active } }
func WithOrgCurrency(currency string) OrgOption { return func(o *BaseOrganization) { o.currency = currency } }
func WithOrgShareQuotaEqually(v bool) OrgOption { return func(o *BaseOrganization) { o.shareQuotaEqually = v } }

func UpdateOrganization(org Organization, opts ...OrgOption) *BaseOrganization {
	b := &BaseOrganization{
		id:                  org.ID(),
		tenantID:            org.TenantID(),
		slug:                org.Slug(),
		name:                org.Name(),
		description:         org.Description(),
		active:              org.Active(),
		currency:            org.Currency(),
		createdAt:           org.CreatedAt(),
		updatedAt:           time.Now(),
		shareQuotaEqually:   org.ShareQuotaEqually(),
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}
