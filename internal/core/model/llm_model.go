package model

import (
	"errors"
	"time"

	"github.com/rs/xid"
)

type LLMModelID string

func NewLLMModelID() LLMModelID {
	return LLMModelID(xid.New().String())
}

// ModelCapabilities describes the features supported by a model.
type ModelCapabilities struct {
	Tools      bool // function / tool calling
	Vision     bool // image inputs
	Reasoning  bool // extended chain-of-thought / reasoning tokens
	Audio      bool // audio inputs or outputs
}

// ModelKind is what a model is called for: each kind answers its own API
// routes, and a model refuses the routes of another kind. Virtual models and
// middlewares serve completion models only, for now.
type ModelKind string

const (
	// ModelKindCompletion answers chat completions and Anthropic Messages.
	// It is the kind of every model declared before kinds existed, but the
	// ones that had the former embeddings capability.
	ModelKindCompletion ModelKind = "completion"
	// ModelKindEmbeddings answers embeddings: vectors of the inputs.
	ModelKindEmbeddings ModelKind = "embeddings"
	// ModelKindDecision answers decisions: typed questions about a state,
	// answered with calibrated probabilities (TypeSafe's Jev models).
	ModelKindDecision ModelKind = "decision"
)

// ModelKinds lists the kinds a model can have.
func ModelKinds() []ModelKind {
	return []ModelKind{ModelKindCompletion, ModelKindEmbeddings, ModelKindDecision}
}

// KindOrDefault returns k, or ModelKindCompletion when k is empty, as it is for
// the models stored before kinds existed.
func KindOrDefault(k ModelKind) ModelKind {
	if k == "" {
		return ModelKindCompletion
	}
	return k
}

// ErrNotAConversationModel is the refusal of a pipeline or plugin calling a
// model whose kind holds no conversation: a configuration mistake, not an
// outage.
var ErrNotAConversationModel = errors.New("the model holds no conversation")

// IsKnownModelKind reports whether k is one of ModelKinds.
func IsKnownModelKind(k ModelKind) bool {
	for _, known := range ModelKinds() {
		if k == known {
			return true
		}
	}
	return false
}

// LLMModel is a proxy route: what users call it → what the provider sees.
type LLMModel interface {
	WithID[LLMModelID]

	ProviderID() ProviderID
	OrgID() OrgID
	ProxyName() string // name the client sends in the API request
	RealModel() string // name forwarded to the upstream provider
	Description() string
	// Kind is what the model is called for, ModelKindCompletion when unset.
	Kind() ModelKind
	Enabled() bool
	// Costs in microcents per 1K tokens (1 microcent = $0.000001)
	PromptCostPer1KTokens() int64
	CachedPromptCostPer1KTokens() int64 // cost for tokens served from provider cache; defaults to PromptCostPer1KTokens
	CompletionCostPer1KTokens() int64
	// Context limits (0 = unknown / not set)
	ContextWindow() int64 // max input tokens
	OutputWindow() int64  // max output tokens
	// ActiveParams is the number of active parameters (0 = unknown).
	// Used for energy estimation.
	ActiveParams() int64
	// TokensPerSecLow/High : known decode throughput in tokens/s (0 = auto-estimate).
	TokensPerSecLow() float64
	TokensPerSecHigh() float64
	// Capabilities
	Capabilities() ModelCapabilities
	CreatedAt() time.Time
	UpdatedAt() time.Time
	TokenLimitConfig() *TokenLimitConfig
	// ExtraBody holds arbitrary key/values injected verbatim into every request
	// targeting this model (e.g. provider-specific flags like "reasoning_split").
	// Returns nil when none are configured.
	ExtraBody() map[string]any
	// APIType is the provider type whose API serves this model, one of
	// ProviderTypes(), or empty for the provider's own type. It lets a
	// provider serve some models through another API than its default, as
	// OpenCode Go does with OpenAI chat completions and Anthropic Messages.
	APIType() string
	// BaseURL replaces the provider's base URL for this model, or is empty to
	// keep it.
	BaseURL() string
	// IsVirtual returns true if this is a virtual model (not associated with a provider).
	IsVirtual() bool
}

type BaseLLMModel struct {
	id                           LLMModelID
	providerID                   ProviderID
	orgID                        OrgID
	proxyName                    string
	realModel                    string
	description                  string
	kind                         ModelKind
	enabled                      bool
	promptCostPer1KTokens        int64
	cachedPromptCostPer1KTokens  int64
	completionCostPer1KTokens    int64
	contextWindow             int64
	outputWindow              int64
	activeParams              int64
	tokensPerSecLow           float64
	tokensPerSecHigh          float64
	capabilities              ModelCapabilities
	createdAt                 time.Time
	updatedAt                 time.Time
	tokenLimitConfig          *TokenLimitConfig
	extraBody                 map[string]any
	apiType                   string
	baseURL                   string
}

func (m *BaseLLMModel) ID() LLMModelID                      { return m.id }
func (m *BaseLLMModel) ProviderID() ProviderID              { return m.providerID }
func (m *BaseLLMModel) OrgID() OrgID                        { return m.orgID }
func (m *BaseLLMModel) ProxyName() string                   { return m.proxyName }
func (m *BaseLLMModel) RealModel() string                   { return m.realModel }
func (m *BaseLLMModel) Description() string                 { return m.description }
func (m *BaseLLMModel) Kind() ModelKind                     { return KindOrDefault(m.kind) }
func (m *BaseLLMModel) Enabled() bool                       { return m.enabled }
func (m *BaseLLMModel) PromptCostPer1KTokens() int64 { return m.promptCostPer1KTokens }
func (m *BaseLLMModel) CachedPromptCostPer1KTokens() int64 {
	if m.cachedPromptCostPer1KTokens == 0 {
		return m.promptCostPer1KTokens
	}
	return m.cachedPromptCostPer1KTokens
}
func (m *BaseLLMModel) CompletionCostPer1KTokens() int64 { return m.completionCostPer1KTokens }
func (m *BaseLLMModel) ContextWindow() int64                { return m.contextWindow }
func (m *BaseLLMModel) OutputWindow() int64                 { return m.outputWindow }
func (m *BaseLLMModel) Capabilities() ModelCapabilities     { return m.capabilities }
func (m *BaseLLMModel) CreatedAt() time.Time                { return m.createdAt }
func (m *BaseLLMModel) UpdatedAt() time.Time                { return m.updatedAt }
func (m *BaseLLMModel) TokenLimitConfig() *TokenLimitConfig { return m.tokenLimitConfig }
func (m *BaseLLMModel) ExtraBody() map[string]any           { return m.extraBody }
func (m *BaseLLMModel) APIType() string                     { return m.apiType }
func (m *BaseLLMModel) BaseURL() string                     { return m.baseURL }

func (m *BaseLLMModel) ActiveParams() int64       { return m.activeParams }
func (m *BaseLLMModel) TokensPerSecLow() float64  { return m.tokensPerSecLow }
func (m *BaseLLMModel) TokensPerSecHigh() float64 { return m.tokensPerSecHigh }
func (m *BaseLLMModel) IsVirtual() bool           { return m.providerID == "" }

func (m *BaseLLMModel) SetCachedPromptCostPer1KTokens(v int64)  { m.cachedPromptCostPer1KTokens = v }
func (m *BaseLLMModel) SetEnabled(v bool)                       { m.enabled = v }
func (m *BaseLLMModel) SetKind(v ModelKind)                     { m.kind = v }
func (m *BaseLLMModel) SetContextWindow(v int64)                { m.contextWindow = v }
func (m *BaseLLMModel) SetOutputWindow(v int64)                 { m.outputWindow = v }
func (m *BaseLLMModel) SetCapabilities(c ModelCapabilities)     { m.capabilities = c }
func (m *BaseLLMModel) SetTokenLimitConfig(c *TokenLimitConfig) { m.tokenLimitConfig = c }
func (m *BaseLLMModel) SetExtraBody(v map[string]any)           { m.extraBody = v }
func (m *BaseLLMModel) SetAPIType(v string)                     { m.apiType = v }
func (m *BaseLLMModel) SetBaseURL(v string)                     { m.baseURL = v }
func (m *BaseLLMModel) SetActiveParams(v int64)                 { m.activeParams = v }
func (m *BaseLLMModel) SetTokensPerSecLow(v float64)            { m.tokensPerSecLow = v }
func (m *BaseLLMModel) SetTokensPerSecHigh(v float64)           { m.tokensPerSecHigh = v }

var _ LLMModel = &BaseLLMModel{}

func NewLLMModel(providerID ProviderID, orgID OrgID, proxyName, realModel, description string, promptCost, completionCost int64) *BaseLLMModel {
	return &BaseLLMModel{
		id:                        NewLLMModelID(),
		providerID:                providerID,
		orgID:                     orgID,
		proxyName:                 proxyName,
		realModel:                 realModel,
		description:               description,
		enabled:                   true,
		promptCostPer1KTokens:     promptCost,
		completionCostPer1KTokens: completionCost,
		createdAt:                 time.Now(),
		updatedAt:                 time.Now(),
	}
}

// ModelAPIType returns the provider type whose API serves m: its own
// override, or the type of its provider p.
func ModelAPIType(m LLMModel, p Provider) string {
	if t := m.APIType(); t != "" {
		return t
	}
	return p.Type()
}

// ModelBaseURL returns the base URL m is reached at: its own override, or the
// base URL of its provider p.
func ModelBaseURL(m LLMModel, p Provider) string {
	if u := m.BaseURL(); u != "" {
		return u
	}
	return p.BaseURL()
}
