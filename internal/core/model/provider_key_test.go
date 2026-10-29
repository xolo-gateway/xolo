package model

import (
	"testing"
	"time"
)

func TestPooledPlanCountsEachConstraintOncePerKey(t *testing.T) {
	budget, glm, concurrent := int64(10_000_000), int64(30_000_000), 2
	p := NewProvider("org", "OpenCode", "openai", "", "own", "USD")
	p.SetSubscriptionPlan(&SubscriptionPlan{Label: "Go", Constraints: []PlanConstraint{
		{Kind: ConstraintRollingWindow, Label: "5h", Duration: PlanDuration(5 * time.Hour), ValueBudget: &budget, Scope: PlanScopeShared,
			ModelBudgets: map[string]PlanBudget{"glm": {ValueBudget: &glm}, "free": {}}},
		{Kind: ConstraintConcurrency, Label: "parallel", MaxConcurrent: &concurrent},
	}})

	if got := PooledPlan(p); got != p.SubscriptionPlan() {
		t.Errorf("a provider with its own key alone keeps its plan, got %+v", got)
	}

	p.SetExtraAPIKeys([]ProviderKey{{ID: "k2", Label: "member 2"}, {ID: "k3", Label: "member 3"}})
	pooled := PooledPlan(p)
	window, parallel := pooled.Constraints[0], pooled.Constraints[1]
	if *window.ValueBudget != 30_000_000 || *window.ModelBudgets["glm"].ValueBudget != 90_000_000 {
		t.Errorf("window = %d, glm %d; want 30$ and 90$", *window.ValueBudget, *window.ModelBudgets["glm"].ValueBudget)
	}
	if window.ModelBudgets["free"].ValueBudget != nil {
		t.Error("a model without budget got one")
	}
	if *parallel.MaxConcurrent != 6 {
		t.Errorf("concurrency = %d, want 6", *parallel.MaxConcurrent)
	}
	// The shared weights do not move: each model fills the pool at its rate.
	if _, v := window.Weigh("glm", 0, 30_000_000); v != 10_000_000 {
		t.Errorf("30$ of glm weigh %d, want 10$ as on one key", v)
	}
	if budget != 10_000_000 || glm != 30_000_000 || *p.SubscriptionPlan().Constraints[0].ValueBudget != 10_000_000 {
		t.Error("pooling changed the provider's own plan")
	}
	if ids := ProviderKeyIDs(p); len(ids) != 3 || ids[0] != PrimaryProviderKey || ids[2] != "k3" {
		t.Errorf("ProviderKeyIDs = %q, want the provider's own key first", ids)
	}
}
