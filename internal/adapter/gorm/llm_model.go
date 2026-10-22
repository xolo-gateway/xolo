package gorm

import (
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

type LLMModel struct {
	ID                        string `gorm:"primaryKey;autoIncrement:false"`
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
	ProviderID                string `gorm:"index"`
	OrgID                     string `gorm:"uniqueIndex:idx_org_proxy;not null"`
	ProxyName                 string `gorm:"uniqueIndex:idx_org_proxy;not null"`
	RealModel                 string `gorm:"not null"`
	Description               string
	// See the note on Organization.Active: no `default` here on purpose.
	Enabled                   int
	PromptCostPer1KTokens        int64 `gorm:"default:0"`
	CachedPromptCostPer1KTokens  int64 `gorm:"default:0"`
	CompletionCostPer1KTokens    int64 `gorm:"default:0"`
	ContextWindow             int64                              `gorm:"default:0"`
	OutputWindow              int64                              `gorm:"default:0"`
	ActiveParams              int64                              `gorm:"default:0"`
	TokensPerSecLow           float64                            `gorm:"default:0"`
	TokensPerSecHigh          float64                            `gorm:"default:0"`
	CapTools                  int                                `gorm:"default:0"`
	CapVision                 int                                `gorm:"default:0"`
	CapReasoning              int                                `gorm:"default:0"`
	CapAudio                  int                                `gorm:"default:0"`
	TokenLimitConfig          JSONColumn[model.TokenLimitConfig] `gorm:"type:text"`
	ExtraBody                 JSONColumn[map[string]any]         `gorm:"type:text"`
	// APIType and BaseURL override the provider's type and base URL for this
	// model; empty keeps the provider's.
	APIType string `gorm:"not null;default:''"`
	BaseURL string `gorm:"not null;default:''"`
	// Kind is what the model is called for. The models stored before kinds
	// existed hold the default, completion.
	Kind string `gorm:"not null;default:'completion'"`
}

type wrappedLLMModel struct {
	m *LLMModel
}

func (w *wrappedLLMModel) ID() model.LLMModelID             { return model.LLMModelID(w.m.ID) }
func (w *wrappedLLMModel) ProviderID() model.ProviderID     { return model.ProviderID(w.m.ProviderID) }
func (w *wrappedLLMModel) OrgID() model.OrgID               { return model.OrgID(w.m.OrgID) }
func (w *wrappedLLMModel) ProxyName() string                { return w.m.ProxyName }
func (w *wrappedLLMModel) RealModel() string                { return w.m.RealModel }
func (w *wrappedLLMModel) Description() string              { return w.m.Description }
func (w *wrappedLLMModel) Kind() model.ModelKind            { return model.KindOrDefault(model.ModelKind(w.m.Kind)) }
func (w *wrappedLLMModel) Enabled() bool                    { return w.m.Enabled != 0 }
func (w *wrappedLLMModel) PromptCostPer1KTokens() int64 { return w.m.PromptCostPer1KTokens }
func (w *wrappedLLMModel) CachedPromptCostPer1KTokens() int64 {
	if w.m.CachedPromptCostPer1KTokens == 0 {
		return w.m.PromptCostPer1KTokens
	}
	return w.m.CachedPromptCostPer1KTokens
}
func (w *wrappedLLMModel) CompletionCostPer1KTokens() int64 { return w.m.CompletionCostPer1KTokens }
func (w *wrappedLLMModel) ContextWindow() int64             { return w.m.ContextWindow }
func (w *wrappedLLMModel) OutputWindow() int64              { return w.m.OutputWindow }
func (w *wrappedLLMModel) ActiveParams() int64              { return w.m.ActiveParams }
func (w *wrappedLLMModel) TokensPerSecLow() float64         { return w.m.TokensPerSecLow }
func (w *wrappedLLMModel) TokensPerSecHigh() float64        { return w.m.TokensPerSecHigh }
func (w *wrappedLLMModel) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{
		Tools:      w.m.CapTools != 0,
		Vision:     w.m.CapVision != 0,
		Reasoning:  w.m.CapReasoning != 0,
		Audio:      w.m.CapAudio != 0,
	}
}
func (w *wrappedLLMModel) CreatedAt() time.Time                      { return w.m.CreatedAt }
func (w *wrappedLLMModel) UpdatedAt() time.Time                      { return w.m.UpdatedAt }
func (w *wrappedLLMModel) TokenLimitConfig() *model.TokenLimitConfig { return w.m.TokenLimitConfig.Val }
func (w *wrappedLLMModel) ExtraBody() map[string]any {
	if w.m.ExtraBody.Val == nil {
		return nil
	}
	return *w.m.ExtraBody.Val
}
func (w *wrappedLLMModel) APIType() string { return w.m.APIType }
func (w *wrappedLLMModel) BaseURL() string { return w.m.BaseURL }
func (w *wrappedLLMModel) IsVirtual() bool                           { return w.m.ProviderID == "" }

var _ model.LLMModel = &wrappedLLMModel{}

func fromLLMModel(m model.LLMModel) *LLMModel {
	caps := m.Capabilities()
	var extraBody *map[string]any
	if eb := m.ExtraBody(); len(eb) > 0 {
		extraBody = &eb
	}
	return &LLMModel{
		ID:                        string(m.ID()),
		ProviderID:                string(m.ProviderID()),
		OrgID:                     string(m.OrgID()),
		ProxyName:                 m.ProxyName(),
		RealModel:                 m.RealModel(),
		Description:               m.Description(),
		Enabled:                   boolToInt(m.Enabled()),
		PromptCostPer1KTokens:       m.PromptCostPer1KTokens(),
		CachedPromptCostPer1KTokens: m.CachedPromptCostPer1KTokens(),
		CompletionCostPer1KTokens:   m.CompletionCostPer1KTokens(),
		ContextWindow:             m.ContextWindow(),
		OutputWindow:              m.OutputWindow(),
		ActiveParams:              m.ActiveParams(),
		TokensPerSecLow:           m.TokensPerSecLow(),
		TokensPerSecHigh:          m.TokensPerSecHigh(),
		CapTools:                  boolToInt(caps.Tools),
		CapVision:                 boolToInt(caps.Vision),
		CapReasoning:              boolToInt(caps.Reasoning),
		CapAudio:                  boolToInt(caps.Audio),
		TokenLimitConfig:          JSONColumn[model.TokenLimitConfig]{Val: m.TokenLimitConfig()},
		ExtraBody:                 JSONColumn[map[string]any]{Val: extraBody},
		APIType:                   m.APIType(),
		BaseURL:                   m.BaseURL(),
		Kind:                      string(m.Kind()),
	}
}
