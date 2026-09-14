package org

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
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

	plan := parseSubscriptionPlanFromForm(r, nil)
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

	plan := parseSubscriptionPlanFromForm(r, nil)
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
		in     string
		want   *float64
		wantOK bool
	}{
		{"", nil, true},
		{"   ", nil, true},
		{"not a number", nil, false},
		{"30 %", nil, false},
		{"-1", nil, false},
		{"101", nil, false},
		{"0", ptr(0.0), true},
		{"30", ptr(0.3), true},
		{"100", ptr(1.0), true},
		{"12.5", ptr(0.125), true},
	}

	for _, tc := range cases {
		got, ok := parsePlanRatioField(tc.in)
		if ok != tc.wantOK {
			t.Errorf("parsePlanRatioField(%q) ok = %v, want %v", tc.in, ok, tc.wantOK)
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

func TestParseSubscriptionPlanFromForm_InvalidTuningKeepsThePreviousValue(t *testing.T) {
	// The form is the only writer of a plan: a typo must not silently replace a
	// deliberate setting with the allocator's default.
	reserve, lead := 0.4, model.PlanDuration(30*time.Minute)
	previous := &model.SubscriptionPlan{
		Label: "Pro",
		Constraints: []model.PlanConstraint{{
			Kind:             model.ConstraintRollingWindow,
			Label:            "5h",
			Duration:         model.PlanDuration(5 * time.Hour),
			ReserveRatio:     &reserve,
			HappyHourMaxLead: &lead,
		}},
	}

	r := makeRequest(map[string]string{
		"plan_label":                  "Pro",
		"plan_constraint_count":       "1",
		"plan_c0_kind":                "rolling_window",
		"plan_c0_duration":            "5h",
		"plan_c0_reserve_ratio":       "30 %",
		"plan_c0_happy_hour_max_lead": "1 h",
	})

	plan := parseSubscriptionPlanFromForm(r, previous)
	if plan == nil || len(plan.Constraints) != 1 {
		t.Fatalf("plan = %+v, want one constraint", plan)
	}
	c := plan.Constraints[0]
	if c.ReserveRatio == nil || *c.ReserveRatio != 0.4 {
		t.Errorf("ReserveRatio = %v, want the previous 0.4 kept", c.ReserveRatio)
	}
	if c.HappyHourMaxLead == nil || c.HappyHourMaxLead.Duration() != 30*time.Minute {
		t.Errorf("HappyHourMaxLead = %v, want the previous 30m kept", c.HappyHourMaxLead)
	}
}

func TestParseSubscriptionPlanFromForm_EmptyTuningClearsThePreviousValue(t *testing.T) {
	// Clearing a field is a deliberate act: it returns the setting to its default.
	reserve, lead := 0.4, model.PlanDuration(30*time.Minute)
	previous := &model.SubscriptionPlan{
		Constraints: []model.PlanConstraint{{
			Kind:             model.ConstraintRollingWindow,
			Duration:         model.PlanDuration(5 * time.Hour),
			ReserveRatio:     &reserve,
			HappyHourMaxLead: &lead,
		}},
	}

	r := makeRequest(map[string]string{
		"plan_label":                  "Pro",
		"plan_constraint_count":       "1",
		"plan_c0_kind":                "rolling_window",
		"plan_c0_duration":            "5h",
		"plan_c0_reserve_ratio":       "",
		"plan_c0_happy_hour_max_lead": "",
	})

	c := parseSubscriptionPlanFromForm(r, previous).Constraints[0]
	if c.ReserveRatio != nil || c.HappyHourMaxLead != nil {
		t.Errorf("tuning = (%v, %v), want both cleared", c.ReserveRatio, c.HappyHourMaxLead)
	}
}

func ptr[T any](v T) *T { return &v }
