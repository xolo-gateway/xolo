package webui

import (
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// virtualModelAsLLMModel wraps a VirtualModel to implement LLMModel interface
// for display purposes on the models page.
type virtualModelAsLLMModel struct {
	vm  model.VirtualModel
	org model.Organization
}

func (v *virtualModelAsLLMModel) ID() model.LLMModelID             { return model.LLMModelID(v.vm.ID()) }
func (v *virtualModelAsLLMModel) ProviderID() model.ProviderID     { return "" }
func (v *virtualModelAsLLMModel) OrgID() model.OrgID               { return v.vm.OrgID() }
func (v *virtualModelAsLLMModel) ProxyName() string                { return v.vm.Name() }
func (v *virtualModelAsLLMModel) RealModel() string                { return "" }
func (v *virtualModelAsLLMModel) Description() string              { return v.vm.Description() }
func (v *virtualModelAsLLMModel) Enabled() bool                    { return true }
func (v *virtualModelAsLLMModel) PromptCostPer1KTokens() int64       { return 0 }
func (v *virtualModelAsLLMModel) CachedPromptCostPer1KTokens() int64 { return 0 }
func (v *virtualModelAsLLMModel) CompletionCostPer1KTokens() int64   { return 0 }
func (v *virtualModelAsLLMModel) ContextWindow() int64             { return 0 }
func (v *virtualModelAsLLMModel) OutputWindow() int64              { return 0 }
func (v *virtualModelAsLLMModel) ActiveParams() int64              { return 0 }
func (v *virtualModelAsLLMModel) TokensPerSecLow() float64         { return 0 }
func (v *virtualModelAsLLMModel) TokensPerSecHigh() float64        { return 0 }
func (v *virtualModelAsLLMModel) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{}
}
func (v *virtualModelAsLLMModel) CreatedAt() time.Time                      { return v.vm.CreatedAt() }
func (v *virtualModelAsLLMModel) UpdatedAt() time.Time                      { return v.vm.UpdatedAt() }
func (v *virtualModelAsLLMModel) TokenLimitConfig() *model.TokenLimitConfig { return nil }
func (v *virtualModelAsLLMModel) ExtraBody() map[string]any                 { return nil }
func (v *virtualModelAsLLMModel) APIType() string                           { return "" }
func (v *virtualModelAsLLMModel) BaseURL() string                           { return "" }
func (v *virtualModelAsLLMModel) IsVirtual() bool                           { return true }

var _ model.LLMModel = &virtualModelAsLLMModel{}
