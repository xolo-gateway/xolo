package main

import (
	"context"
	"encoding/json"
	"sync"

	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
	"github.com/xolo-gateway/xolo/plugins/internal/complexity"
	"github.com/xolo-gateway/xolo/plugins/internal/complexity/data"
	"github.com/xolo-gateway/xolo/plugins/internal/requesttext"
)

const PluginName = "text-classifier"
const PluginVersion = "0.2.0"

// Categories the classifier can answer, in the order they are documented.
var categories = []string{
	"analysis", "code", "conversation", "creative", "factual",
	"instruction", "math", "rewriting", "summarization", "translation",
}

// Plugin classifies the request into a thematic category without calling any
// model. It works in two layers. Lexical rules first: a request that says
// "traduis" or carries a code block is settled there with high confidence.
// Then an embedded Naive Bayes model, trained from the corpus in
// plugins/internal/complexity/data and rebuilt with train-classifier, decides
// the rest and reports how clearly the winning class stands out. Below the
// configured confidence the answer is "unknown" rather than a guess.
type Plugin struct {
	proto.UnimplementedXoloPluginServer

	once    sync.Once
	model   *complexity.NaiveBayes
	loadErr error
}

func (p *Plugin) Describe(_ context.Context, _ *proto.DescribeRequest) (*proto.PluginDescriptor, error) {
	return &proto.PluginDescriptor{
		Name:         PluginName,
		Version:      PluginVersion,
		Description:  "Classe la requête dans une catégorie thématique (code, traduction, résumé, maths, rédaction…) par règles lexicales puis modèle bayésien embarqué, sans appel à un LLM.",
		Capabilities: []proto.PluginDescriptor_Capability{proto.PluginDescriptor_PRE_REQUEST},
		InputPorts: []*proto.PortDescriptor{
			{Name: "request", PortType: "request", Required: true},
		},
		OutputPorts: []*proto.PortDescriptor{
			{Name: "category", PortType: "string"},
			{Name: "confidence", PortType: "number"},
			{Name: "source", PortType: "string"},
		},
		ConfigSchema: configSchemaJSON,
	}, nil
}

func (p *Plugin) PreRequest(_ context.Context, in *proto.PreRequestInput) (*proto.PreRequestOutput, error) {
	cfg := parseConfig(in.GetCtx().GetConfigJson())
	text := requesttext.LastUserTurn(in.MessagesJson)

	category, confidence, source := p.classify(text, cfg)

	b, _ := json.Marshal(map[string]interface{}{
		"category":   category,
		"confidence": confidence,
		"source":     source,
	})
	return &proto.PreRequestOutput{Allowed: true, OutputsJson: string(b)}, nil
}

// classify returns the category, a confidence in [0, 1] and which layer
// decided: "rule", "model", "heuristic" or "none".
func (p *Plugin) classify(text string, cfg Config) (string, float64, string) {
	if text == "" {
		return "unknown", 0, "none"
	}

	if cfg.UseRules {
		if c := applyRules(text); c != "" {
			return c, cfg.RuleConfidence, "rule"
		}
	}

	if nb := p.classifier(); nb != nil {
		preds := nb.PredictTopK(text, 2)
		if len(preds) > 0 {
			// The margin over the runner-up is what tells a clear call from a
			// coin toss; the winner's probability alone hides that difference.
			margin := preds[0].Prob
			if len(preds) > 1 {
				margin = preds[0].Prob - preds[1].Prob
			}
			if margin >= cfg.MinConfidence {
				return preds[0].Class, margin, "model"
			}
			return "unknown", margin, "model"
		}
	}

	// No informative word at all: a greeting, an acknowledgement.
	if wordCount(text) <= shortChatMaxWords {
		return "conversation", 0.5, "heuristic"
	}
	return "unknown", 0, "none"
}

func (p *Plugin) classifier() *complexity.NaiveBayes {
	p.once.Do(func() {
		p.model, p.loadErr = complexity.LoadModel(data.RawModel)
	})
	if p.loadErr != nil {
		return nil
	}
	return p.model
}

// ─── Config ───────────────────────────────────────────────────────────────────

type Config struct {
	// UseRules enables the lexical rules layer (default true).
	UseRules bool `json:"use_rules"`
	// RuleConfidence is the confidence reported when a rule decides.
	RuleConfidence float64 `json:"rule_confidence"`
	// MinConfidence is the margin below which the model's answer becomes "unknown".
	MinConfidence float64 `json:"min_confidence"`
}

func defaultConfig() Config {
	return Config{UseRules: true, RuleConfidence: 0.9, MinConfidence: 0.05}
}

func parseConfig(raw string) Config {
	cfg := defaultConfig()
	if raw == "" || raw == "{}" {
		return cfg
	}
	// use_rules must keep its default when absent, which a plain Unmarshal
	// into a bool cannot express; read it through a pointer.
	var aux struct {
		UseRules       *bool    `json:"use_rules"`
		RuleConfidence *float64 `json:"rule_confidence"`
		MinConfidence  *float64 `json:"min_confidence"`
	}
	if err := json.Unmarshal([]byte(raw), &aux); err != nil {
		return cfg
	}
	if aux.UseRules != nil {
		cfg.UseRules = *aux.UseRules
	}
	if aux.RuleConfidence != nil && *aux.RuleConfidence >= 0 && *aux.RuleConfidence <= 1 {
		cfg.RuleConfidence = *aux.RuleConfidence
	}
	if aux.MinConfidence != nil && *aux.MinConfidence >= 0 && *aux.MinConfidence <= 1 {
		cfg.MinConfidence = *aux.MinConfidence
	}
	return cfg
}

const configSchemaJSON = `{
  "type": "object",
  "properties": {
    "use_rules": {
      "type": "boolean",
      "title": "Règles lexicales avant le modèle",
      "description": "Tranche immédiatement les demandes explicites (traduction, résumé, code…).",
      "default": true
    },
    "rule_confidence": {
      "type": "number",
      "title": "Confiance attribuée à une règle",
      "minimum": 0, "maximum": 1, "default": 0.9
    },
    "min_confidence": {
      "type": "number",
      "title": "Marge minimale du modèle",
      "description": "En dessous, la catégorie devient « unknown » plutôt qu'une supposition.",
      "minimum": 0, "maximum": 1, "default": 0.05
    }
  }
}`
