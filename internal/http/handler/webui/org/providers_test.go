package org

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/crypto"
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

func TestParseRequestHeadersFromForm(t *testing.T) {
	t.Run("nothing set yields nil", func(t *testing.T) {
		h, err := parseRequestHeadersFromForm(makeRequest(map[string]string{"request_header_count": "0", "session_header": " "}))
		if err != nil || h != nil {
			t.Errorf("got %+v, %v; want nil, nil", h, err)
		}
	})

	t.Run("opencode go", func(t *testing.T) {
		h, err := parseRequestHeadersFromForm(makeRequest(map[string]string{
			"session_header":          " x-opencode-session ",
			"request_header_count":    "1",
			"request_header_k0_key":   "User-Agent",
			"request_header_k0_value": " xolo/1.0 ",
		}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if h.SessionHeader != "x-opencode-session" || len(h.Headers) != 1 || h.Headers["User-Agent"] != "xolo/1.0" {
			t.Errorf("got %+v", h)
		}
	})

	t.Run("credentials are refused", func(t *testing.T) {
		_, err := parseRequestHeadersFromForm(makeRequest(map[string]string{
			"request_header_count":    "1",
			"request_header_k0_key":   "Authorization",
			"request_header_k0_value": "Bearer stolen",
		}))
		if err == nil {
			t.Error("expected an error for a reserved header")
		}
	})
}

func TestParseProviderKeysFromForm(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef"
	stored := []model.ProviderKey{
		{ID: "k2", Label: "member 2", APIKey: "encrypted-2"},
		{ID: "k3", Label: "member 3", APIKey: "encrypted-3"},
	}

	t.Run("a form without the section keeps the stored keys", func(t *testing.T) {
		keys, err := parseProviderKeysFromForm(makeRequest(map[string]string{"name": "OpenCode"}), secret, stored, nil)
		if err != nil || len(keys) != 2 {
			t.Errorf("got %+v, %v; want the stored keys", keys, err)
		}
	})

	t.Run("edits, removes and adds", func(t *testing.T) {
		keys, err := parseProviderKeysFromForm(makeRequest(map[string]string{
			"provider_key_count":    "3",
			"provider_key_k0_id":    "k2",
			"provider_key_k0_label": " member two ",
			"provider_key_k1_id":    "",
			"provider_key_k1_label": "member 4",
			"provider_key_k1_value": " sk-4 ",
			// An added row left empty.
			"provider_key_k2_id": "",
		}), secret, stored, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(keys) != 2 {
			t.Fatalf("got %d keys, want 2: k3 was removed, the empty row skipped", len(keys))
		}
		if !reflect.DeepEqual(keys[0], model.ProviderKey{ID: "k2", Label: "member two", APIKey: "encrypted-2"}) {
			t.Errorf("k2 = %+v, want relabelled with its secret kept", keys[0])
		}
		if keys[1].ID == "" || keys[1].Label != "member 4" {
			t.Errorf("new key = %+v, want an id and its label", keys[1])
		}
		if plain, err := crypto.Decrypt(secret, keys[1].APIKey); err != nil || plain != "sk-4" {
			t.Errorf("new key decrypts to %q, %v; want sk-4", plain, err)
		}
	})

	t.Run("an unknown id is a new key", func(t *testing.T) {
		_, err := parseProviderKeysFromForm(makeRequest(map[string]string{
			"provider_key_count":    "1",
			"provider_key_k0_id":    "forged",
			"provider_key_k0_label": "member 5",
		}), secret, stored, nil)
		if err == nil {
			t.Error("expected an error: a new key needs its value")
		}
	})

	t.Run("a stored key posted twice is kept once", func(t *testing.T) {
		keys, err := parseProviderKeysFromForm(makeRequest(map[string]string{
			"provider_key_count":    "2",
			"provider_key_k0_id":    "k2",
			"provider_key_k0_label": "member 2",
			"provider_key_k1_id":    "k2",
			"provider_key_k1_label": "member 2 bis",
			"provider_key_k1_value": "sk-bis",
		}), secret, stored, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(keys) != 2 || keys[0].ID != "k2" || keys[1].ID == "k2" || keys[1].ID == "" {
			t.Errorf("got %+v, want k2 once and the second row as a new key", keys)
		}
	})

	for name, fields := range map[string]map[string]string{
		// The count sizes what the parser allocates.
		"a count out of range": {"provider_key_count": "99999999999999999999"},
		"too many keys":        {"provider_key_count": "100000000"},
		"a negative count":     {"provider_key_count": "-1"},
		"a key without label":  {"provider_key_count": "1", "provider_key_k0_value": "sk"},
		"a label used twice": {
			"provider_key_count": "2",
			"provider_key_k0_id": "k2", "provider_key_k0_label": "member",
			"provider_key_k1_id": "k3", "provider_key_k1_label": "member",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseProviderKeysFromForm(makeRequest(fields), secret, stored, nil); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestParseProviderKeysFromForm_Overrides(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef"
	stored := []model.ProviderKey{{ID: "k2", Label: "member 2", APIKey: "encrypted-2"}}
	month, week := int64(60_000_000), int64(30_000_000)
	glm := int64(180_000_000)
	plan := &model.SubscriptionPlan{Constraints: []model.PlanConstraint{
		{Kind: model.ConstraintRollingWindow, Label: "Semaine", Duration: model.PlanDuration(168 * time.Hour), ValueBudget: &week},
		{Kind: model.ConstraintRollingWindow, Label: "Mois", Duration: model.PlanDuration(720 * time.Hour), ValueBudget: &month,
			Scope: model.PlanScopeShared, ModelBudgets: map[string]model.PlanBudget{"glm": {ValueBudget: &glm}}},
	}}
	fields := map[string]string{
		"provider_key_count":    "1",
		"provider_key_k0_id":    "k2",
		"provider_key_k0_label": "member 2",
		// The week is kept as is.
		"provider_key_k0_o0_label": "Semaine",
		// Posted in another order than the plan's: the label decides.
		"provider_key_k0_o1_label":         "Mois",
		"provider_key_k0_o1_value_budget":  "65",
		"provider_key_k0_o1_model_budgets": "glm = 185",
		"provider_key_k0_o1_reset_in":      "9d",
		// A window the plan no longer has is dropped.
		"provider_key_k0_o2_label":        "5h",
		"provider_key_k0_o2_value_budget": "12",
	}

	before := time.Now()
	keys, err := parseProviderKeysFromForm(makeRequest(fields), secret, stored, plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	overrides := keys[0].Overrides
	if len(overrides) != 1 {
		t.Fatalf("overrides = %+v, want the month alone", overrides)
	}
	o := overrides["Mois"]
	if o.ValueBudget == nil || *o.ValueBudget != 65_000_000 || o.TokenBudget != nil {
		t.Errorf("month budget = %+v, want 65$", o)
	}
	if b := o.ModelBudgets["glm"].ValueBudget; b == nil || *b != 185_000_000 {
		t.Errorf("glm = %v, want 185$", b)
	}
	// The month resets in nine days: its window started 21 days ago.
	if o.WindowAnchor == nil || o.Apply(plan.Constraints[1]).NextResetAt(before).Sub(before) < 9*24*time.Hour-time.Minute {
		t.Errorf("anchor = %v, want a window resetting in 9 days", o.WindowAnchor)
	}

	fields["provider_key_k0_o1_model_budgets"] = "glm = 0"
	if _, err := parseProviderKeysFromForm(makeRequest(fields), secret, stored, plan); err == nil {
		t.Error("expected an error: a zero model budget weighs infinitely on a shared window")
	}
}

func TestRedisplayedKeysDropTheSecretOfNewKeys(t *testing.T) {
	stored := []model.ProviderKey{{ID: "k2", Label: "member 2", APIKey: "encrypted-2"}}
	posted := []model.ProviderKey{
		{ID: "k2", Label: "member two", APIKey: "encrypted-2"},
		{ID: "fresh", Label: "member 3", APIKey: "encrypted-3"},
	}

	got := redisplayedKeys(posted, stored)

	want := []model.ProviderKey{{ID: "k2", Label: "member two", APIKey: "encrypted-2"}, {Label: "member 3"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v: a key never saved is shown as a new row whose value is typed again", got, want)
	}
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
		"plan_c0_share_threshold":     "70",
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
	if c.ShareThreshold == nil || *c.ShareThreshold != 0.7 {
		t.Errorf("ShareThreshold = %v, want 0.7", c.ShareThreshold)
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
		{"NaN", nil, true}, // ParseFloat accepts it, and NaN passes every comparison
		{"Inf", nil, true},
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

// TestParseSubscriptionPlanFromForm_InvalidValueBudgetIsReported covers
// the value_budget path on a rolling_window constraint. The same fix
// that turned parseBudgetField from "silently map invalid input to nil"
// into "return a parse error" (issue #88) is wired into the plan
// parser at providers.go:619-621: a typo on value_budget must surface
// through fail() with the constraint name, not silently drop the cap.
// 0 is a strict zero cap (the enforcer rejects spent >= 0).
func TestParseSubscriptionPlanFromForm_InvalidValueBudgetIsReported(t *testing.T) {
	cases := []string{
		"1e16", // overflows int64(f * 1_000_000)
		"-1",   // negative: must reject
		"abc",  // unparsable: must reject
		"NaN",  // NaN: must reject
		"Inf",  // +Inf: must reject
	}

	for _, value := range cases {
		t.Run(value, func(t *testing.T) {
			fields := map[string]string{
				"plan_label":            "Pro",
				"plan_constraint_count": "1",
				"plan_c0_kind":          "rolling_window",
				"plan_c0_label":         "5h",
				"plan_c0_duration":      "5h",
				"plan_c0_token_budget":  "1000",
				"plan_c0_value_budget":  value,
			}

			plan, err := parseSubscriptionPlanFromForm(makeRequest(fields))
			if err == nil {
				t.Fatalf("plan_c0_value_budget = %q accepted, want a validation error", value)
			}
			if plan == nil || len(plan.Constraints) != 1 {
				t.Fatalf("plan = %+v, want the constraint as submitted", plan)
			}
			if got := plan.Constraints[0]; got.Label != "5h" || got.Duration.Duration() != 5*time.Hour {
				t.Errorf("constraint = %+v, want the fields that were readable", got)
			}
			if !strings.Contains(err.Error(), "5h") {
				t.Errorf("error = %q, want it to name the constraint", err)
			}
		})
	}
}

// TestParseSubscriptionPlanFromForm_ZeroValueBudgetIsAccepted covers the
// positive case for value_budget = 0. Issue #88 documents that 0 must
// mean "strict zero cap" (every request gets a 429), not "unlimited";
// the plan parser must accept 0 and store the strict-cap pointer.
func TestParseSubscriptionPlanFromForm_ZeroValueBudgetIsAccepted(t *testing.T) {
	r := makeRequest(map[string]string{
		"plan_label":            "Pro",
		"plan_constraint_count": "1",
		"plan_c0_kind":          "rolling_window",
		"plan_c0_label":         "5h",
		"plan_c0_duration":      "5h",
		"plan_c0_token_budget":  "1000",
		"plan_c0_value_budget":  "0",
	})

	plan, err := parseSubscriptionPlanFromForm(r)
	if err != nil {
		t.Fatalf("unexpected error for value_budget=0: %v", err)
	}
	c := plan.Constraints[0]
	if c.ValueBudget == nil {
		t.Fatalf("ValueBudget = nil, want pointer to 0 (strict zero cap)")
	}
	if *c.ValueBudget != 0 {
		t.Errorf("ValueBudget = %d, want 0", *c.ValueBudget)
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
	// Stored as PAYG: the operator is switching it to a subscription, and the
	// form must come back showing that switch, or the plan editor vanishes and
	// the resubmit saves PAYG.
	stored := model.NewProvider("org-1", "Mistral", "mistral", "https://api.mistral.ai/v1", "key", "EUR")
	stored.SetBillingMode(model.BillingModePayg)
	stored.SetSubscriptionPlan(&model.SubscriptionPlan{Label: "stored"})

	typed := &model.SubscriptionPlan{Label: "typed", Constraints: []model.PlanConstraint{{
		Kind: model.ConstraintRollingWindow, Label: "as-typed", Duration: model.PlanDuration(5 * time.Hour),
	}}}
	p := providerWithPlan{Provider: stored, pType: "mistral", currency: "EUR", active: true, billingMode: model.BillingModeSubscription, plan: typed}

	if p.SubscriptionPlan() != typed {
		t.Error("SubscriptionPlan() does not return the submitted plan")
	}
	if p.BillingMode() != model.BillingModeSubscription {
		t.Errorf("BillingMode() = %q, want the submitted subscription mode, not the stored PAYG", p.BillingMode())
	}
	if p.Name() != "Mistral" || p.Currency() != "EUR" {
		t.Errorf("stored fields altered: name=%q currency=%q", p.Name(), p.Currency())
	}

	// A rejected edit must come back showing the submitted identity, so a
	// provider stored with a type the form no longer offers is displayed
	// with what the operator just typed, not the stale row.
	form := makeRequest(map[string]string{
		"name": "Mistral EU", "base_url": " https://eu.mistral.ai/v1 ", "provider_type": "openrouter",
		"currency": "GBP", "cloud_tier": "2",
		// "active" absent: the operator unticked it.
	})
	edited := submittedProvider(form, stored, model.BillingModePayg, nil)
	if edited.Name() != "Mistral EU" || edited.BaseURL() != "https://eu.mistral.ai/v1" || edited.Type() != "openrouter" {
		t.Errorf("submitted identity lost: name=%q url=%q type=%q", edited.Name(), edited.BaseURL(), edited.Type())
	}
	if edited.Currency() != "GBP" || edited.CloudTier() != 2 || edited.Active() {
		t.Errorf("submitted currency/tier/active lost: %q %d %v", edited.Currency(), edited.CloudTier(), edited.Active())
	}
	// A blank submitted type must not fall back on the stored one: the form
	// asks the operator to pick one and must not show the old one selected.
	blank := submittedProvider(makeRequest(map[string]string{"name": "x"}), stored, model.BillingModePayg, nil)
	if blank.Type() != "" || blank.Currency() != "EUR" {
		t.Errorf("blank type must stay blank and currency fall back: type=%q currency=%q", blank.Type(), blank.Currency())
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

// TestTestProviderConnection_KnowsEveryFormType guards the coupling between
// the type list offered by the provider form and the genai registry: a type
// selectable in the UI but unknown to the registry would only fail once an
// administrator clicks "test connection".
func TestTestProviderConnection_KnowsEveryFormType(t *testing.T) {
	for _, providerType := range model.ProviderTypes() {
		t.Run(providerType, func(t *testing.T) {
			ok, err := testProviderConnection(context.Background(), providerType, "http://127.0.0.1:9", "test-key", nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !ok {
				t.Fatal("expected the client to be created")
			}
		})
	}
}

func TestTestProviderConnection_UnknownType(t *testing.T) {
	if _, err := testProviderConnection(context.Background(), "nope", "http://127.0.0.1:9", "k", nil); err == nil {
		t.Fatal("expected an error for an unknown provider type")
	}
}

func TestIsKnownProviderType(t *testing.T) {
	for _, providerType := range model.ProviderTypes() {
		if !model.IsKnownProviderType(providerType) {
			t.Errorf("%q must be known", providerType)
		}
	}
	for _, unknown := range []string{"", "yzma", "OpenAI"} {
		if model.IsKnownProviderType(unknown) {
			t.Errorf("%q must be rejected", unknown)
		}
	}
}

func TestParseModelAPIFromForm(t *testing.T) {
	t.Run("empty keeps the provider's", func(t *testing.T) {
		apiType, baseURL, err := parseModelAPIFromForm(makeRequest(map[string]string{}))
		if err != nil || apiType != "" || baseURL != "" {
			t.Errorf("got (%q, %q, %v), want empty and no error", apiType, baseURL, err)
		}
	})

	t.Run("a known type and an absolute URL are kept", func(t *testing.T) {
		apiType, baseURL, err := parseModelAPIFromForm(makeRequest(map[string]string{
			"api_type":     "anthropic",
			"api_base_url": "  https://opencode.ai/inference/go/anthropic/v1 ",
		}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if apiType != "anthropic" || baseURL != "https://opencode.ai/inference/go/anthropic/v1" {
			t.Errorf("got (%q, %q)", apiType, baseURL)
		}
	})

	for name, fields := range map[string]map[string]string{
		"unknown type":     {"api_type": "google"},
		"relative URL":     {"api_base_url": "/v1"},
		"URL without host": {"api_base_url": "https://"},
		"non-http scheme":  {"api_base_url": "ftp://example.com"},
		"unparseable URL":  {"api_base_url": "http://[::1"},
	} {
		t.Run(name+" is refused", func(t *testing.T) {
			if _, _, err := parseModelAPIFromForm(makeRequest(fields)); err == nil {
				t.Errorf("expected an error for %v", fields)
			}
		})
	}
}
