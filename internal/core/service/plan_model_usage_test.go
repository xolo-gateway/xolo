package service_test

import (
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

func TestGroupPlanUsageByUpstreamModel(t *testing.T) {
	kimi := model.NewLLMModel("p1", "o", "kimi", "kimi-k3", "", 0, 0)
	alias := model.NewLLMModel("p1", "o", "kimi-fast", "kimi-k3", "", 0, 0)
	glm := model.NewLLMModel("p1", "o", "glm", "glm-5.3-flash", "", 0, 0)
	idle := model.NewLLMModel("p1", "o", "qwen", "qwen3.7-plus", "", 0, 0)
	elsewhere := model.NewLLMModel("p2", "o", "other", "kimi-k3", "", 0, 0)

	got := service.GroupPlanUsageByUpstreamModel(
		[]model.LLMModel{kimi, alias, glm, idle, elsewhere},
		"p1",
		map[model.LLMModelID]port.PlanUsageTotals{
			kimi.ID():      {Tokens: 10, Value: 100},
			alias.ID():     {Tokens: 5, Value: 50},
			glm.ID():       {}, // used, for no billable token
			elsewhere.ID(): {Tokens: 1, Value: 1},
		},
	)

	if len(got) != 2 {
		t.Fatalf("got %d upstream models, want glm and kimi: %+v", len(got), got)
	}
	if got[0].UpstreamModel != "glm-5.3-flash" || got[1].UpstreamModel != "kimi-k3" {
		t.Errorf("order = %s, %s; want sorted by upstream model", got[0].UpstreamModel, got[1].UpstreamModel)
	}
	if got[1].Tokens != 15 || got[1].Value != 150 || len(got[1].ModelIDs) != 2 {
		t.Errorf("kimi-k3 = %+v, want both proxy names folded together", got[1])
	}
}

func TestWeighSharedPlanUsage(t *testing.T) {
	value := func(v int64) *int64 { return &v }
	c := model.PlanConstraint{
		Kind:        model.ConstraintRollingWindow,
		ValueBudget: value(60_000_000),
		Scope:       model.PlanScopeShared,
		ModelBudgets: map[string]model.PlanBudget{
			"deepseek-v4.1-flash": {ValueBudget: value(160_000_000)},
			"kimi-k3.1-pro":       {ValueBudget: value(15_000_000)},
			"beam-free":           {},
		},
	}
	deepseek := model.NewLLMModel("p1", "o", "deepseek", "deepseek-v4.1-flash", "", 0, 0)
	alias := model.NewLLMModel("p1", "o", "deepseek-fast", "deepseek-v4.1-flash", "", 0, 0)
	kimi := model.NewLLMModel("p1", "o", "kimi", "kimi-k3", "", 0, 0)
	free := model.NewLLMModel("p1", "o", "beam", "beam-free", "", 0, 0)

	got := service.WeighSharedPlanUsage(c, []model.LLMModel{deepseek, alias, kimi, free}, map[model.LLMModelID]port.PlanUsageTotals{
		deepseek.ID(): {Tokens: 100, Value: 8_000_000},
		alias.ID():    {Tokens: 100, Value: 8_000_000}, // same upstream model, same rate
		kimi.ID():     {Tokens: 10, Value: 1_000_000},
		free.ID():     {Tokens: 1_000, Value: 50_000_000},
		"deleted":     {Tokens: 5, Value: 2_000_000}, // no longer listed: the highest rate
	})

	// 16$ of deepseek weigh 6$, plus 1$ of kimi, and 2$ of a deleted model at
	// the rate of the smallest budget, 60$/15$: 8$.
	if got.Value != 15_000_000 {
		t.Errorf("value = %d, want 15000000", got.Value)
	}
	// The constraint limits no tokens: they are summed as they are.
	if got.Tokens != 1_215 {
		t.Errorf("tokens = %d, want 1215", got.Tokens)
	}
}
