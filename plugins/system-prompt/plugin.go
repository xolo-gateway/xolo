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

func (p *Plugin) PreRequest(_ context.Context, in *proto.PreRequestInput) (*proto.PreRequestOutput, error) {

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
		// Replace mode: remove any existing system messages, then prepend the configured one
		filtered := make([]map[string]interface{}, 0, len(messages))
		for _, msg := range messages {
			if msg["role"] != "system" {
				filtered = append(filtered, msg)
			}
		}
		systemMsg := map[string]interface{}{
			"role":    "system",
			"content": cfg.SystemPrompt,
		}
		modified = append([]map[string]interface{}{systemMsg}, filtered...)
	}

	b, _ := json.Marshal(modified)
	slog.Info("system-prompt: modified messages",
		slog.String("original_messages", in.GetMessagesJson()),
		slog.String("modified_messages", string(b)))

	return &proto.PreRequestOutput{
		Allowed:              true,
		ModifiedMessagesJson: string(b),
	}, nil
}
