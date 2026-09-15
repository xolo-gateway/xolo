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
	Type() string   // openai | anthropic | mistral | openrouter | yzma
	BaseURL() string
	APIKey() string // encrypted at rest, decrypted on use
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
}

type BaseProvider struct {
	id               ProviderID
	orgID            OrgID
	name             string
	pType            string
	baseURL          string
	apiKey           string
	active           bool
	currency         string
	cloudTier        int
	createdAt        time.Time
	updatedAt        time.Time
	retryConfig      *RetryConfig
	rateLimitConfig  *RateLimitConfig
	billingMode      BillingMode
	subscriptionPlan *SubscriptionPlan
}

func (p *BaseProvider) ID() ProviderID                      { return p.id }
func (p *BaseProvider) OrgID() OrgID                        { return p.orgID }
func (p *BaseProvider) Name() string                        { return p.name }
func (p *BaseProvider) Type() string                        { return p.pType }
func (p *BaseProvider) BaseURL() string                     { return p.baseURL }
func (p *BaseProvider) APIKey() string                      { return p.apiKey }
func (p *BaseProvider) Active() bool                        { return p.active }
func (p *BaseProvider) Currency() string                    { return p.currency }
func (p *BaseProvider) CloudTier() int                      { return p.cloudTier }
func (p *BaseProvider) CreatedAt() time.Time                { return p.createdAt }
func (p *BaseProvider) UpdatedAt() time.Time                { return p.updatedAt }
func (p *BaseProvider) RetryConfig() *RetryConfig           { return p.retryConfig }
func (p *BaseProvider) RateLimitConfig() *RateLimitConfig   { return p.rateLimitConfig }
func (p *BaseProvider) BillingMode() BillingMode            { return p.billingMode }
func (p *BaseProvider) SubscriptionPlan() *SubscriptionPlan { return p.subscriptionPlan }

func (p *BaseProvider) SetRetryConfig(c *RetryConfig)                   { p.retryConfig = c }
func (p *BaseProvider) SetRateLimitConfig(c *RateLimitConfig)             { p.rateLimitConfig = c }
func (p *BaseProvider) SetCloudTier(t int)                                { p.cloudTier = t }
func (p *BaseProvider) SetBillingMode(m BillingMode)                      { p.billingMode = m }
func (p *BaseProvider) SetSubscriptionPlan(plan *SubscriptionPlan)        { p.subscriptionPlan = plan }

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
