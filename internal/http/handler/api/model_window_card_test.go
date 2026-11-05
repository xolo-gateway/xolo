package api_test

import (
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// intField reads an optional number of a card, nil when it is left out.
func intField(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

// A model with a known output window publishes it in top_provider, even
// without a token limit: per_request_limits stays the operator's limit.
func TestModelCard_TopProviderPublishesTheOutputWindow(t *testing.T) {
	org := model.NewOrganization(testTenantID, "acme", "ACME", "")
	flash := realModel(org, "flash", 1_048_576, model.ModelCapabilities{Tools: true}, 0, 0)
	flash.SetOutputWindow(943_718)

	got := callModels(t, org, []model.LLMModel{flash}, nil, "")["acme/flash"]

	if got.TopProvider == nil {
		t.Fatal("top_provider is missing")
	}
	if c, m := intField(got.TopProvider.ContextLength), intField(got.TopProvider.MaxCompletionTokens); c != 1_048_576 || m != 943_718 {
		t.Errorf("top_provider = {context_length: %v, max_completion_tokens: %v}, want 1048576 and 943718", c, m)
	}
	if got.PerRequestLimits != nil {
		t.Errorf("per_request_limits = %+v, want none without a token limit", got.PerRequestLimits)
	}
}

// The token limit and the output window are told apart: the limit stays in
// per_request_limits, the window in top_provider.
func TestModelCard_TopProviderKeepsTheWindowBesideTheTokenLimit(t *testing.T) {
	org := model.NewOrganization(testTenantID, "acme", "ACME", "")
	capped := realModel(org, "capped", 200_000, model.ModelCapabilities{}, 0, 0)
	capped.SetOutputWindow(64_000)
	capped.SetTokenLimitConfig(&model.TokenLimitConfig{MaxTokens: 8_000})

	got := callModels(t, org, []model.LLMModel{capped}, nil, "")["acme/capped"]

	if got.PerRequestLimits == nil || got.PerRequestLimits.CompletionTokens != 8_000 {
		t.Errorf("per_request_limits = %+v, want completion 8000", got.PerRequestLimits)
	}
	if got.TopProvider == nil || intField(got.TopProvider.MaxCompletionTokens) != 64_000 {
		t.Errorf("top_provider = %+v, want max_completion_tokens 64000", got.TopProvider)
	}
}

// An unknown window is left out, and a card without any known window has no
// top_provider at all.
func TestModelCard_TopProviderLeavesUnknownWindowsOut(t *testing.T) {
	org := model.NewOrganization(testTenantID, "acme", "ACME", "")
	contextOnly := realModel(org, "context-only", 32_000, model.ModelCapabilities{}, 0, 0)
	unknown := realModel(org, "unknown", 0, model.ModelCapabilities{}, 0, 0)

	cards := callModels(t, org, []model.LLMModel{contextOnly, unknown}, nil, "")

	if top := cards["acme/context-only"].TopProvider; top == nil || intField(top.ContextLength) != 32_000 || top.MaxCompletionTokens != nil {
		t.Errorf("top_provider = %+v, want context_length 32000 and no max_completion_tokens", top)
	}
	if top := cards["acme/unknown"].TopProvider; top != nil {
		t.Errorf("top_provider = %+v, want none", top)
	}
}

// Only a completion model gets the windows, as only it takes max_tokens.
func TestModelCard_TopProviderIsForCompletionModels(t *testing.T) {
	org := model.NewOrganization(testTenantID, "acme", "ACME", "")
	vectors := realModel(org, "vectors", 8_000, model.ModelCapabilities{}, 0, 0)
	vectors.SetKind(model.ModelKindEmbeddings)

	if top := callModels(t, org, []model.LLMModel{vectors}, nil, "")["acme/vectors"].TopProvider; top != nil {
		t.Errorf("top_provider = %+v, want none on an embeddings model", top)
	}
}

// A virtual model publishes the smallest output window of its candidates, so
// that a client never asks for more than one of them accepts. One candidate
// with an unknown window leaves it unknown.
func TestVirtualModelCard_TopProviderTakesTheSmallestOutputWindow(t *testing.T) {
	org := model.NewOrganization(testTenantID, "acme", "ACME", "")
	big := realModel(org, "big", 200_000, model.ModelCapabilities{}, 0, 0)
	big.SetOutputWindow(128_000)
	small := realModel(org, "small", 64_000, model.ModelCapabilities{}, 0, 0)
	small.SetOutputWindow(16_000)
	unknown := realModel(org, "unknown", 64_000, model.ModelCapabilities{}, 0, 0)
	fallback := func(name string, models ...string) model.VirtualModel {
		return vmWithGraph(org, name, &model.PipelineGraph{Nodes: []model.PipelineNode{
			{ID: "f", Type: model.NodeTypeModelFallback, Data: nodeData(t, model.ModelFallbackNodeData{Models: models})},
		}})
	}

	cards := callModels(t, org, []model.LLMModel{big, small, unknown},
		[]model.VirtualModel{fallback("safe", "big", "small"), fallback("partial", "big", "unknown")}, "")

	if top := cards["acme/safe"].TopProvider; top == nil || intField(top.MaxCompletionTokens) != 16_000 || intField(top.ContextLength) != 64_000 {
		t.Errorf("top_provider = %+v, want the smallest windows 64000 and 16000", top)
	}
	if top := cards["acme/partial"].TopProvider; top == nil || top.MaxCompletionTokens != nil {
		t.Errorf("top_provider = %+v, want no max_completion_tokens with a candidate of unknown window", top)
	}
}

// The MaxOutputTokens override of a virtual model is its output window too.
func TestVirtualModelCard_TopProviderTakesTheOverride(t *testing.T) {
	org := model.NewOrganization(testTenantID, "acme", "ACME", "")
	real := realModel(org, "real", 128_000, model.ModelCapabilities{}, 0, 0)
	vm := vmWithGraph(org, "capped", modelNodeGraph(t, "real"))
	vm.SetCatalogOverrides(&model.CatalogOverrides{MaxOutputTokens: 4_096})

	got := callModels(t, org, []model.LLMModel{real}, []model.VirtualModel{vm}, "")["acme/capped"]

	if got.TopProvider == nil || intField(got.TopProvider.MaxCompletionTokens) != 4_096 {
		t.Errorf("top_provider = %+v, want max_completion_tokens 4096", got.TopProvider)
	}
}

// A personal virtual model publishes the windows of the model its graph
// names, as an organization one does.
func TestPersonalVirtualModelCard_TopProviderIsDerived(t *testing.T) {
	org := model.NewOrganization(testTenantID, "acme", "ACME", "")
	flash := realModel(org, "flash", 1_048_576, model.ModelCapabilities{}, 0, 0)
	flash.SetOutputWindow(943_718)
	pvm := personalVMWithGraph("mine", modelNodeGraph(t, "flash"))

	got := cardScenario{
		orgs:     []model.Organization{org},
		models:   map[model.OrgID][]model.LLMModel{org.ID(): {flash}},
		pvms:     []model.PersonalVirtualModel{pvm},
		tokenOrg: org.ID(),
	}.call(t)["~/mine"]

	if got.TopProvider == nil || intField(got.TopProvider.ContextLength) != 1_048_576 || intField(got.TopProvider.MaxCompletionTokens) != 943_718 {
		t.Errorf("top_provider = %+v, want 1048576 and 943718", got.TopProvider)
	}
}
