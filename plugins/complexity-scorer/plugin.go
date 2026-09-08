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
const PluginVersion = "0.2.0"

// Plugin scores how demanding a request is from its text alone.
//
// Two things are measured separately because they call for different
// decisions. The complexity of what the user is asking now, read on the last
// user turn: constraints, structure, code, vocabulary. And the size of the
// conversation the model has to read, exposed as context_tokens. A long,
// banal chat is not a hard request, and a one-line question about a pasted
// stack trace is.
type Plugin struct {
	proto.UnimplementedXoloPluginServer
}

func (p *Plugin) Describe(_ context.Context, _ *proto.DescribeRequest) (*proto.PluginDescriptor, error) {
	return &proto.PluginDescriptor{
		Name:         PluginName,
		Version:      PluginVersion,
		Description:  "Évalue la complexité de la demande courante (score entre 0 et 1 : contraintes, structure, code, vocabulaire) et mesure séparément la taille du contexte.",
		Capabilities: []proto.PluginDescriptor_Capability{proto.PluginDescriptor_PRE_REQUEST},
		InputPorts: []*proto.PortDescriptor{
			{Name: "request", PortType: "request", Required: true},
		},
		OutputPorts: []*proto.PortDescriptor{
			{Name: "complexity", PortType: "number"},
			{Name: "level", PortType: "string"},
			{Name: "has_code", PortType: "boolean"},
			{Name: "constraint_count", PortType: "number"},
			{Name: "word_count", PortType: "number"},
			{Name: "context_tokens", PortType: "number"},
			{Name: "estimated_output_tokens", PortType: "number"},
		},
	}, nil
}

func (p *Plugin) PreRequest(_ context.Context, in *proto.PreRequestInput) (*proto.PreRequestOutput, error) {
	turn := complexity.AnalyzeDefault(requesttext.LastUserTurn(in.MessagesJson))
	contextTokens := requesttext.EstimateTokens(requesttext.Context(in.MessagesJson))

	outputs := map[string]interface{}{
		"complexity":              turn.Composite,
		"level":                   turn.Level,
		"has_code":                turn.Stats.HasCode,
		"constraint_count":        turn.Stats.ConstraintCount,
		"word_count":              turn.Stats.TokenCount,
		"context_tokens":          contextTokens,
		"estimated_output_tokens": estimateOutputTokens(turn.Stats.TokenCount, turn.Composite),
	}
	b, _ := json.Marshal(outputs)

	return &proto.PreRequestOutput{Allowed: true, OutputsJson: string(b)}, nil
}

// estimateOutputTokens assumes that a more complex request calls for a longer
// answer. The estimate grows with the request's own length and with its
// complexity: a trivial question gets a short answer whatever its size, a
// demanding one gets up to about 1.5 times its length, floored at 64 tokens
// and capped at 4096.
func estimateOutputTokens(words int, composite float64) int {
	base := 64 + composite*1024
	scaled := float64(words) * (0.2 + composite*1.3)
	out := int(math.Min(math.Max(base, scaled), 4096))
	if out < 64 {
		return 64
	}
	return out
}
