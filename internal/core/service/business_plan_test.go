package service

import (
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

func TestValidatePlanConstraint_Shared(t *testing.T) {
	value := func(v int64) *int64 { return &v }
	shared := model.PlanConstraint{
		Kind:         model.ConstraintRollingWindow,
		Label:        "month",
		Duration:     model.PlanDuration(720 * time.Hour),
		ValueBudget:  value(60_000_000),
		Scope:        model.PlanScopeShared,
		ModelBudgets: map[string]model.PlanBudget{"deepseek-v4.1-flash": {ValueBudget: value(160_000_000)}, "free": {}},
	}
	if err := validatePlanConstraint(shared); err != nil {
		t.Errorf("shared constraint rejected: %v", err)
	}

	// A zero budget would weigh infinitely.
	shared.ModelBudgets = map[string]model.PlanBudget{"deepseek-v4.1-flash": {ValueBudget: value(0)}}
	if err := validatePlanConstraint(shared); err == nil {
		t.Error("shared constraint with a zero model budget accepted, want rejected")
	}

	// Nor a kind the constraint does not limit: it would weigh nothing.
	shared.ModelBudgets = map[string]model.PlanBudget{"deepseek-v4.1-flash": {TokenBudget: value(50_000_000)}}
	if err := validatePlanConstraint(shared); err == nil {
		t.Error("shared constraint with a token model budget and no token budget accepted, want rejected")
	}

	concurrency := model.PlanConstraint{Kind: model.ConstraintConcurrency, Label: "c", MaxConcurrent: new(int), Scope: model.PlanScopeShared}
	*concurrency.MaxConcurrent = 2
	if err := validatePlanConstraint(concurrency); err == nil {
		t.Error("shared concurrency constraint accepted, want rejected")
	}
}

func TestValidatePlanConstraint_PerModel(t *testing.T) {
	value := func(v int64) *int64 { return &v }
	window := func() model.PlanConstraint {
		return model.PlanConstraint{Kind: model.ConstraintRollingWindow, Label: "5h", Duration: model.PlanDuration(5 * time.Hour)}
	}

	perModel := window()
	perModel.Scope = model.PlanScopeModel
	perModel.ModelBudgets = map[string]model.PlanBudget{"kimi-k3": {ValueBudget: value(3_000_000)}, "free": {}}
	if err := validatePlanConstraint(perModel); err != nil {
		t.Errorf("per-model constraint rejected: %v", err)
	}

	tests := map[string]func(c *model.PlanConstraint){
		"unknown scope":           func(c *model.PlanConstraint) { c.Scope = "team" },
		"overrides without scope": func(c *model.PlanConstraint) { c.ModelBudgets = map[string]model.PlanBudget{"kimi-k3": {}} },
		"per-model concurrency": func(c *model.PlanConstraint) {
			two := 2
			c.Kind, c.Scope, c.MaxConcurrent = model.ConstraintConcurrency, model.PlanScopeModel, &two
		},
		"negative model budget": func(c *model.PlanConstraint) {
			c.Scope, c.ModelBudgets = model.PlanScopeModel, map[string]model.PlanBudget{"kimi-k3": {TokenBudget: value(-1)}}
		},
		"empty model name": func(c *model.PlanConstraint) {
			c.Scope, c.ModelBudgets = model.PlanScopeModel, map[string]model.PlanBudget{"": {}}
		},
		"control char in the model": func(c *model.PlanConstraint) {
			c.Scope, c.ModelBudgets = model.PlanScopeModel, map[string]model.PlanBudget{"kimi\n": {}}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			c := window()
			mutate(&c)
			if err := validatePlanConstraint(c); err == nil {
				t.Error("constraint accepted, want it rejected")
			}
		})
	}
}

func TestValidatePlanConstraint_ShareThreshold(t *testing.T) {
	budget := int64(30_000_000)
	c := model.PlanConstraint{Kind: model.ConstraintRollingWindow, Label: "week", Duration: model.PlanDuration(168 * time.Hour), ValueBudget: &budget}
	for _, tc := range []struct {
		threshold float64
		valid     bool
	}{{0, true}, {0.7, true}, {1, true}, {-0.1, false}, {1.5, false}} {
		threshold := tc.threshold
		c.ShareThreshold = &threshold
		if err := validatePlanConstraint(c); (err == nil) != tc.valid {
			t.Errorf("threshold %v: err = %v, want valid %v", threshold, err, tc.valid)
		}
	}
}
