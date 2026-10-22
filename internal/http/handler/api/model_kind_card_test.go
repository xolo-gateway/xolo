package api_test

import (
	"slices"
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// A decision model is listed with what tells it apart: it answers
// decisions, on /decisions, and takes no completion parameter.
func TestModelCard_DecisionModel(t *testing.T) {
	org := model.NewOrganization(testTenantID, "acme", "ACME", "")
	chat := realModel(org, "gpt", 128_000, model.ModelCapabilities{Tools: true}, 3000, 15000)
	jev := realModel(org, "jev", 0, model.ModelCapabilities{}, 1000, 4000)
	jev.SetKind(model.ModelKindDecision)
	jev.SetTokenLimitConfig(&model.TokenLimitConfig{MaxTokens: 4000})

	cards := callModels(t, org, []model.LLMModel{chat, jev}, nil, "")
	if cards["acme/jev"].PerRequestLimits != nil {
		t.Errorf("the token limit wraps completions only, the decision card must announce none")
	}

	decision, conversation := cards["acme/jev"], cards["acme/gpt"]
	if decision.Xolo.Kind != model.ModelKindDecision || conversation.Xolo.Kind != model.ModelKindCompletion {
		t.Errorf("kinds = %q and %q, want decision and completion", decision.Xolo.Kind, conversation.Xolo.Kind)
	}
	if !slices.Equal(decision.Architecture.OutputModalities, []string{"decision"}) {
		t.Errorf("output modalities = %v, want [decision]", decision.Architecture.OutputModalities)
	}
	if len(decision.SupportedParameters) != 0 {
		t.Errorf("a decision model takes no completion parameter, got %v", decision.SupportedParameters)
	}
	if !slices.Contains(conversation.SupportedParameters, "tools") {
		t.Errorf("the completion model keeps its parameters")
	}

	if listed := callModels(t, org, []model.LLMModel{chat, jev}, nil, "?output_modalities=text"); len(listed) != 1 {
		t.Errorf("a client asking for text models must not get the decision model, got %d models", len(listed))
	}
}

// A virtual model serves completions only: its card is a completion card even
// when the model its graph names has another kind.
func TestVirtualModelCard_IsACompletionCard(t *testing.T) {
	org := model.NewOrganization(testTenantID, "acme", "ACME", "")
	vectors := realModel(org, "vectors", 8_000, model.ModelCapabilities{}, 100, 0)
	vectors.SetKind(model.ModelKindEmbeddings)
	vm := vmWithGraph(org, "auto", modelNodeGraph(t, "vectors"))

	got := callModels(t, org, []model.LLMModel{vectors}, []model.VirtualModel{vm}, "")["acme/auto"]

	if got.Xolo.Kind != model.ModelKindCompletion {
		t.Errorf("kind = %q, want completion", got.Xolo.Kind)
	}
	if !slices.Equal(got.Architecture.OutputModalities, []string{"text"}) {
		t.Errorf("output modalities = %v, want [text]", got.Architecture.OutputModalities)
	}
}
