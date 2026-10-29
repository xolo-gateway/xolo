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

func TestPlanConstraint_Weigh(t *testing.T) {
	value := func(v int64) *int64 { return &v }
	c := PlanConstraint{
		Kind:        ConstraintRollingWindow,
		Label:       "month",
		Duration:    PlanDuration(720 * time.Hour),
		TokenBudget: value(1_000),
		ValueBudget: value(60_000_000),
		Scope:       PlanScopeShared,
		ModelBudgets: map[string]PlanBudget{
			"deepseek-v4.1-flash": {ValueBudget: value(160_000_000)},
			"glm-5.3":             {TokenBudget: value(4_000), ValueBudget: value(120_000_000)},
			"beam-free":           {},
		},
	}

	tests := []struct {
		model         string
		tokens, value int64
		wantTokens    int64
		wantValue     int64
	}{
		// A budget of 160$ against the window's 60$: 16$ weigh 6$. The model
		// lists no token budget, so its tokens weigh nothing.
		{"deepseek-v4.1-flash", 800, 16_000_000, 0, 6_000_000},
		{"glm-5.3", 800, 12_000_000, 200, 6_000_000},
		// Not listed: the constraint's own rate.
		{"kimi-k3", 800, 12_000_000, 800, 12_000_000},
		// Listed without budget: unlimited, weighs nothing.
		{"beam-free", 800, 12_000_000, 0, 0},
	}
	for _, tt := range tests {
		tokens, value := c.Weigh(tt.model, tt.tokens, tt.value)
		if tokens != tt.wantTokens || value != tt.wantValue {
			t.Errorf("Weigh(%s) = (%d, %d), want (%d, %d)", tt.model, tokens, value, tt.wantTokens, tt.wantValue)
		}
	}

	// A dimension the constraint does not limit is left as is.
	c.TokenBudget = nil
	if tokens, _ := c.Weigh("glm-5.3", 800, 0); tokens != 800 {
		t.Errorf("tokens = %d without a token budget on the constraint, want 800", tokens)
	}

	// Only a shared constraint weighs.
	c.Scope = PlanScopeModel
	if _, value := c.Weigh("deepseek-v4.1-flash", 0, 16_000_000); value != 16_000_000 {
		t.Errorf("per-model Weigh = %d, want the figure unchanged", value)
	}
}

func TestSubscriptionPlan_HasSharedConstraint(t *testing.T) {
	plan := &SubscriptionPlan{Constraints: []PlanConstraint{
		{Kind: ConstraintRollingWindow, Scope: PlanScopeModel},
		{Kind: ConstraintConcurrency, Scope: PlanScopeShared},
	}}
	if plan.HasSharedConstraint() {
		t.Error("HasSharedConstraint = true, want false: only a rolling window can be shared")
	}
	plan.Constraints = append(plan.Constraints, PlanConstraint{Kind: ConstraintRollingWindow, Scope: PlanScopeShared})
	if !plan.HasSharedConstraint() {
		t.Error("HasSharedConstraint = false, want true")
	}
	if (*SubscriptionPlan)(nil).HasSharedConstraint() {
		t.Error("a nil plan has no shared constraint")
	}
}

func TestPlanConstraint_WeighUnknown(t *testing.T) {
	value := func(v int64) *int64 { return &v }
	c := PlanConstraint{
		Kind:        ConstraintRollingWindow,
		TokenBudget: value(1_000),
		ValueBudget: value(60_000_000),
		Scope:       PlanScopeShared,
		ModelBudgets: map[string]PlanBudget{
			"deepseek-v4.1-flash": {ValueBudget: value(160_000_000)},
			"kimi-k3.1-pro":       {ValueBudget: value(15_000_000)},
			"beam-free":           {},
		},
	}

	// The smallest value budget, 15$ against 60$, weighs four times; no model
	// has a token budget below the constraint's, so tokens keep its rate.
	tokens, weighed := c.WeighUnknown(800, 2_000_000)
	if tokens != 800 || weighed != 8_000_000 {
		t.Errorf("WeighUnknown = (%d, %d), want (800, 8000000)", tokens, weighed)
	}

	// Without a smaller budget, the constraint's own rate.
	delete(c.ModelBudgets, "kimi-k3.1-pro")
	if _, value := c.WeighUnknown(0, 2_000_000); value != 2_000_000 {
		t.Errorf("value = %d, want 2000000: a larger model budget never lowers the rate below the constraint's", value)
	}
}
