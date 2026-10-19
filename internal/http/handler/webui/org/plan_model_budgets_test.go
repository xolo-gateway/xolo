package org

import (
	"reflect"
	"strings"
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

func TestParsePlanModelBudgets(t *testing.T) {
	budgets, err := parsePlanModelBudgets(strings.Join([]string{
		"# Go plan, 5h window",
		"kimi-k3 = 3",
		"",
		"deepseek-v4.1-flash = 16, 50000000 tokens",
		"  glm-5.3-flash=12.5  ",
		"longcat-2.5-preview-free = illimité",
	}, "\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	value := func(v int64) *int64 { return &v }
	want := map[string]model.PlanBudget{
		"kimi-k3":                  {ValueBudget: value(3_000_000)},
		"deepseek-v4.1-flash":      {ValueBudget: value(16_000_000), TokenBudget: value(50_000_000)},
		"glm-5.3-flash":            {ValueBudget: value(12_500_000)},
		"longcat-2.5-preview-free": {},
	}
	if !reflect.DeepEqual(budgets, want) {
		t.Errorf("budgets = %+v, want %+v", budgets, want)
	}
}

func TestParsePlanModelBudgets_Rejects(t *testing.T) {
	for _, text := range []string{
		"kimi-k3",                  // no budget
		"= 3",                      // no model
		"kimi-k3 = cheap",          // not a number
		"kimi-k3 = -1",             // negative
		"kimi-k3 = 12abc tokens",   // not a token count
		"kimi-k3 = 3\nkimi-k3 = 4", // listed twice
	} {
		if _, err := parsePlanModelBudgets(text); err == nil {
			t.Errorf("parsePlanModelBudgets(%q) accepted, want an error", text)
		}
	}
}

func TestParseSubscriptionPlanFromForm_PerModel(t *testing.T) {
	r := makeRequest(map[string]string{
		"plan_label":            "OpenCode Go",
		"plan_constraint_count": "1",
		"plan_c0_kind":          "rolling_window",
		"plan_c0_label":         "5h",
		"plan_c0_duration":      "5h",
		"plan_c0_value_budget":  "6",
		"plan_c0_scope":         "model",
		"plan_c0_model_budgets": "kimi-k3 = 3",
	})

	plan, err := parseSubscriptionPlanFromForm(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c := plan.Constraints[0]
	if !c.PerModel() {
		t.Errorf("Scope = %q, want a per-model constraint", c.Scope)
	}
	if b := c.ModelBudgets["kimi-k3"]; b.ValueBudget == nil || *b.ValueBudget != 3_000_000 {
		t.Errorf("ModelBudgets = %+v, want kimi-k3 at 3", c.ModelBudgets)
	}
}

func TestParseSubscriptionPlanFromForm_ModelBudgetsNeedPerModelScope(t *testing.T) {
	// Overrides on a provider-wide constraint would be stored and never read.
	r := makeRequest(map[string]string{
		"plan_label":            "OpenCode Go",
		"plan_constraint_count": "1",
		"plan_c0_kind":          "rolling_window",
		"plan_c0_label":         "5h",
		"plan_c0_duration":      "5h",
		"plan_c0_model_budgets": "kimi-k3 = 3",
	})

	if _, err := parseSubscriptionPlanFromForm(r); err == nil {
		t.Error("plan accepted, want an error for per-model budgets on a provider-wide constraint")
	}
}
