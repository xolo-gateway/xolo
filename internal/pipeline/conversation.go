package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/bornholm/genai/llm"
	"github.com/pkg/errors"
)

// ToolTrigger says when a conversation tool runs.
type ToolTrigger int

const (
	// ToolTriggerAlways runs the tool once, before the first model call, on
	// every request whose last user message its arguments can be mapped from.
	ToolTriggerAlways ToolTrigger = iota + 1
)

// ConversationTool is a tool a node wants resolved inside the conversation,
// before the model is called, instead of being offered to it as a callable
// tool. A model cannot be relied on to call a tool on every request — a
// retrieval step it sometimes skips produces a confident answer with nothing
// behind it — so a node that knows the call belongs at the start of the
// conversation asks the host to make it.
//
// It is an llm.Tool, so the host resolves it with the same Execute path, the
// same retries and the same result shape as a tool the model called.
type ConversationTool struct {
	// Tool is the tool to resolve, normally also advertised as a callable tool
	// by the same node. Its description is what the model reads on the
	// injected result, so it says what the data is, not how to fetch it.
	Tool llm.Tool
	// Trigger says when the host runs it. The zero value is treated as
	// ToolTriggerAlways.
	Trigger ToolTrigger
	// Arguments is the JSON object the tool is called with, before templating.
	// A string value may hold a placeholder, see FillTemplate.
	Arguments string
	// ResultTemplate, when non-empty, wraps the tool's result before it is
	// injected: each "{{result}}" occurrence becomes the result text.
	ResultTemplate string
	// Label, when non-empty, opens the injected block. Plain text.
	Label string
}

// ConversationToolSet collects the conversation tools of one execution. It is
// shared by pointer through the ExecutionContext, like ToolInspectorSet, so a
// node running early can register one that the tool loop, built later on the
// resolved client, will run.
type ConversationToolSet struct {
	tools []ConversationTool
}

// NewConversationToolSet returns an empty set.
func NewConversationToolSet() *ConversationToolSet { return &ConversationToolSet{} }

// Register adds a conversation tool. Nil tools and a nil set are ignored.
func (s *ConversationToolSet) Register(tools ...ConversationTool) {
	if s == nil {
		return
	}
	for _, t := range tools {
		if t.Tool == nil {
			continue
		}
		if t.Trigger == 0 {
			t.Trigger = ToolTriggerAlways
		}
		s.tools = append(s.tools, t)
	}
}

// Always returns the registered tools due on every request, in registration
// order.
func (s *ConversationToolSet) Always() []ConversationTool {
	if s == nil {
		return nil
	}
	out := make([]ConversationTool, 0, len(s.tools))
	for _, t := range s.tools {
		if t.Trigger == ToolTriggerAlways {
			out = append(out, t)
		}
	}
	return out
}

// Empty reports whether no conversation tool is registered.
func (s *ConversationToolSet) Empty() bool {
	return s == nil || len(s.tools) == 0
}

// ConversationTools is the value ExecutionContext.ConversationTools holds.
// Tools are registered during the forward pass; Values is what a node that
// reads the request at PreRequest time resolved (an MCP resource, say) and
// wants injected with them, in the same block, under its own label.
type ConversationTools struct {
	Tools  *ConversationToolSet
	Values []ConversationValue

	// injected records that the prefetch ran for this execution, so it runs
	// once however many tool loops the graph stacks (one per tool-provider
	// node): each would otherwise resolve the same tools again and inject their
	// results a second time.
	injected bool
}

// takeInjection claims the prefetch of this execution, reporting false when
// another tool loop of the same execution already ran it.
//
// A plain flag rather than a sync.Once: the ExecutionContext travels by value
// through nested pipeline runs, so a copied Once would already be marked done
// and the prefetch of the nested run would be skipped.
func (c *ConversationTools) takeInjection() bool {
	if c == nil || c.injected {
		return false
	}
	c.injected = true
	return true
}

// ConversationValue is a piece of context a node resolved before the model is
// called, injected next to the conversation tools' results.
type ConversationValue struct {
	// Label opens the value's block in the injected message.
	Label string
	// Content is the value itself, as text.
	Content string
}

// NewConversationTools returns an empty holder.
func NewConversationTools() *ConversationTools {
	return &ConversationTools{Tools: NewConversationToolSet()}
}

// SessionKey seeds where the gateway keeps what belongs to a conversation
// rather than to a request (a plugin's MCP session, for one). It is stable for
// a conversation across requests, unlike a request ID, and empty for a call
// that belongs to none.
//
// Callers set it when they know better — the proxy hook adapter uses the
// request's session header — this function only derives one from the caller
// identity so that a graph run outside the proxy still gets a stable scope.
func (ec ExecutionContext) SessionKey() string {
	if ec.SessionID != "" {
		return ec.SessionID
	}
	if ec.UserID == "" && ec.OrgID == "" {
		return ""
	}
	return ec.OrgID + "/" + ec.UserID
}

// FillTemplate replaces the PLACEHOLDERS of template with values, and reports
// whether one of them was missing.
//
// The placeholders a template may use are the ones a tool's arguments are
// mapped from:
//
//	{{last_user_message}}  text of the last message the user wrote
//	{{user_id}} {{org_id}} {{display_name}}  the caller
//	{{session_id}}  the conversation's key, see SessionKey
//
// A missing value is reported rather than silently replaced by an empty
// string: a retrieval tool called with an empty query answers with nothing,
// which the model then has to make sense of. Lookups are not chained, so a
// value holding "{{...}}" is never expanded again.
func FillTemplate(template string, values map[string]string) (string, bool) {
	missing := false
	out := placeholders.ReplaceAllStringFunc(template, func(match string) string {
		name := strings.TrimSpace(match[2 : len(match)-2])
		value, ok := values[name]
		if !ok || value == "" {
			missing = true
			return match
		}
		return value
	})
	return out, missing
}

// TemplateValues builds the values FillTemplate substitutes from the request:
// the caller identity, the conversation key and the last user message.
func (ec ExecutionContext) TemplateValues(messages []llm.Message) map[string]string {
	return map[string]string{
		"last_user_message": lastUserMessageText(messages),
		"user_id":           ec.UserID,
		"org_id":            ec.OrgID,
		"display_name":      ec.DisplayName,
		"session_id":        ec.SessionKey(),
	}
}

// lastUserMessageText returns the text of the last message the user wrote,
// which is the request the model is about to answer.
func lastUserMessageText(messages []llm.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role() != llm.RoleUser {
			continue
		}
		if text := strings.TrimSpace(messages[i].Content()); text != "" {
			return text
		}
	}
	return ""
}

var placeholders = regexp.MustCompile(`\{\{[^{}]*\}\}`)

// prefetchConversationTools runs the conversation tools due on this request and
// returns the message carrying their results, or an empty text when there is
// nothing to inject.
//
// Failures are part of the injected text rather than the request's outcome, for
// the reason tool call failures are: an MCP server having a bad minute should
// leave the model able to answer, not turn the request into an error.
func (c *ToolLoopClient) prefetchConversationTools(ctx context.Context, messages []llm.Message) (string, error) {
	if c.conversation == nil || !c.conversation.takeInjection() {
		return "", nil
	}

	var blocks []string
	for _, v := range c.conversation.Values {
		if v.Content == "" {
			continue
		}
		blocks = append(blocks, conversationBlock(v.Label, v.Content))
	}

	values := c.ec.TemplateValues(messages)
	for _, tool := range c.conversation.Tools.Always() {
		block, err := c.runConversationTool(ctx, tool, values)
		if err != nil {
			return "", err
		}
		if block != "" {
			blocks = append(blocks, block)
		}
	}
	if len(blocks) == 0 {
		return "", nil
	}
	return strings.Join(blocks, "\n\n"), nil
}

// runConversationTool resolves one conversation tool into the text injected for
// it, or an empty string when its arguments cannot be mapped from the request.
func (c *ToolLoopClient) runConversationTool(ctx context.Context, tool ConversationTool, values map[string]string) (string, error) {
	arguments, missing := FillTemplate(tool.Arguments, values)
	if missing {
		// The arguments name something this request does not carry (no user
		// message to map a query from). Calling the tool anyway would ask the
		// server a question nobody asked.
		return "", nil
	}
	if strings.TrimSpace(arguments) == "" {
		arguments = "{}"
	}

	name := tool.Tool.Name()
	var params map[string]any
	if err := json.Unmarshal([]byte(arguments), &params); err != nil {
		return "", errors.Wrapf(err, "conversation tool %q: arguments are not a JSON object", name)
	}

	content, calledErr := callConversationTool(ctx, tool.Tool, params)
	if calledErr != nil {
		content = "the tool failed: " + calledErr.Error()
	}

	// The result is about to reach the model just like a tool result the model
	// asked for: it goes through the same inspectors, or the indirect
	// injection surface of a prefetched call would be the one nobody watches.
	if blocked, reason := c.inspectToolResult(ctx, name, content); blocked {
		return "", &RejectionError{Reason: reason}
	}

	if content == "" {
		return "", nil
	}
	if tool.ResultTemplate != "" {
		content = strings.ReplaceAll(tool.ResultTemplate, "{{result}}", content)
	}
	return conversationBlock(tool.Label, content), nil
}

// callConversationTool executes a tool call, turning a panic into an error the
// same way the tool loop does for a call the model made.
func callConversationTool(ctx context.Context, tool llm.Tool, params map[string]any) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("tool panicked: %v", r)
		}
	}()

	result, execErr := tool.Execute(ctx, params)
	if execErr != nil {
		return "", execErr
	}
	return result.Text(), nil
}

// conversationBlock wraps one injected value with its label, when it has one.
func conversationBlock(label, content string) string {
	if strings.TrimSpace(label) == "" {
		return content
	}
	return label + "\n" + content
}
