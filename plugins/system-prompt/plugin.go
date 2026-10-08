package main

import (
	"context"
	"encoding/json"
	"log/slog"

	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

type Plugin struct {
	proto.UnimplementedXoloPluginServer
}

func (p *Plugin) Describe(_ context.Context, _ *proto.DescribeRequest) (*proto.PluginDescriptor, error) {
	return &proto.PluginDescriptor{
		Name:         "system-prompt",
		Version:      "0.1.0",
		Description:  "Injecte un prompt système fixe dans les messages de requête.",
		Capabilities: []proto.PluginDescriptor_Capability{proto.PluginDescriptor_PRE_REQUEST},
		InputPorts:   []*proto.PortDescriptor{{Name: "request", PortType: "request"}},
		OutputPorts:  []*proto.PortDescriptor{{Name: "request", PortType: "request"}},
	}, nil
}

func (p *Plugin) PreRequest(ctx context.Context, in *proto.PreRequestInput) (*proto.PreRequestOutput, error) {

	cfg := parseConfig(in.GetCtx().GetConfigJson())
	if cfg.SystemPrompt == "" {
		slog.Info("system-prompt: no system prompt configured, skipping")
		return &proto.PreRequestOutput{Allowed: true}, nil
	}

	var messages []map[string]interface{}
	json.Unmarshal([]byte(in.GetMessagesJson()), &messages) //nolint:errcheck

	var modified []map[string]interface{}

	if cfg.Append {
		// Append mode: add to existing system prompt if present
		found := false
		for i, msg := range messages {
			if msg["role"] == "system" {
				existing, _ := msg["content"].(string)
				messages[i]["content"] = existing + "\n\n" + cfg.SystemPrompt
				found = true
				break
			}
		}
		if !found {
			// No existing system message, prepend
			systemMsg := map[string]interface{}{
				"role":    "system",
				"content": cfg.SystemPrompt,
			}
			modified = append([]map[string]interface{}{systemMsg}, messages...)
		} else {
			modified = messages
		}
	} else {
		// Replace mode: prepend (current behavior)
		systemMsg := map[string]interface{}{
			"role":    "system",
			"content": cfg.SystemPrompt,
		}
		modified = append([]map[string]interface{}{systemMsg}, messages...)
	}

	b, _ := json.Marshal(modified)
	slog.InfoContext(ctx, "system-prompt: pre-request handled",
		slog.String("org_id", in.GetCtx().GetOrgId()),
		slog.Int("original_messages_count", len(messages)),
		slog.Int("modified_messages_count", len(modified)),
		slog.Int("original_messages_bytes", len(in.GetMessagesJson())),
		slog.Int("modified_messages_bytes", len(b)),
		slog.Bool("append", cfg.Append),
	)
	slog.DebugContext(ctx, "system-prompt: pre-request messages",
		slog.String("original_messages", in.GetMessagesJson()),
		slog.String("modified_messages", string(b)))

	return &proto.PreRequestOutput{
		Allowed:              true,
		ModifiedMessagesJson: string(b),
	}, nil
}
