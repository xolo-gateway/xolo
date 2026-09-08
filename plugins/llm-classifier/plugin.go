package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
	"github.com/xolo-gateway/xolo/plugins/internal/requesttext"
)

const PluginName = "llm-classifier"
const PluginVersion = "0.1.0"

// Plugin classifies the request by asking a model of the organisation, through
// the gateway itself. The categories and their meaning are configured on the
// node, so the same plugin can sort requests by topic, by sensitivity, by
// department or by anything a sentence can describe. The model to use comes
// from the model_name port when connected (a model value node, a router) and
// from the configuration otherwise. Point it at a small, cheap model: the
// call happens before the request is served and adds its latency to it.
type Plugin struct {
	proto.UnimplementedXoloPluginServer

	hostMu sync.Mutex
	host   pluginsdk.HostClient
}

// SetHostClient implements pluginsdk.HostClientSetter.
func (p *Plugin) SetHostClient(c pluginsdk.HostClient) {
	p.hostMu.Lock()
	defer p.hostMu.Unlock()
	p.host = c
}

func (p *Plugin) hostClient() pluginsdk.HostClient {
	p.hostMu.Lock()
	defer p.hostMu.Unlock()
	return p.host
}

func (p *Plugin) Describe(_ context.Context, _ *proto.DescribeRequest) (*proto.PluginDescriptor, error) {
	return &proto.PluginDescriptor{
		Name:         PluginName,
		Version:      PluginVersion,
		Description:  "Classe la requête en demandant à un modèle de l'organisation (via la passerelle) de choisir parmi des catégories décrites dans la configuration.",
		Capabilities: []proto.PluginDescriptor_Capability{proto.PluginDescriptor_PRE_REQUEST},
		InputPorts: []*proto.PortDescriptor{
			{Name: "request", PortType: "request", Required: true},
			{Name: "model_name", PortType: "string"},
		},
		OutputPorts: []*proto.PortDescriptor{
			{Name: "category", PortType: "string"},
			{Name: "confidence", PortType: "number"},
			{Name: "reason", PortType: "string"},
			{Name: "error", PortType: "string"},
		},
		ConfigSchema: configSchemaJSON,
	}, nil
}

func (p *Plugin) PreRequest(ctx context.Context, in *proto.PreRequestInput) (*proto.PreRequestOutput, error) {
	cfg := parseConfig(in.GetCtx().GetConfigJson())
	modelName := cfg.Model
	if v := inputString(in.InputsJson, "model_name"); v != "" {
		modelName = v
	}

	result := p.classify(ctx, in, cfg, modelName)

	b, _ := json.Marshal(map[string]interface{}{
		"category":   result.Category,
		"confidence": result.Confidence,
		"reason":     result.Reason,
		"error":      result.Error,
	})
	return &proto.PreRequestOutput{Allowed: true, OutputsJson: string(b)}, nil
}

type verdict struct {
	Category   string  `json:"category"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
	Error      string  `json:"-"`
}

func (p *Plugin) classify(ctx context.Context, in *proto.PreRequestInput, cfg Config, modelName string) verdict {
	fallback := verdict{Category: cfg.FallbackCategory}

	host := p.hostClient()
	switch {
	case host == nil:
		fallback.Error = "host client not connected"
		return fallback
	case modelName == "":
		fallback.Error = "no model configured: connect model_name or set the model in the configuration"
		return fallback
	case len(cfg.Categories) == 0:
		fallback.Error = "no categories configured"
		return fallback
	}

	text := requesttext.LastUserTurn(in.MessagesJson)
	if text == "" {
		fallback.Error = "empty request"
		return fallback
	}

	callCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSeconds*float64(time.Second)))
	defer cancel()

	resp, err := host.ChatCompletion(callCtx, &proto.HostChatCompletionRequest{
		OrgId:        in.GetCtx().GetOrgId(),
		UserId:       in.GetCtx().GetUserId(),
		Model:        modelName,
		Messages:     buildMessages(cfg, text, in.MessagesJson),
		Temperature:  cfg.Temperature,
		MaxTokens:    int32(cfg.MaxTokens),
		JsonResponse: true,
	})
	if err != nil {
		fallback.Error = fmt.Sprintf("model call failed: %v", err)
		return fallback
	}

	v, ok := parseVerdict(resp.GetContent(), cfg.Categories)
	if !ok {
		fallback.Error = "model answer did not name a known category"
		fallback.Reason = truncate(resp.GetContent(), 200)
		return fallback
	}
	return v
}

// buildMessages writes the classification prompt: the categories with their
// descriptions, the answer format, then the text to classify. When configured,
// a short excerpt of the earlier conversation gives the model the context a
// follow-up question needs ("and in Rust?").
func buildMessages(cfg Config, text, messagesJSON string) []*proto.ChatMessage {
	var sb strings.Builder
	sb.WriteString("You classify user requests sent to an AI assistant. Choose exactly one category from the list below.\n\nCategories:\n")
	for _, c := range cfg.Categories {
		sb.WriteString("- ")
		sb.WriteString(c.Name)
		if c.Description != "" {
			sb.WriteString(": ")
			sb.WriteString(c.Description)
		}
		sb.WriteString("\n")
	}
	if cfg.Instructions != "" {
		sb.WriteString("\nAdditional instructions:\n")
		sb.WriteString(cfg.Instructions)
		sb.WriteString("\n")
	}
	sb.WriteString("\nAnswer with a single JSON object and nothing else: ")
	sb.WriteString(`{"category": "<one of the names above>", "confidence": <number between 0 and 1>, "reason": "<one short sentence>"}`)

	var user strings.Builder
	if cfg.IncludeHistory {
		if history := historyExcerpt(messagesJSON, cfg.MaxContextChars/2); history != "" {
			user.WriteString("Earlier in the conversation:\n")
			user.WriteString(history)
			user.WriteString("\n\n")
		}
	}
	user.WriteString("Request to classify:\n")
	user.WriteString(truncate(text, cfg.MaxContextChars))

	return []*proto.ChatMessage{
		{Role: "system", Content: sb.String()},
		{Role: "user", Content: user.String()},
	}
}

// historyExcerpt returns the conversation before the last user turn, most
// recent messages first, cut to the given size.
func historyExcerpt(messagesJSON string, maxChars int) string {
	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal([]byte(messagesJSON), &messages); err != nil || len(messages) < 2 {
		return ""
	}
	// Drop the last user message: it is the text being classified.
	last := len(messages) - 1
	for last >= 0 && messages[last].Role != "user" {
		last--
	}
	var parts []string
	total := 0
	for i := last - 1; i >= 0 && total < maxChars; i-- {
		var content string
		if err := json.Unmarshal(messages[i].Content, &content); err != nil || content == "" {
			continue
		}
		line := messages[i].Role + ": " + truncate(content, 300)
		parts = append([]string{line}, parts...)
		total += len(line)
	}
	return strings.Join(parts, "\n")
}

var reJSONObject = regexp.MustCompile(`(?s)\{.*\}`)

// parseVerdict reads the model's answer. It accepts a JSON object, possibly
// wrapped in prose or code fences, and falls back to spotting a category name
// in the raw text, since small models do not always honour the format.
func parseVerdict(content string, categories []Category) (verdict, bool) {
	var v verdict
	if m := reJSONObject.FindString(content); m != "" {
		if err := json.Unmarshal([]byte(m), &v); err == nil && v.Category != "" {
			if name, ok := matchCategory(v.Category, categories); ok {
				v.Category = name
				v.Confidence = clamp01(v.Confidence)
				if v.Confidence == 0 {
					v.Confidence = 0.5
				}
				return v, true
			}
		}
	}
	lower := strings.ToLower(content)
	for _, c := range categories {
		if strings.Contains(lower, strings.ToLower(c.Name)) {
			return verdict{Category: c.Name, Confidence: 0.5, Reason: truncate(content, 200)}, true
		}
	}
	return verdict{}, false
}

func matchCategory(answer string, categories []Category) (string, bool) {
	answer = strings.TrimSpace(strings.ToLower(answer))
	for _, c := range categories {
		if strings.ToLower(c.Name) == answer {
			return c.Name, true
		}
	}
	return "", false
}

func inputString(inputsJSON, name string) string {
	if inputsJSON == "" {
		return ""
	}
	var inputs map[string]interface{}
	if err := json.Unmarshal([]byte(inputsJSON), &inputs); err != nil {
		return ""
	}
	s, _ := inputs[name].(string)
	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}
