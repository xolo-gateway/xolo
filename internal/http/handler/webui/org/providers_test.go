package org

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/org/component"
)

func makeRequest(fields map[string]string) *http.Request {
	form := url.Values{}
	for k, v := range fields {
		form.Set(k, v)
	}
	r, _ := http.NewRequest("POST", "/", nil)
	r.Form = form
	return r
}

func TestParseDurationField_Empty(t *testing.T) {
	r := makeRequest(map[string]string{"val": "", "unit": "s"})
	d, err := parseDurationField(r, "val", "unit")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != 0 {
		t.Errorf("expected 0, got %v", d)
	}
}

func TestParseDurationField_Seconds(t *testing.T) {
	r := makeRequest(map[string]string{"val": "5", "unit": "s"})
	d, err := parseDurationField(r, "val", "unit")
	if err != nil {
		t.Fatal(err)
	}
	if d != 5*time.Second {
		t.Errorf("expected 5s, got %v", d)
	}
}

func TestParseDurationField_Milliseconds(t *testing.T) {
	r := makeRequest(map[string]string{"val": "500", "unit": "ms"})
	d, err := parseDurationField(r, "val", "unit")
	if err != nil {
		t.Fatal(err)
	}
	if d != 500*time.Millisecond {
		t.Errorf("expected 500ms, got %v", d)
	}
}

func TestParseDurationField_Minutes(t *testing.T) {
	r := makeRequest(map[string]string{"val": "2", "unit": "min"})
	d, err := parseDurationField(r, "val", "unit")
	if err != nil {
		t.Fatal(err)
	}
	if d != 2*time.Minute {
		t.Errorf("expected 2min, got %v", d)
	}
}

func TestParseDurationField_InvalidValue(t *testing.T) {
	r := makeRequest(map[string]string{"val": "abc", "unit": "s"})
	_, err := parseDurationField(r, "val", "unit")
	if err == nil {
		t.Error("expected error for non-numeric value")
	}
}

func TestParseDurationField_ZeroValue(t *testing.T) {
	r := makeRequest(map[string]string{"val": "0", "unit": "s"})
	_, err := parseDurationField(r, "val", "unit")
	if err == nil {
		t.Error("expected error for zero value")
	}
}

func TestParseDurationField_NegativeValue(t *testing.T) {
	r := makeRequest(map[string]string{"val": "-1", "unit": "s"})
	_, err := parseDurationField(r, "val", "unit")
	if err == nil {
		t.Error("expected error for negative value")
	}
}

func TestCoerceExtraBodyValue(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{"true", true},
		{"TRUE", true},
		{"false", false},
		{"  true  ", true},
		{"40", int64(40)},
		{"-3", int64(-3)},
		{"0.7", float64(0.7)},
		{"auto", "auto"},
		{"", ""},
		{"12abc", "12abc"},
	}
	for _, c := range cases {
		got := coerceExtraBodyValue(c.in)
		if got != c.want {
			t.Errorf("coerceExtraBodyValue(%q) = %#v (%T), want %#v (%T)", c.in, got, got, c.want, c.want)
		}
	}
}

func TestParseExtraBodyFromForm(t *testing.T) {
	t.Run("no rows yields nil", func(t *testing.T) {
		r := makeRequest(map[string]string{"extra_body_count": "0"})
		m, err := parseExtraBodyFromForm(r)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m != nil {
			t.Errorf("expected nil map, got %v", m)
		}
	})

	t.Run("typed rows", func(t *testing.T) {
		r := makeRequest(map[string]string{
			"extra_body_count":    "3",
			"extra_body_k0_key":   "reasoning_split",
			"extra_body_k0_value": "true",
			"extra_body_k1_key":   "top_k",
			"extra_body_k1_value": "40",
			"extra_body_k2_key":   "mode",
			"extra_body_k2_value": "auto",
		})
		m, err := parseExtraBodyFromForm(r)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m["reasoning_split"] != true {
			t.Errorf("reasoning_split = %#v, want true", m["reasoning_split"])
		}
		if m["top_k"] != int64(40) {
			t.Errorf("top_k = %#v, want int64(40)", m["top_k"])
		}
		if m["mode"] != "auto" {
			t.Errorf("mode = %#v, want \"auto\"", m["mode"])
		}
	})

	t.Run("empty key rows are skipped", func(t *testing.T) {
		r := makeRequest(map[string]string{
			"extra_body_count":    "2",
			"extra_body_k0_key":   "  ",
			"extra_body_k0_value": "ignored",
			"extra_body_k1_key":   "keep",
			"extra_body_k1_value": "1",
		})
		m, err := parseExtraBodyFromForm(r)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(m) != 1 || m["keep"] != int64(1) {
			t.Errorf("expected only keep=1, got %#v", m)
		}
	})

	t.Run("duplicate keys are rejected", func(t *testing.T) {
		r := makeRequest(map[string]string{
			"extra_body_count":    "2",
			"extra_body_k0_key":   "dup",
			"extra_body_k0_value": "1",
			"extra_body_k1_key":   "dup",
			"extra_body_k1_value": "2",
		})
		if _, err := parseExtraBodyFromForm(r); err == nil {
			t.Error("expected error for duplicate keys")
		}
	})

	t.Run("all-empty yields nil", func(t *testing.T) {
		r := makeRequest(map[string]string{
			"extra_body_count":  "1",
			"extra_body_k0_key": "",
		})
		m, err := parseExtraBodyFromForm(r)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m != nil {
			t.Errorf("expected nil, got %#v", m)
		}
	})
}

func TestParseSubscriptionPlanFromForm_FairShareTuning(t *testing.T) {
	// The form is the only writer of a subscription plan: a tuning field it does
	// not read is a field the next save of the provider silently erases.
	r := makeRequest(map[string]string{
		"plan_label":                  "Pro",
		"plan_constraint_count":       "1",
		"plan_c0_kind":                "rolling_window",
		"plan_c0_label":               "5h",
		"plan_c0_duration":            "5h",
		"plan_c0_token_budget":        "1000000",
		"plan_c0_reserve_ratio":       "40",
		"plan_c0_pace_slack":          "5",
		"plan_c0_happy_hour_start":    "80",
		"plan_c0_happy_hour_max_lead": "30m",
	})

	plan, err := parseSubscriptionPlanFromForm(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan == nil || len(plan.Constraints) != 1 {
		t.Fatalf("plan = %+v, want one constraint", plan)
	}
	c := plan.Constraints[0]

	if c.ReserveRatio == nil || *c.ReserveRatio != 0.4 {
		t.Errorf("ReserveRatio = %v, want 0.4", c.ReserveRatio)
	}
	if c.PaceSlack == nil || *c.PaceSlack != 0.05 {
		t.Errorf("PaceSlack = %v, want 0.05", c.PaceSlack)
	}
	if c.HappyHourStart == nil || *c.HappyHourStart != 0.8 {
		t.Errorf("HappyHourStart = %v, want 0.8", c.HappyHourStart)
	}
	if c.HappyHourMaxLead == nil || c.HappyHourMaxLead.Duration() != 30*time.Minute {
		t.Errorf("HappyHourMaxLead = %v, want 30m", c.HappyHourMaxLead)
	}
}

func TestParseSubscriptionPlanFromForm_FairShareTuningLeftToDefaults(t *testing.T) {
	r := makeRequest(map[string]string{
		"plan_label":            "Pro",
		"plan_constraint_count": "1",
		"plan_c0_kind":          "rolling_window",
		"plan_c0_duration":      "5h",
		"plan_c0_token_budget":  "1000000",
	})

	plan, err := parseSubscriptionPlanFromForm(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan == nil || len(plan.Constraints) != 1 {
		t.Fatalf("plan = %+v, want one constraint", plan)
	}
	c := plan.Constraints[0]
	if c.ReserveRatio != nil || c.PaceSlack != nil || c.HappyHourStart != nil || c.HappyHourMaxLead != nil {
		t.Errorf("tuning = (%v, %v, %v, %v), want all nil so the defaults apply",
			c.ReserveRatio, c.PaceSlack, c.HappyHourStart, c.HappyHourMaxLead)
	}
}

func TestParsePlanRatioField(t *testing.T) {
	// An empty field clears the setting; an unusable one is reported as such so
	// the caller can keep what the plan already held.
	cases := []struct {
		in      string
		want    *float64
		wantErr bool
	}{
		{"", nil, false},
		{"   ", nil, false},
		{"not a number", nil, true},
		{"30 %", nil, true},
		{"-1", nil, true},
		{"101", nil, true},
		{"0", ptr(0.0), false},
		{"30", ptr(0.3), false},
		{"100", ptr(1.0), false},
		{"12.5", ptr(0.125), false},
	}

	for _, tc := range cases {
		got, err := parsePlanRatioField(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("parsePlanRatioField(%q) error = %v, want error: %v", tc.in, err, tc.wantErr)
		}
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("parsePlanRatioField(%q) = %v, want nil", tc.in, *got)
		case tc.want != nil && got == nil:
			t.Errorf("parsePlanRatioField(%q) = nil, want %v", tc.in, *tc.want)
		case tc.want != nil && got != nil && *got != *tc.want:
			t.Errorf("parsePlanRatioField(%q) = %v, want %v", tc.in, *got, *tc.want)
		}
	}
}

func TestParseSubscriptionPlanFromForm_InvalidTuningIsReported(t *testing.T) {
	// The form is the only writer of a plan: a typo must be shown, not swallowed.
	// Reverting to the default in silence leaves the operator believing they set
	// a reserve they never set.
	cases := map[string]string{
		"plan_c0_reserve_ratio":       "30 %",
		"plan_c0_pace_slack":          "-5",
		"plan_c0_happy_hour_start":    "150",
		"plan_c0_happy_hour_max_lead": "1 h",
	}

	for field, value := range cases {
		t.Run(field, func(t *testing.T) {
			fields := map[string]string{
				"plan_label":            "Pro",
				"plan_constraint_count": "1",
				"plan_c0_kind":          "rolling_window",
				"plan_c0_label":         "5h",
				"plan_c0_duration":      "5h",
				field:                   value,
			}

			plan, err := parseSubscriptionPlanFromForm(makeRequest(fields))
			if err == nil {
				t.Fatalf("%s = %q accepted, want a validation error", field, value)
			}
			// The plan built so far comes back with the error: the screen re-renders
			// what was typed, so one mistyped percentage does not discard the rest.
			if plan == nil || len(plan.Constraints) != 1 {
				t.Fatalf("plan = %+v, want the constraint as submitted", plan)
			}
			if got := plan.Constraints[0]; got.Label != "5h" || got.Duration.Duration() != 5*time.Hour {
				t.Errorf("constraint = %+v, want the fields that were readable", got)
			}
			// The message must say which constraint is at fault: a plan carries
			// several, and they are edited on one screen.
			if !strings.Contains(err.Error(), "5h") {
				t.Errorf("error = %q, want it to name the constraint", err)
			}
		})
	}
}

func TestParseSubscriptionPlanFromForm_EmptyTuningKeepsTheDefaults(t *testing.T) {
	// Clearing a field is a deliberate act: it returns the setting to its default.
	r := makeRequest(map[string]string{
		"plan_label":                  "Pro",
		"plan_constraint_count":       "1",
		"plan_c0_kind":                "rolling_window",
		"plan_c0_duration":            "5h",
		"plan_c0_reserve_ratio":       "",
		"plan_c0_happy_hour_max_lead": "",
	})

	plan, err := parseSubscriptionPlanFromForm(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c := plan.Constraints[0]
	if c.ReserveRatio != nil || c.HappyHourMaxLead != nil {
		t.Errorf("tuning = (%v, %v), want both left to their defaults", c.ReserveRatio, c.HappyHourMaxLead)
	}
}

func ptr[T any](v T) *T { return &v }

func TestParseSubscriptionPlanFromForm_ErrorKeepsEveryConstraint(t *testing.T) {
	// A typo on the first row must not wipe the rows below it: the plan handed
	// back with the error carries all three constraints, readable fields filled.
	r := makeRequest(map[string]string{
		"plan_label":             "Pro",
		"plan_constraint_count":  "3",
		"plan_c0_kind":           "rolling_window",
		"plan_c0_label":          "5h",
		"plan_c0_duration":       "5h",
		"plan_c0_token_budget":   "1000",
		"plan_c0_reserve_ratio":  "30 %", // the typo
		"plan_c1_kind":           "rolling_window",
		"plan_c1_label":          "7d",
		"plan_c1_duration":       "168h",
		"plan_c1_token_budget":   "5000",
		"plan_c1_pace_slack":     "10",
		"plan_c2_kind":           "concurrency",
		"plan_c2_label":          "conc",
		"plan_c2_max_concurrent": "4",
	})

	plan, err := parseSubscriptionPlanFromForm(r)
	if err == nil {
		t.Fatal("expected a validation error on the first constraint")
	}
	if !strings.Contains(err.Error(), "5h") {
		t.Errorf("error = %q, want it to name the failing constraint", err)
	}
	if plan == nil || len(plan.Constraints) != 3 {
		t.Fatalf("plan carries %d constraints, want all 3 despite the error", len(plan.Constraints))
	}

	// The rows after the failing one were parsed in full.
	second := plan.Constraints[1]
	if second.TokenBudget == nil || *second.TokenBudget != 5000 || second.PaceSlack == nil || *second.PaceSlack != 0.1 {
		t.Errorf("second constraint = %+v, want its budget and pace slack read", second)
	}
	if third := plan.Constraints[2]; third.MaxConcurrent == nil || *third.MaxConcurrent != 4 {
		t.Errorf("third constraint = %+v, want its concurrency read", third)
	}
}

func TestParseSubscriptionPlanFromForm_FirstErrorWins(t *testing.T) {
	// Several typos, one message: the first in form order, so the operator fixes
	// them top to bottom.
	r := makeRequest(map[string]string{
		"plan_label":            "Pro",
		"plan_constraint_count": "2",
		"plan_c0_kind":          "rolling_window",
		"plan_c0_label":         "first",
		"plan_c0_duration":      "5h",
		"plan_c0_pace_slack":    "abc",
		"plan_c1_kind":          "rolling_window",
		"plan_c1_label":         "second",
		"plan_c1_duration":      "5h",
		"plan_c1_reserve_ratio": "999",
	})

	_, err := parseSubscriptionPlanFromForm(r)
	if err == nil {
		t.Fatal("expected a validation error")
	}
	if !strings.Contains(err.Error(), "first") || strings.Contains(err.Error(), "second") {
		t.Errorf("error = %q, want the first constraint's error only", err)
	}
}

func TestProviderWithPlan_ShowsTheSubmittedPlanOnTheStoredProvider(t *testing.T) {
	// providerWithPlan is what brings the plan back on screen as typed when the
	// form is rendered in error. It must present the submitted plan while every
	// other field still comes from the stored provider.
	stored := model.NewProvider("org-1", "Mistral", "mistral", "https://api.mistral.ai/v1", "key", "EUR")
	stored.SetBillingMode(model.BillingModeSubscription)
	stored.SetSubscriptionPlan(&model.SubscriptionPlan{Label: "stored"})

	typed := &model.SubscriptionPlan{Label: "typed", Constraints: []model.PlanConstraint{{
		Kind: model.ConstraintRollingWindow, Label: "as-typed", Duration: model.PlanDuration(5 * time.Hour),
	}}}
	p := providerWithPlan{Provider: stored, plan: typed}

	if p.SubscriptionPlan() != typed {
		t.Error("SubscriptionPlan() does not return the submitted plan")
	}
	if p.Name() != "Mistral" || p.Currency() != "EUR" || p.BillingMode() != model.BillingModeSubscription {
		t.Errorf("stored fields altered: name=%q currency=%q billing=%q", p.Name(), p.Currency(), p.BillingMode())
	}

	// And the editor renders that plan, rejected value included.
	submitted := url.Values{"plan_c0_reserve_ratio": {"30 %"}}
	var out strings.Builder
	if err := component.SubscriptionPlanEditor(p.SubscriptionPlan(), false, submitted).Render(context.Background(), &out); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := out.String()
	for _, want := range []string{`value="typed"`, `value="as-typed"`, `value="30 %"`} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered editor lacks %s", want)
		}
	}
	if strings.Contains(html, `value="stored"`) {
		t.Error("rendered editor still shows the stored plan label")
	}
}
