package main

import (
	"context"
	"encoding/json"
	"math"

	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
	"github.com/xolo-gateway/xolo/plugins/internal/complexity"
	"github.com/xolo-gateway/xolo/plugins/internal/requesttext"
)

const PluginName = "complexity-scorer"
const PluginVersion = "0.1.0"

// Plugin scores the linguistic and structural complexity of a request from
// its text alone: length, vocabulary, entropy, nesting, explicit constraints,
// code presence. It also derives the number of output tokens such a request
// is likely to produce, which downstream estimators can consume.
type Plugin struct {
	proto.UnimplementedXoloPluginServer
}

func (p *Plugin) Describe(_ context.Context, _ *proto.DescribeRequest) (*proto.PluginDescriptor, error) {
	return &proto.PluginDescriptor{
		Name:         PluginName,
		Version:      PluginVersion,
		Description:  "Évalue la complexité lexicale et structurelle de la requête (score composite entre 0 et 1) et estime la longueur de la réponse.",
		Capabilities: []proto.PluginDescriptor_Capability{proto.PluginDescriptor_PRE_REQUEST},
		InputPorts: []*proto.PortDescriptor{
			{Name: "request", PortType: "request", Required: true},
		},
		OutputPorts: []*proto.PortDescriptor{
			{Name: "complexity", PortType: "number"},
			{Name: "level", PortType: "string"},
			{Name: "word_count", PortType: "number"},
			{Name: "estimated_output_tokens", PortType: "number"},
		},
	}, nil
}

func (p *Plugin) PreRequest(_ context.Context, in *proto.PreRequestInput) (*proto.PreRequestOutput, error) {
	score := complexity.AnalyzeDefault(requesttext.Context(in.MessagesJson))

	outputs := map[string]interface{}{
		"complexity":              score.Composite,
		"level":                   score.Level,
		"word_count":              score.Stats.TokenCount,
		"estimated_output_tokens": estimateOutputTokens(score.Stats.TokenCount, score.Composite),
	}
	b, _ := json.Marshal(outputs)

	return &proto.PreRequestOutput{Allowed: true, OutputsJson: string(b)}, nil
}

// estimateOutputTokens assumes that a more complex request calls for a longer
// answer: from a fifth of the input for trivial prompts up to one and a half
// times the input for the most demanding ones, capped at 4096 tokens.
func estimateOutputTokens(inputTokens int, composite float64) int {
	ratio := 0.2 + composite*1.3
	out := int(math.Min(float64(inputTokens)*ratio, 4096))
	if out < 64 {
		return 64
	}
	return out
}
