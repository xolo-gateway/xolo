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
