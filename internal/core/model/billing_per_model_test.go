package model

import (
	"testing"
	"time"
)

func TestPlanConstraint_ForModel(t *testing.T) {
	value := func(v int64) *int64 { return &v }
	c := PlanConstraint{
		Kind:        ConstraintRollingWindow,
		Label:       "5h",
		Duration:    PlanDuration(5 * time.Hour),
		TokenBudget: value(1_000),
		ValueBudget: value(3_000_000),
		Scope:       PlanScopeModel,
		ModelBudgets: map[string]PlanBudget{
			"glm-5.3-flash": {ValueBudget: value(12_000_000)},
		},
	}

	// A model without override takes the constraint's budgets.
	kimi := c.ForModel("kimi-k3")
	if *kimi.ValueBudget != 3_000_000 || *kimi.TokenBudget != 1_000 || kimi.ModelBudgets != nil {
		t.Errorf("kimi = %+v, want the constraint budgets and no overrides", kimi)
	}

	// An override replaces both budgets: the token budget it does not set is gone.
	glm := c.ForModel("glm-5.3-flash")
	if *glm.ValueBudget != 12_000_000 || glm.TokenBudget != nil {
		t.Errorf("glm = %+v, want its own value budget and no token budget", glm)
	}
	if !glm.PerModel() {
		t.Error("the resolved constraint must still read as per model")
	}

	// A provider-wide constraint is left as is.
	c.Scope = ""
	if got := c.ForModel("glm-5.3-flash"); *got.ValueBudget != 3_000_000 {
		t.Errorf("provider-wide ForModel = %+v, want unchanged", got)
	}
}

func TestPlanConstraint_PerModelOnlyForRollingWindows(t *testing.T) {
	c := PlanConstraint{Kind: ConstraintConcurrency, Scope: PlanScopeModel}
	if c.PerModel() {
		t.Error("a concurrency constraint is always provider-wide")
	}
}

func TestPlanUsageScope_KeyIgnoresModelOrder(t *testing.T) {
	a := PlanUsageScope{OrgID: "o", ProviderID: "p", ModelIDs: []LLMModelID{"m1", "m2"}}
	b := PlanUsageScope{OrgID: "o", ProviderID: "p", ModelIDs: []LLMModelID{"m2", "m1"}}
	if a.Key() != b.Key() {
		t.Errorf("keys differ: %q vs %q", a.Key(), b.Key())
	}
	if a.Key() == (PlanUsageScope{OrgID: "o", ProviderID: "p"}).Key() {
		t.Error("a per-model scope must not share the provider-wide key")
	}
	if a.ModelIDs[0] != "m1" {
		t.Error("Key must not reorder the scope's own model ids")
	}
}

func TestUpstreamModelIDs(t *testing.T) {
	kimi := NewLLMModel("p1", "o", "kimi", "kimi-k3", "", 0, 0)
	alias := NewLLMModel("p1", "o", "kimi-fast", "kimi-k3", "", 0, 0)
	otherProvider := NewLLMModel("p2", "o", "kimi-elsewhere", "kimi-k3", "", 0, 0)
	glm := NewLLMModel("p1", "o", "glm", "glm-5.3-flash", "", 0, 0)

	got := UpstreamModelIDs([]LLMModel{glm, alias, kimi, otherProvider}, kimi)
	if len(got) != 2 || got[0] != kimi.ID() || got[1] != alias.ID() {
		t.Errorf("UpstreamModelIDs = %v, want kimi and its alias on the same provider", got)
	}
}
