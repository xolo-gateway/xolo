package main

import (
	"context"
	"encoding/json"
	"math"

	"github.com/xolo-gateway/xolo/internal/estimator"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

const PluginName = "energy-estimator"
const PluginVersion = "0.1.0"

// Plugin estimates the energy an inference will draw from the number of
// tokens it processes and the size of the model that will run it. The model
// size and the hosting tier come from the input ports, falling back to the
// node configuration, so the same node can be fed by a static value or by an
// upstream router.
type Plugin struct {
	proto.UnimplementedXoloPluginServer
}

func (p *Plugin) Describe(_ context.Context, _ *proto.DescribeRequest) (*proto.PluginDescriptor, error) {
	return &proto.PluginDescriptor{
		Name:         PluginName,
		Version:      PluginVersion,
		Description:  "Estime l'énergie consommée par l'inférence (Wh, kWh, durée) à partir du nombre de tokens et de la taille du modèle cible, et la normalise en un coût entre 0 et 1.",
		Capabilities: []proto.PluginDescriptor_Capability{proto.PluginDescriptor_PRE_REQUEST},
		InputPorts: []*proto.PortDescriptor{
			{Name: "input_tokens", PortType: "number", Required: true},
			{Name: "output_tokens", PortType: "number"},
			{Name: "model_params_b", PortType: "number"},
		},
		OutputPorts: []*proto.PortDescriptor{
			{Name: "energy_kwh", PortType: "number"},
			{Name: "energy_wh", PortType: "number"},
			{Name: "duration_ms", PortType: "number"},
			{Name: "energy_cost", PortType: "number"},
		},
		ConfigSchema: configSchemaJSON,
	}, nil
}

func (p *Plugin) PreRequest(_ context.Context, in *proto.PreRequestInput) (*proto.PreRequestOutput, error) {
	cfg := parseConfig(in.GetCtx().GetConfigJson())

	var inputs map[string]interface{}
	if in.InputsJson != "" {
		_ = json.Unmarshal([]byte(in.InputsJson), &inputs)
	}

	inputTokens := intInput(inputs, "input_tokens", 0)
	outputTokens := intInput(inputs, "output_tokens", cfg.DefaultOutputTokens)
	params := floatInput(inputs, "model_params_b", cfg.DefaultModelParamsB)

	est := estimator.NewCloudEstimator(cfg.tier())
	rng := est.EstimateFromParams(params, estimator.InferenceRequest{
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}, 0, 0)

	outputs := map[string]interface{}{
		"energy_kwh":  rng.Mid.TotalKWh,
		"energy_wh":   rng.Mid.TotalWh,
		"duration_ms": rng.Mid.DurationMs,
		"energy_cost": normalize(rng.Mid.TotalKWh, cfg.ReferenceKWh),
	}
	b, _ := json.Marshal(outputs)

	return &proto.PreRequestOutput{Allowed: true, OutputsJson: string(b)}, nil
}

// normalize maps an energy amount onto [0, 1] with a logistic curve centred
// on the reference: the reference amount scores 0.5, ten times more scores
// about 0.82 and ten times less about 0.18. Log scale keeps the score readable
// across the several orders of magnitude a gateway sees in one day.
func normalize(kwh, referenceKWh float64) float64 {
	if kwh <= 0 || referenceKWh <= 0 {
		return 0
	}
	x := math.Log10(kwh / referenceKWh)
	return 1 / (1 + math.Exp(-1.5*x))
}

func intInput(inputs map[string]interface{}, name string, fallback int) int {
	if v, ok := inputs[name]; ok {
		if f, ok := toFloat(v); ok && f > 0 {
			return int(f)
		}
	}
	return fallback
}

func floatInput(inputs map[string]interface{}, name string, fallback float64) float64 {
	if v, ok := inputs[name]; ok {
		if f, ok := toFloat(v); ok && f > 0 {
			return f
		}
	}
	return fallback
}

func toFloat(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// ─── Config ───────────────────────────────────────────────────────────────────

type Config struct {
	// Tier names the hosting infrastructure: hyperscaler, major_cloud or small_provider.
	Tier string `json:"tier"`
	// DefaultModelParamsB is the model size (in billions of active parameters)
	// used when the model_params_b port is not connected.
	DefaultModelParamsB float64 `json:"default_model_params_b"`
	// DefaultOutputTokens is used when the output_tokens port is not connected.
	DefaultOutputTokens int `json:"default_output_tokens"`
	// ReferenceKWh is the energy amount that maps to an energy_cost of 0.5.
	ReferenceKWh float64 `json:"reference_kwh"`
}

func defaultConfig() Config {
	return Config{
		Tier:                "major_cloud",
		DefaultModelParamsB: 70,
		DefaultOutputTokens: 512,
		// Roughly one 70B-class inference of a few hundred tokens.
		ReferenceKWh: 0.001,
	}
}

func parseConfig(raw string) Config {
	cfg := defaultConfig()
	if raw != "" && raw != "{}" {
		_ = json.Unmarshal([]byte(raw), &cfg)
	}
	if cfg.DefaultModelParamsB <= 0 {
		cfg.DefaultModelParamsB = 70
	}
	if cfg.DefaultOutputTokens <= 0 {
		cfg.DefaultOutputTokens = 512
	}
	if cfg.ReferenceKWh <= 0 {
		cfg.ReferenceKWh = 0.001
	}
	return cfg
}

func (c Config) tier() estimator.CloudTier {
	switch c.Tier {
	case "hyperscaler":
		return estimator.TierHyperscaler
	case "small_provider":
		return estimator.TierSmallProvider
	default:
		return estimator.TierMajorCloud
	}
}

const configSchemaJSON = `{
  "type": "object",
  "properties": {
    "tier": {
      "type": "string",
      "title": "Infrastructure d'hébergement",
      "enum": ["hyperscaler", "major_cloud", "small_provider"],
      "default": "major_cloud"
    },
    "default_model_params_b": {
      "type": "number",
      "title": "Taille du modèle par défaut (milliards de paramètres actifs)",
      "default": 70
    },
    "default_output_tokens": {
      "type": "integer",
      "title": "Tokens de sortie par défaut",
      "default": 512
    },
    "reference_kwh": {
      "type": "number",
      "title": "Énergie de référence (kWh) correspondant à un coût de 0.5",
      "default": 0.001
    }
  }
}`
