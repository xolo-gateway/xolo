package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

const PluginName = "dummy-model"
const PluginVersion = "0.2.0"

const configSchemaJSON = `{
  "type": "object",
  "properties": {
    "response_template": {
      "type": "string",
      "title": "Modèle de réponse",
      "description": "Texte Markdown retourné à la place du LLM. Placeholders : {{.User}}, {{.LastMessage}}."
    }
  }
}`

// Plugin implements the dummy-model gRPC plugin.
type Plugin struct {
	proto.UnimplementedXoloPluginServer
}

func (p *Plugin) Describe(_ context.Context, _ *proto.DescribeRequest) (*proto.PluginDescriptor, error) {
	return &proto.PluginDescriptor{
		Name:         PluginName,
		Version:      PluginVersion,
		Description:  "Retourne une réponse forgée à la place du LLM, à des fins de test. Supporte les modes streaming et non-streaming.",
		Capabilities: []proto.PluginDescriptor_Capability{proto.PluginDescriptor_RESOLVE_MODEL},
		ConfigSchema: configSchemaJSON,
		InputPorts: []*proto.PortDescriptor{
			{Name: "request", PortType: "request", Required: true},
		},
		OutputPorts: []*proto.PortDescriptor{
			{Name: "response", PortType: "response", Required: true},
		},
	}, nil
}

// ResolveModel always returns a forged ResponseContent — it never calls a real LLM.
// The pipeline engine converts this into a DummyLLMClient transparently.
func (p *Plugin) ResolveModel(ctx context.Context, in *proto.ResolveModelInput) (*proto.ResolveModelOutput, error) {
	cfg, err := ParseConfig(in.GetCtx().GetConfigJson())
	if err != nil {
		slog.WarnContext(ctx, "dummy-model: failed to parse config, using defaults", slog.Any("error", err))
		cfg = Config{}
	}

	user := in.GetCtx().GetDisplayName()
	if user == "" {
		user = in.GetCtx().GetUserId()
	}
	if user == "" {
		user = "(inconnu)"
	}

	lastMessage := extractLastUserMessage(pluginsdk.ResolveModelMessages(in))
	content := applyTemplate(cfg.template(), user, lastMessage)

	return &proto.ResolveModelOutput{ResponseContent: content}, nil
}

func applyTemplate(tmpl, user, lastMessage string) string {
	s := strings.ReplaceAll(tmpl, "{{.User}}", user)
	s = strings.ReplaceAll(s, "{{.LastMessage}}", lastMessage)
	return s
}

func extractLastUserMessage(messagesJSON string) string {
	if messagesJSON == "" {
		return "(aucun message)"
	}
	var messages []struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}
	if err := json.Unmarshal([]byte(messagesJSON), &messages); err != nil {
		return "(impossible de lire le message)"
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "user" {
			continue
		}
		switch v := messages[i].Content.(type) {
		case string:
			return v
		default:
			if b, err := json.Marshal(v); err == nil {
				return fmt.Sprintf("%s", b)
			}
		}
	}
	return "(aucun message utilisateur)"
}
