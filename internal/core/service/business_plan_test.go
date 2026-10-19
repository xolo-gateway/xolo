package service

import (
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

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
