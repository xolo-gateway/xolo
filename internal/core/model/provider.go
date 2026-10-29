package model

import (
	"time"

	"github.com/rs/xid"
)

type ProviderID string

func NewProviderID() ProviderID {
	return ProviderID(xid.New().String())
}

type Provider interface {
	WithID[ProviderID]

	OrgID() OrgID
	Name() string
	Type() string // one of ProviderTypes()
	BaseURL() string
	APIKey() string // encrypted at rest, decrypted on use
	// ExtraAPIKeys are the keys the provider holds besides its own, see
	// ProviderKey. Empty for most providers.
	ExtraAPIKeys() []ProviderKey
	Active() bool
	Currency() string
	// CloudTier indicates the infrastructure tier (0=Hyperscaler, 1=MajorCloud, 2=SmallProvider).
	CloudTier() int
	CreatedAt() time.Time
	UpdatedAt() time.Time
	RetryConfig() *RetryConfig
	RateLimitConfig() *RateLimitConfig
	BillingMode() BillingMode
	SubscriptionPlan() *SubscriptionPlan
	// RequestHeaders is nil when the provider needs no extra header.
	RequestHeaders() *ProviderRequestHeaders
}

type BaseProvider struct {
	id               ProviderID
	orgID            OrgID
	name             string
	pType            string
	baseURL          string
	apiKey           string
	extraAPIKeys     []ProviderKey
	active           bool
	currency         string
	cloudTier        int
	createdAt        time.Time
	updatedAt        time.Time
	retryConfig      *RetryConfig
	rateLimitConfig  *RateLimitConfig
	billingMode      BillingMode
	subscriptionPlan *SubscriptionPlan
	requestHeaders   *ProviderRequestHeaders
}

func (p *BaseProvider) ID() ProviderID                      { return p.id }
func (p *BaseProvider) OrgID() OrgID                        { return p.orgID }
func (p *BaseProvider) Name() string                        { return p.name }
func (p *BaseProvider) Type() string                        { return p.pType }
func (p *BaseProvider) BaseURL() string                     { return p.baseURL }
func (p *BaseProvider) APIKey() string                      { return p.apiKey }
func (p *BaseProvider) ExtraAPIKeys() []ProviderKey         { return p.extraAPIKeys }
func (p *BaseProvider) Active() bool                        { return p.active }
func (p *BaseProvider) Currency() string                    { return p.currency }
func (p *BaseProvider) CloudTier() int                      { return p.cloudTier }
func (p *BaseProvider) CreatedAt() time.Time                { return p.createdAt }
func (p *BaseProvider) UpdatedAt() time.Time                { return p.updatedAt }
func (p *BaseProvider) RetryConfig() *RetryConfig           { return p.retryConfig }
func (p *BaseProvider) RateLimitConfig() *RateLimitConfig   { return p.rateLimitConfig }
func (p *BaseProvider) BillingMode() BillingMode            { return p.billingMode }
func (p *BaseProvider) SubscriptionPlan() *SubscriptionPlan { return p.subscriptionPlan }
func (p *BaseProvider) RequestHeaders() *ProviderRequestHeaders {
	return p.requestHeaders
}

// SetID gives the provider an identifier chosen by the provisioning client.
func (p *BaseProvider) SetID(id ProviderID) { p.id = id }

// SetActive enables or disables the provider.
func (p *BaseProvider) SetActive(active bool) { p.active = active }

func (p *BaseProvider) SetRetryConfig(c *RetryConfig)              { p.retryConfig = c }
func (p *BaseProvider) SetRateLimitConfig(c *RateLimitConfig)      { p.rateLimitConfig = c }
func (p *BaseProvider) SetCloudTier(t int)                         { p.cloudTier = t }
func (p *BaseProvider) SetBillingMode(m BillingMode)               { p.billingMode = m }
func (p *BaseProvider) SetSubscriptionPlan(plan *SubscriptionPlan) { p.subscriptionPlan = plan }
func (p *BaseProvider) SetExtraAPIKeys(keys []ProviderKey)         { p.extraAPIKeys = keys }

// SetRequestHeaders sets the headers sent to the provider; an empty
// configuration is stored as none.
func (p *BaseProvider) SetRequestHeaders(h *ProviderRequestHeaders) {
	if h.Empty() {
		h = nil
	}
	p.requestHeaders = h
}

// CopyProvider returns a modifiable copy of p, under the same identity.
func CopyProvider(p Provider) *BaseProvider {
	c := NewProvider(p.OrgID(), p.Name(), p.Type(), p.BaseURL(), p.APIKey(), p.Currency())
	c.id = p.ID()
	c.active = p.Active()
	c.cloudTier = p.CloudTier()
	c.createdAt = p.CreatedAt()
	c.updatedAt = p.UpdatedAt()
	c.retryConfig = p.RetryConfig()
	c.rateLimitConfig = p.RateLimitConfig()
	c.billingMode = p.BillingMode()
	c.subscriptionPlan = p.SubscriptionPlan()
	c.requestHeaders = p.RequestHeaders()
	// The keys a provider holds besides its own are secrets of this
	// organization: a copy keeps them, a bundle never carries them.
	c.extraAPIKeys = p.ExtraAPIKeys()
	return c
}

// SetType, SetBaseURL and SetCurrency change the connection settings.
func (p *BaseProvider) SetType(t string)         { p.pType = t }
func (p *BaseProvider) SetBaseURL(u string)      { p.baseURL = u }
func (p *BaseProvider) SetCurrency(c string)     { p.currency = c }
func (p *BaseProvider) SetUpdatedAt(t time.Time) { p.updatedAt = t }

var _ Provider = &BaseProvider{}

func NewProvider(orgID OrgID, name, pType, baseURL, apiKey, currency string) *BaseProvider {
	return &BaseProvider{
		id:          NewProviderID(),
		orgID:       orgID,
		name:        name,
		pType:       pType,
		baseURL:     baseURL,
		apiKey:      apiKey,
		active:      true,
		currency:    currency,
		billingMode: BillingModePayg,
		createdAt:   time.Now(),
		updatedAt:   time.Now(),
	}
}
