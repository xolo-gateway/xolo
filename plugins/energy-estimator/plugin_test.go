package main

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

func estimate(t *testing.T, config, inputs string) map[string]any {
	t.Helper()
	out, err := (&Plugin{}).PreRequest(context.Background(), &proto.PreRequestInput{
		Ctx:        &proto.RequestContext{ConfigJson: config},
		InputsJson: inputs,
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out.OutputsJson), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPreRequest_ScalesWithTokensAndModelSize(t *testing.T) {
	small := estimate(t, "", `{"input_tokens":100,"output_tokens":50,"model_params_b":7}`)
	big := estimate(t, "", `{"input_tokens":100,"output_tokens":50,"model_params_b":400}`)
	long := estimate(t, "", `{"input_tokens":10000,"output_tokens":2000,"model_params_b":7}`)

	if small["energy_kwh"].(float64) >= big["energy_kwh"].(float64) {
		t.Errorf("bigger model must draw more energy: %v vs %v", small["energy_kwh"], big["energy_kwh"])
	}
	if small["energy_kwh"].(float64) >= long["energy_kwh"].(float64) {
		t.Errorf("longer request must draw more energy: %v vs %v", small["energy_kwh"], long["energy_kwh"])
	}
	if small["energy_cost"].(float64) >= big["energy_cost"].(float64) {
		t.Errorf("energy_cost must follow energy: %v vs %v", small["energy_cost"], big["energy_cost"])
	}
	for _, m := range []map[string]any{small, big, long} {
		if c := m["energy_cost"].(float64); c < 0 || c > 1 {
			t.Errorf("energy_cost out of range: %v", c)
		}
	}
}

func TestPreRequest_DefaultsFromConfig(t *testing.T) {
	m := estimate(t, `{"default_model_params_b":7,"default_output_tokens":10,"tier":"hyperscaler"}`, `{"input_tokens":100}`)
	if m["energy_kwh"].(float64) <= 0 {
		t.Errorf("expected a positive estimate, got %v", m)
	}
}

func TestNormalize(t *testing.T) {
	if got := normalize(0.001, 0.001); math.Abs(got-0.5) > 1e-9 {
		t.Errorf("reference must score 0.5, got %v", got)
	}
	if normalize(0.01, 0.001) <= normalize(0.001, 0.001) {
		t.Error("more energy must score higher")
	}
	if normalize(0, 0.001) != 0 {
		t.Error("no energy scores 0")
	}
}

func TestParseConfig_Tier(t *testing.T) {
	if parseConfig(`{"tier":"small_provider"}`).tier().String() != "SmallProvider" &&
		parseConfig(`{"tier":"small_provider"}`).tier() == parseConfig("").tier() {
		t.Error("tier must be honoured")
	}
	cfg := parseConfig(`{"default_model_params_b":-1,"reference_kwh":0}`)
	if cfg.DefaultModelParamsB != 70 || cfg.ReferenceKWh != 0.001 {
		t.Errorf("invalid values must fall back to defaults: %+v", cfg)
	}
}
