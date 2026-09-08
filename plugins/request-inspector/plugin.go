package main

import (
	"context"
	"encoding/json"

	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
	"github.com/xolo-gateway/xolo/plugins/internal/requesttext"
)

const PluginName = "request-inspector"
const PluginVersion = "0.1.0"

// Plugin exposes the structural facts of a request: what modalities and
// features it uses and how large it is. It performs no scoring; every output
// can be read straight from the request body.
type Plugin struct {
	proto.UnimplementedXoloPluginServer
}

func (p *Plugin) Describe(_ context.Context, _ *proto.DescribeRequest) (*proto.PluginDescriptor, error) {
	return &proto.PluginDescriptor{
		Name:         PluginName,
		Version:      PluginVersion,
		Description:  "Inspecte la structure de la requête : présence d'images, de raisonnement, d'outils, de streaming, et taille estimée du contexte.",
		Capabilities: []proto.PluginDescriptor_Capability{proto.PluginDescriptor_PRE_REQUEST},
		InputPorts: []*proto.PortDescriptor{
			{Name: "request", PortType: "request", Required: true},
		},
		OutputPorts: []*proto.PortDescriptor{
			{Name: "has_vision", PortType: "boolean"},
			{Name: "has_reasoning", PortType: "boolean"},
			{Name: "has_tools", PortType: "boolean"},
			{Name: "is_streaming", PortType: "boolean"},
			{Name: "message_count", PortType: "number"},
			{Name: "input_tokens", PortType: "number"},
			{Name: "max_tokens", PortType: "number"},
		},
	}, nil
}

func (p *Plugin) PreRequest(_ context.Context, in *proto.PreRequestInput) (*proto.PreRequestOutput, error) {
	body := parseBody(in.GetModel())

	outputs := map[string]interface{}{
		"has_vision":    requesttext.HasImage(in.MessagesJson),
		"has_reasoning": body.hasReasoning(),
		"has_tools":     len(body.Tools) > 0 || len(body.Functions) > 0,
		"is_streaming":  body.Stream,
		"message_count": countMessages(in.MessagesJson),
		"input_tokens":  requesttext.EstimateTokens(requesttext.Context(in.MessagesJson)),
		"max_tokens":    body.maxTokens(),
	}
	b, _ := json.Marshal(outputs)

	return &proto.PreRequestOutput{Allowed: true, OutputsJson: string(b)}, nil
}

// requestBody holds the top-level fields of a chat completion request that
// are relevant to routing. Reasoning flags cover the OpenAI, Anthropic and
// Qwen/vLLM conventions.
type requestBody struct {
	Stream              bool              `json:"stream"`
	Tools               []json.RawMessage `json:"tools"`
	Functions           []json.RawMessage `json:"functions"`
	MaxTokens           *int              `json:"max_tokens"`
	MaxCompletionTokens *int              `json:"max_completion_tokens"`
	ReasoningEffort     string            `json:"reasoning_effort"`
	EnableThinking      *bool             `json:"enable_thinking"`
	Thinking            *struct {
		Type string `json:"type"`
	} `json:"thinking"`
}

func parseBody(bodyJSON string) requestBody {
	var body requestBody
	if bodyJSON != "" {
		_ = json.Unmarshal([]byte(bodyJSON), &body)
	}
	return body
}

func (b requestBody) hasReasoning() bool {
	if b.Thinking != nil && b.Thinking.Type == "enabled" {
		return true
	}
	if b.EnableThinking != nil && *b.EnableThinking {
		return true
	}
	return b.ReasoningEffort != "" && b.ReasoningEffort != "none"
}

// maxTokens returns the requested completion limit, or 0 when unspecified.
func (b requestBody) maxTokens() int {
	if b.MaxCompletionTokens != nil {
		return *b.MaxCompletionTokens
	}
	if b.MaxTokens != nil {
		return *b.MaxTokens
	}
	return 0
}

func countMessages(messagesJSON string) int {
	if messagesJSON == "" {
		return 0
	}
	var messages []json.RawMessage
	if err := json.Unmarshal([]byte(messagesJSON), &messages); err != nil {
		return 0
	}
	return len(messages)
}
