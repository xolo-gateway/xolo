package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bornholm/genai/llm"
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

// parseVerdict reads the model's answer. It tries the JSON object first, then
// falls back to a word-boundary count of category mentions across the whole
// text. The count replaces the previous first-match substring loop, which
// always picked the first category in declaration order whenever the JSON
// parse failed (so a request that mentioned every category ended up labelled
// with whichever came first in the list).
func parseVerdict(content string, categories []Category) (verdict, bool) {
	if v, ok := tryParseJSON(content, categories); ok {
		v.Confidence = clamp01(v.Confidence)
		if v.Confidence == 0 {
			v.Confidence = 0.5
		}
		return v, true
	}
	lower := strings.ToLower(content)
	best, bestN := "", 0
	for _, c := range categories {
		n := countWordMatches(lower, strings.ToLower(c.Name))
		if n > bestN {
			best, bestN = c.Name, n
		}
	}
	if bestN > 0 {
		return verdict{Category: best, Confidence: 0.5, Reason: truncate(content, 200)}, true
	}
	return verdict{}, false
}

// tryParseJSON delegates to genai, which splits the answer into balanced JSON
// blocks and runs each through json-repair. Hand-rolling this does not pay
// off: a greedy regexp swallows the surrounding prose, a non-greedy one
// rejects an object holding a nested one or a brace inside a string value, and
// neither copes with the trailing commas, single quotes and payloads cut short
// by max_tokens that small models produce.
//
// The blocks come in document order and the first one naming a configured
// category wins, so a scratchpad object the model wrote before its verdict is
// skipped rather than mistaken for it — including when that scratchpad carries
// a category of its own. Testing the block against `categories` here, rather
// than on the single block the caller gets back, is what makes that true.
// There is no preference for a fenced block: the fence is prose around the
// object, nothing more. Returns ok=false when no block names a configured
// category, which is the normal path for a prose-only answer.
func tryParseJSON(content string, categories []Category) (verdict, bool) {
	verdicts, err := llm.ParseJSON[verdict](llm.NewMessage(llm.RoleAssistant, content))
	if err != nil {
		return verdict{}, false
	}
	for _, v := range verdicts {
		if name, ok := matchCategory(v.Category, categories); ok {
			v.Category = name
			return v, true
		}
	}
	return verdict{}, false
}

// countWordMatches returns how many times needle appears in haystack as a
// whole word. Boundaries are decoded as runes, not bytes: an ASCII-only test
// takes a UTF-8 continuation byte for a boundary, so "code" would count as a
// mention inside "décode".
//
// The trade-off is that inflected forms no longer count: "coded", "maths" and
// "docs" are not mentions of "code", "math" and "doc". Loosening this would
// bring back the "encoder"/"hardcoded" false positives this path exists to
// remove, and a category the model only names in the plural is rare enough
// next to a category it names inside an unrelated word. Punctuation is a
// boundary, so "code-based" and "code's" do count.
func countWordMatches(haystack, needle string) int {
	if needle == "" {
		return 0
	}
	n, i := 0, 0
	for i < len(haystack) {
		j := strings.Index(haystack[i:], needle)
		if j < 0 {
			return n
		}
		k := i + j
		if !endsWithWordRune(haystack[:k]) && !startsWithWordRune(haystack[k+len(needle):]) {
			n++
		}
		i = k + len(needle)
	}
	return n
}

func endsWithWordRune(s string) bool {
	if s == "" {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(s)
	return isWordRune(r)
}

func startsWithWordRune(s string) bool {
	if s == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s)
	return isWordRune(r)
}

// A combining mark is part of the word it decorates. Without Mn, the decomposed
// form of "décode" ("de" + U+0301 + "code") puts a non-letter right before
// "code" and the mention counts — the precomposed form already did not.
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r)
}

// matchCategory resolves the category the model named. The comparison is exact
// apart from a trailing "s": a model that answers "docs" for a category called
// "doc" has picked a category, not made one up, and before the word-boundary
// rule landed the substring match let that through. Tolerating it here rather
// than in countWordMatches keeps it off the prose path, where a loose
// comparison is what made "encoder" count as "code".
func matchCategory(answer string, categories []Category) (string, bool) {
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer == "" {
		return "", false
	}
	for _, c := range categories {
		name := strings.ToLower(c.Name)
		if name == answer || name+"s" == answer || name == answer+"s" {
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
