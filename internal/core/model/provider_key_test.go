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

func TestKeyPlansAdjustTheProviderPlanPerKey(t *testing.T) {
	month, glm, kimi := int64(60_000_000), int64(180_000_000), int64(120_000_000)
	more, moreGLM := int64(65_000_000), int64(185_000_000)
	anchor := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
	moved := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)
	p := NewProvider("org", "OpenCode", "openai", "", "own", "USD")
	p.SetSubscriptionPlan(&SubscriptionPlan{Constraints: []PlanConstraint{{
		Kind: ConstraintRollingWindow, Label: "Mois", Duration: PlanDuration(720 * time.Hour), ValueBudget: &month, WindowAnchor: &anchor,
		Scope: PlanScopeShared, ModelBudgets: map[string]PlanBudget{"glm": {ValueBudget: &glm}, "kimi": {ValueBudget: &kimi}},
	}}})
	p.SetExtraAPIKeys([]ProviderKey{
		{ID: "k2", Overrides: map[string]PlanConstraintOverride{"Mois": {
			ValueBudget: &more, ModelBudgets: map[string]PlanBudget{"glm": {ValueBudget: &moreGLM}}, WindowAnchor: &moved,
		}}},
		{ID: "k3"},
	})

	k2 := KeyPlan(p, "k2").Constraints[0]
	if *k2.ValueBudget != 65_000_000 || *k2.ModelBudgets["glm"].ValueBudget != 185_000_000 || *k2.ModelBudgets["kimi"].ValueBudget != 120_000_000 {
		t.Errorf("k2 = %+v, want 65$, glm 185$ and the plan's kimi", k2)
	}
	if KeyPlan(p, "k3") != p.SubscriptionPlan() || KeyPlan(p, PrimaryProviderKey) != p.SubscriptionPlan() {
		t.Error("a key without override does not hold the provider's plan")
	}
	if *p.SubscriptionPlan().Constraints[0].ValueBudget != 60_000_000 || *p.SubscriptionPlan().Constraints[0].ModelBudgets["glm"].ValueBudget != 180_000_000 {
		t.Error("an override changed the provider's plan")
	}

	pooled := PooledPlan(p).Constraints[0]
	if *pooled.ValueBudget != 185_000_000 || *pooled.ModelBudgets["glm"].ValueBudget != 545_000_000 || *pooled.ModelBudgets["kimi"].ValueBudget != 360_000_000 {
		t.Errorf("pool = %d, glm %d, kimi %d; want 185$, 545$ and 360$",
			*pooled.ValueBudget, *pooled.ModelBudgets["glm"].ValueBudget, *pooled.ModelBudgets["kimi"].ValueBudget)
	}
	if !pooled.WindowAnchor.Equal(anchor) {
		t.Errorf("pool anchor = %v, want the provider's", pooled.WindowAnchor)
	}

	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	windows := KeyWindows(p, "Mois", now)
	if len(windows) != 1 || !windows["k2"].Equal(moved) {
		t.Errorf("windows = %v, want k2's own, from the 29th", windows)
	}
	if KeyWindows(p, "Semaine", now) != nil {
		t.Error("windows for a constraint the plan does not have")
	}
}

func TestPooledPlanLeavesOutTheKeysAModelIsUnlimitedOn(t *testing.T) {
	month, glm := int64(60_000_000), int64(180_000_000)
	p := NewProvider("org", "OpenCode", "openai", "", "own", "USD")
	p.SetSubscriptionPlan(&SubscriptionPlan{Constraints: []PlanConstraint{{
		Kind: ConstraintRollingWindow, Label: "Mois", Duration: PlanDuration(720 * time.Hour), ValueBudget: &month,
		Scope: PlanScopeShared, ModelBudgets: map[string]PlanBudget{"glm": {ValueBudget: &glm}},
	}}})
	p.SetExtraAPIKeys([]ProviderKey{
		{ID: "k2", Overrides: map[string]PlanConstraintOverride{"Mois": {ModelBudgets: map[string]PlanBudget{"glm": {}}}}},
		{ID: "k3", Overrides: map[string]PlanConstraintOverride{"Mois": {ModelBudgets: map[string]PlanBudget{"free": {}}}}},
	})

	pooled := PooledPlan(p).Constraints[0]
	if got := *pooled.ModelBudgets["glm"].ValueBudget; got != 360_000_000 {
		t.Errorf("pooled glm = %d, want 360$: k2 leaves it unlimited and adds nothing", got)
	}
	if b := pooled.ModelBudgets["free"]; b.ValueBudget == nil || *b.ValueBudget != 120_000_000 {
		t.Errorf("pooled free = %+v, want 120$: the keys that do not list it count at their window's budget", b)
	}
}

func TestKeyWindowsIgnoreAnythingButARollingWindow(t *testing.T) {
	anchor := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)
	concurrent := 2
	p := NewProvider("org", "OpenCode", "openai", "", "own", "USD")
	p.SetSubscriptionPlan(&SubscriptionPlan{Constraints: []PlanConstraint{
		{Kind: ConstraintConcurrency, Label: "parallel", MaxConcurrent: &concurrent},
		{Kind: ConstraintRollingWindow, Label: "unset"},
	}})
	p.SetExtraAPIKeys([]ProviderKey{{ID: "k2", Overrides: map[string]PlanConstraintOverride{
		"parallel": {WindowAnchor: &anchor}, "unset": {WindowAnchor: &anchor},
	}}})

	for _, label := range []string{"parallel", "unset"} {
		if w := KeyWindows(p, label, anchor.Add(time.Hour)); w != nil {
			t.Errorf("%s: windows = %v, want none: it has no window to start", label, w)
		}
	}
}
