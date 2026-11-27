package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
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

	messagesJSON := pluginsdk.PreRequestMessages(in)
	var messages []map[string]interface{}
	if err := json.Unmarshal([]byte(messagesJSON), &messages); err != nil {
		// Malformed MessagesJson: pass the request through untouched so the
		// client conversation is not silently replaced by a single system
		// message in replace mode. The configured prompt is skipped for this
		// call; the warning lets an operator see it happened.
		slog.WarnContext(ctx, "system-prompt: malformed messages, passing the request through", slog.Any("error", err))
		return &proto.PreRequestOutput{Allowed: true}, nil
	}

	modified, clientSystemCount := rewriteSystemPrompt(messages, cfg)

	b, _ := json.Marshal(modified)
	slog.InfoContext(ctx, "system-prompt: pre-request handled",
		slog.String("org_id", in.GetCtx().GetOrgId()),
		slog.Int("original_messages_count", len(messages)),
		slog.Int("modified_messages_count", len(modified)),
		slog.Int("client_system_messages_count", clientSystemCount),
		slog.Int("original_messages_bytes", len(messagesJSON)),
		slog.Int("modified_messages_bytes", len(b)),
		slog.Bool("append", cfg.Append),
	)

	return &proto.PreRequestOutput{
		Allowed:              true,
		ModifiedMessagesJson: string(b),
	}, nil
}

// systemPromptSeparator joins the system prompts folded into a single message.
const systemPromptSeparator = "\n\n"

// rewriteSystemPrompt returns the conversation with exactly one system
// message, at its head, whatever the mode and however many system messages
// the client sent. Both modes share this invariant so that every provider
// (some accept a single, leading system prompt only) and every downstream
// adapter see the same shape:
//
//   - replace: the system message is the configured prompt alone;
//   - append: it is every client system prompt, in order, followed by the
//     configured prompt, so the configured prompt has the last word.
//
// A client system message sent in the middle of the conversation therefore
// moves to its head. It also returns how many client system messages were
// folded or dropped.
func rewriteSystemPrompt(messages []map[string]interface{}, cfg Config) ([]map[string]interface{}, int) {
	clientPrompts, rest, count := splitSystemMessages(messages)

	content := cfg.SystemPrompt
	if cfg.Append {
		content = strings.Join(append(clientPrompts, cfg.SystemPrompt), systemPromptSeparator)
	}

	systemMsg := map[string]interface{}{
		"role":    "system",
		"content": content,
	}
	return append([]map[string]interface{}{systemMsg}, rest...), count
}

// splitSystemMessages separates the system messages from the rest of the
// conversation, keeping the order of both. It returns the non-empty text of
// each system message, the other messages, and the number of system messages
// found.
func splitSystemMessages(messages []map[string]interface{}) (prompts []string, rest []map[string]interface{}, count int) {
	rest = make([]map[string]interface{}, 0, len(messages))
	for _, msg := range messages {
		if msg["role"] != "system" {
			rest = append(rest, msg)
			continue
		}
		count++
		if text := textContent(msg["content"]); text != "" {
			prompts = append(prompts, text)
		}
	}
	return prompts, rest, count
}

// textContent returns the text of a message content, which the OpenAI
// format allows either as a plain string or as an array of content parts
// ({"type":"text","text":"..."}). It keeps the text the proxy keeps (genai's
// extractContentParts), concatenated without a separator so a folded prompt
// reads as it would untouched:
//
//   - bare strings;
//   - the "text" of a text part ("text", "input_text", "output_text");
//   - the "text" of a part that declares no type, or a type the proxy does not
//     know: the proxy classifies the first by its shape, where a "text" field
//     makes it a text part, and keeps the text of the second.
//
// A part of a known non-textual kind (an image, an audio clip, a file, a
// structural block) contributes nothing, even if it carries a "text" field.
func textContent(content interface{}) string {
	switch c := content.(type) {
	case string:
		return c
	case []interface{}:
		var b strings.Builder
		for _, part := range c {
			switch p := part.(type) {
			case string:
				b.WriteString(p)
			case map[string]interface{}:
				if partType, _ := p["type"].(string); nonTextPartTypes[partType] {
					continue
				}
				text, _ := p["text"].(string)
				b.WriteString(text)
			}
		}
		return b.String()
	default:
		return ""
	}
}

// nonTextPartTypes lists the declared content part types whose payload is
// not text for the proxy: attachments and structural blocks. Any other part
// is read for its "text" field.
var nonTextPartTypes = map[string]bool{
	"image_url": true, "input_image": true, "image": true,
	"input_audio": true, "audio": true,
	"file": true, "input_file": true, "document": true,
	"refusal": true, "thinking": true, "redacted_thinking": true, "tool_use": true, "tool_result": true,
}
