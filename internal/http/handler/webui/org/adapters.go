package org

import (
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// updatedProviderAdapter is used when updating a provider.
type updatedProviderAdapter struct {
	id               model.ProviderID
	orgID            model.OrgID
	name             string
	pType            string
	baseURL          string
	apiKey           string
	extraAPIKeys     []model.ProviderKey
	active           bool
	currency         string
	cloudTier        int
	createdAt        time.Time
	updatedAt        time.Time
	retryConfig      *model.RetryConfig
	rateLimitConfig  *model.RateLimitConfig
	billingMode      model.BillingMode
	subscriptionPlan *model.SubscriptionPlan
	requestHeaders   *model.ProviderRequestHeaders
}

func (p *updatedProviderAdapter) ID() model.ProviderID                      { return p.id }
func (p *updatedProviderAdapter) OrgID() model.OrgID                        { return p.orgID }
func (p *updatedProviderAdapter) Name() string                              { return p.name }
func (p *updatedProviderAdapter) Type() string                              { return p.pType }
func (p *updatedProviderAdapter) BaseURL() string                           { return p.baseURL }
func (p *updatedProviderAdapter) APIKey() string                            { return p.apiKey }
func (p *updatedProviderAdapter) ExtraAPIKeys() []model.ProviderKey         { return p.extraAPIKeys }
func (p *updatedProviderAdapter) Active() bool                              { return p.active }
func (p *updatedProviderAdapter) Currency() string                          { return p.currency }
func (p *updatedProviderAdapter) CloudTier() int                            { return p.cloudTier }
func (p *updatedProviderAdapter) CreatedAt() time.Time                      { return p.createdAt }
func (p *updatedProviderAdapter) UpdatedAt() time.Time                      { return p.updatedAt }
func (p *updatedProviderAdapter) RetryConfig() *model.RetryConfig           { return p.retryConfig }
func (p *updatedProviderAdapter) RateLimitConfig() *model.RateLimitConfig   { return p.rateLimitConfig }
func (p *updatedProviderAdapter) BillingMode() model.BillingMode            { return p.billingMode }
func (p *updatedProviderAdapter) SubscriptionPlan() *model.SubscriptionPlan { return p.subscriptionPlan }
func (p *updatedProviderAdapter) RequestHeaders() *model.ProviderRequestHeaders {
	return p.requestHeaders
}

var _ model.Provider = &updatedProviderAdapter{}

// updatedLLMModelAdapter is used when updating an LLM model.
type updatedLLMModelAdapter struct {
	id                        model.LLMModelID
	providerID                model.ProviderID
	orgID                     model.OrgID
	proxyName                 string
	realModel                 string
	description               string
	kind                      model.ModelKind
	enabled                   bool
	promptCostPer1KTokens       int64
	cachedPromptCostPer1KTokens int64
	completionCostPer1KTokens   int64
	contextWindow             int64
	outputWindow              int64
	activeParams              int64
	tokensPerSecLow           float64
	tokensPerSecHigh          float64
	capabilities              model.ModelCapabilities
	createdAt                 time.Time
	updatedAt                 time.Time
	tokenLimitConfig          *model.TokenLimitConfig
	extraBody                 map[string]any
	apiType                   string
	baseURL                   string
}

func (m *updatedLLMModelAdapter) ID() model.LLMModelID         { return m.id }
func (m *updatedLLMModelAdapter) ProviderID() model.ProviderID { return m.providerID }
func (m *updatedLLMModelAdapter) OrgID() model.OrgID           { return m.orgID }
func (m *updatedLLMModelAdapter) ProxyName() string            { return m.proxyName }
func (m *updatedLLMModelAdapter) RealModel() string            { return m.realModel }
func (m *updatedLLMModelAdapter) Description() string          { return m.description }
func (m *updatedLLMModelAdapter) Kind() model.ModelKind        { return model.KindOrDefault(m.kind) }
func (m *updatedLLMModelAdapter) Enabled() bool                { return m.enabled }
func (m *updatedLLMModelAdapter) PromptCostPer1KTokens() int64 { return m.promptCostPer1KTokens }
func (m *updatedLLMModelAdapter) CachedPromptCostPer1KTokens() int64 {
	if m.cachedPromptCostPer1KTokens == 0 {
		return m.promptCostPer1KTokens
	}
	return m.cachedPromptCostPer1KTokens
}
func (m *updatedLLMModelAdapter) CompletionCostPer1KTokens() int64 {
	return m.completionCostPer1KTokens
}
func (m *updatedLLMModelAdapter) ContextWindow() int64                  { return m.contextWindow }
func (m *updatedLLMModelAdapter) OutputWindow() int64                   { return m.outputWindow }
func (m *updatedLLMModelAdapter) ActiveParams() int64                   { return m.activeParams }
func (m *updatedLLMModelAdapter) TokensPerSecLow() float64              { return m.tokensPerSecLow }
func (m *updatedLLMModelAdapter) TokensPerSecHigh() float64             { return m.tokensPerSecHigh }
func (m *updatedLLMModelAdapter) Capabilities() model.ModelCapabilities { return m.capabilities }
func (m *updatedLLMModelAdapter) CreatedAt() time.Time                  { return m.createdAt }
func (m *updatedLLMModelAdapter) UpdatedAt() time.Time                  { return m.updatedAt }
func (m *updatedLLMModelAdapter) TokenLimitConfig() *model.TokenLimitConfig {
	return m.tokenLimitConfig
}
func (m *updatedLLMModelAdapter) ExtraBody() map[string]any { return m.extraBody }
func (m *updatedLLMModelAdapter) APIType() string           { return m.apiType }
func (m *updatedLLMModelAdapter) BaseURL() string           { return m.baseURL }
func (m *updatedLLMModelAdapter) IsVirtual() bool           { return false }

var _ model.LLMModel = &updatedLLMModelAdapter{}
