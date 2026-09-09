package pipeline

import (
	"context"

	"github.com/bornholm/genai/llm"
)

// DefaultToolLoopMaxIterations bounds the number of LLM round-trips a
// ToolLoopClient will perform before giving up, in case a model keeps
// requesting tool calls indefinitely. This is a hard safety net, distinct
// from (and larger than) DefaultMaxConsecutiveToolCalls.
const DefaultToolLoopMaxIterations = 8

// DefaultMaxConsecutiveToolCalls bounds how many consecutive tool-resolution
// rounds are allowed before the next LLM call is forced to ToolChoiceNone,
// so the model answers using whatever results it already gathered instead
// of looping further.
const DefaultMaxConsecutiveToolCalls = 2

// ToolLoopClient wraps an llm.Client, intercepting tool calls matching its
// known tools and resolving them (via llm.ExecuteToolCall) before continuing
// the conversation, so the caller never sees these tool_calls. If a turn's
// tool_calls contain even one name unknown to this client, the response is
// returned unmodified — the unresolved tool_calls are left for the upstream
// caller (e.g. normal client-side function calling) to handle.
//
// After maxConsecutiveToolCalls rounds, the next call forces
// ToolChoiceNone instead of erroring out: the model is asked for a final
// answer using whatever tool results it already has. maxIterations remains
// a hard backstop in case a model still returns tool_calls despite
// ToolChoiceNone.
type ToolLoopClient struct {
	inner                   llm.Client
	tools                   []llm.Tool
	maxIterations           int
	maxConsecutiveToolCalls int
	inspectors              *ToolInspectorSet
}

// NewToolLoopClient creates a ToolLoopClient. maxIterations <= 0 falls back
// to DefaultToolLoopMaxIterations; maxConsecutiveToolCalls <= 0 falls back
// to DefaultMaxConsecutiveToolCalls.
func NewToolLoopClient(inner llm.Client, tools []llm.Tool, maxIterations int, maxConsecutiveToolCalls int, inspectors *ToolInspectorSet) *ToolLoopClient {
	if maxIterations <= 0 {
		maxIterations = DefaultToolLoopMaxIterations
	}
	if maxConsecutiveToolCalls <= 0 {
		maxConsecutiveToolCalls = DefaultMaxConsecutiveToolCalls
	}
	return &ToolLoopClient{
		inner:                   inner,
		tools:                   tools,
		maxIterations:           maxIterations,
		maxConsecutiveToolCalls: maxConsecutiveToolCalls,
		inspectors:              inspectors,
	}
}

func (c *ToolLoopClient) toolByName(name string) llm.Tool {
	for _, t := range c.tools {
		if t.Name() == name {
			return t
		}
	}
	return nil
}

// allKnown returns true if every tool call name matches a known tool.
func (c *ToolLoopClient) allKnown(calls []llm.ToolCall) bool {
	for _, tc := range calls {
		if c.toolByName(tc.Name()) == nil {
			return false
		}
	}
	return true
}

func mergeTools(requested []llm.Tool, own []llm.Tool) []llm.Tool {
	if len(own) == 0 {
		return requested
	}
	merged := make([]llm.Tool, 0, len(requested)+len(own))
	merged = append(merged, requested...)
	merged = append(merged, own...)
	return merged
}

// toolChoiceFuncs returns a WithToolChoice override forcing "auto" when
// tools are offered and the caller didn't explicitly request a non-default
// choice. Without this, llm.NewChatCompletionOptions' zero-value default
// (ToolChoiceNone) silently prevents the provider from ever using the tools
// we just injected.
func toolChoiceFuncs(opts *llm.ChatCompletionOptions, tools []llm.Tool) []llm.ChatCompletionOptionFunc {
	if len(tools) == 0 || opts.ToolChoice != llm.ToolChoiceNone {
		return nil
	}
	return []llm.ChatCompletionOptionFunc{llm.WithToolChoice(llm.ToolChoiceAuto)}
}

// ChatCompletion implements llm.Client.
func (c *ToolLoopClient) ChatCompletion(ctx context.Context, funcs ...llm.ChatCompletionOptionFunc) (llm.ChatCompletionResponse, error) {
	opts := llm.NewChatCompletionOptions(funcs...)
	messages := opts.Messages
	tools := mergeTools(opts.Tools, c.tools)

	for i := 0; i < c.maxIterations; i++ {
		forceFinal := i >= c.maxConsecutiveToolCalls

		callFuncs := append(append([]llm.ChatCompletionOptionFunc{}, funcs...), llm.WithMessages(messages...), llm.WithTools(tools...))
		if forceFinal {
			callFuncs = append(callFuncs, llm.WithToolChoice(llm.ToolChoiceNone))
		} else {
			callFuncs = append(callFuncs, toolChoiceFuncs(opts, tools)...)
		}

		resp, err := c.inner.ChatCompletion(ctx, callFuncs...)
		if err != nil {
			return nil, err
		}

		if forceFinal {
			return resp, nil
		}

		toolCalls := resp.ToolCalls()
		if len(toolCalls) == 0 || !c.allKnown(toolCalls) {
			return resp, nil
		}

		messages = append(messages, llm.NewToolCallsMessage(toolCalls...))
		for _, tc := range toolCalls {
			toolMsg := executeToolCallResilient(ctx, tc, c.tools)
			if blocked, reason := c.inspectToolResult(ctx, tc.Name(), toolMsg.Content()); blocked {
				return nil, &RejectionError{Reason: reason}
			}
			messages = append(messages, toolMsg)
		}
	}

	return nil, errTooManyToolIterations
}

// inspectToolResult runs the registered inspectors on a fetched tool result.
func (c *ToolLoopClient) inspectToolResult(ctx context.Context, toolName, content string) (bool, string) {
	if c.inspectors == nil || content == "" {
		return false, ""
	}
	return c.inspectors.Inspect(ctx, toolName, content)
}

// executeToolCallResilient runs the tool call and always returns a tool
// result message, even on failure (e.g. a rate-limited or unreachable MCP
// server): the error is surfaced to the LLM as the tool's result text
// instead of aborting the whole chat completion with a 500. This lets the
// model react (apologize, retry differently, answer without the tool)
// rather than the request failing outright on a transient backend hiccup.
func executeToolCallResilient(ctx context.Context, tc llm.ToolCall, tools []llm.Tool) llm.ToolMessage {
	toolMsg, err := llm.ExecuteToolCall(ctx, tc, tools...)
	if err != nil {
		return llm.NewToolMessage(tc.ID(), llm.NewToolResult("Tool \""+tc.Name()+"\" failed: "+err.Error()))
	}
	return toolMsg
}

// ChatCompletionStream implements llm.Client. Each turn is fully buffered;
// only the final turn (no further tool calls to resolve) is replayed to the
// caller, so intermediate tool-resolution turns stay invisible.
func (c *ToolLoopClient) ChatCompletionStream(ctx context.Context, funcs ...llm.ChatCompletionOptionFunc) (<-chan llm.StreamChunk, error) {
	opts := llm.NewChatCompletionOptions(funcs...)
	messages := opts.Messages
	tools := mergeTools(opts.Tools, c.tools)

	outCh := make(chan llm.StreamChunk, 8)

	go func() {
		defer close(outCh)

		for i := 0; i < c.maxIterations; i++ {
			forceFinal := i >= c.maxConsecutiveToolCalls

			callFuncs := append(append([]llm.ChatCompletionOptionFunc{}, funcs...), llm.WithMessages(messages...), llm.WithTools(tools...))
			if forceFinal {
				callFuncs = append(callFuncs, llm.WithToolChoice(llm.ToolChoiceNone))
			} else {
				callFuncs = append(callFuncs, toolChoiceFuncs(opts, tools)...)
			}

			sourceCh, err := c.inner.ChatCompletionStream(ctx, callFuncs...)
			if err != nil {
				outCh <- llm.NewErrorStreamChunk(err)
				return
			}

			chunks, toolCalls := bufferStreamTurn(sourceCh)

			if forceFinal || len(toolCalls) == 0 || !c.allKnown(toolCalls) {
				for _, ch := range chunks {
					outCh <- ch
				}
				return
			}

			messages = append(messages, llm.NewToolCallsMessage(toolCalls...))
			blockedInLoop := false
			for _, tc := range toolCalls {
				toolMsg := executeToolCallResilient(ctx, tc, c.tools)
				if blocked, reason := c.inspectToolResult(ctx, tc.Name(), toolMsg.Content()); blocked {
					outCh <- llm.NewErrorStreamChunk(&RejectionError{Reason: reason})
					blockedInLoop = true
					break
				}
				messages = append(messages, toolMsg)
			}
			if blockedInLoop {
				return
			}
		}

		outCh <- llm.NewErrorStreamChunk(errTooManyToolIterations)
	}()

	return outCh, nil
}

// Embeddings implements llm.Client.
func (c *ToolLoopClient) Embeddings(ctx context.Context, inputs []string, funcs ...llm.EmbeddingsOptionFunc) (llm.EmbeddingsResponse, error) {
	return c.inner.Embeddings(ctx, inputs, funcs...)
}

// Transcription implements llm.Client. The tool loop only applies to chat
// completions, so transcriptions are passed through unchanged.
func (c *ToolLoopClient) Transcription(ctx context.Context, audio []byte, funcs ...llm.TranscriptionOptionFunc) (llm.TranscriptionResponse, error) {
	return c.inner.Transcription(ctx, audio, funcs...)
}

var _ llm.Client = (*ToolLoopClient)(nil)

// bufferStreamTurn drains sourceCh, accumulating tool call deltas (by index)
// into complete ToolCall values, and returns every chunk seen alongside the
// assembled tool calls (empty if the turn carried none).
func bufferStreamTurn(sourceCh <-chan llm.StreamChunk) ([]llm.StreamChunk, []llm.ToolCall) {
	var chunks []llm.StreamChunk
	assembled := map[int]*assembledToolCall{}
	var order []int

	for chunk := range sourceCh {
		chunks = append(chunks, chunk)
		d := chunk.Delta()
		if d == nil {
			continue
		}
		for _, tcd := range d.ToolCalls() {
			a, ok := assembled[tcd.Index()]
			if !ok {
				a = &assembledToolCall{}
				assembled[tcd.Index()] = a
				order = append(order, tcd.Index())
			}
			if tcd.ID() != "" {
				a.id = tcd.ID()
			}
			if tcd.Name() != "" {
				a.name = tcd.Name()
			}
			a.argumentsJSON += tcd.ParametersDelta()
		}
	}

	if len(order) == 0 {
		return chunks, nil
	}

	toolCalls := make([]llm.ToolCall, 0, len(order))
	for _, idx := range order {
		a := assembled[idx]
		toolCalls = append(toolCalls, llm.NewToolCall(a.id, a.name, a.argumentsJSON))
	}
	return chunks, toolCalls
}

type assembledToolCall struct {
	id            string
	name          string
	argumentsJSON string
}

var errTooManyToolIterations = toolLoopError("tool loop: max iterations exceeded")

type toolLoopError string

func (e toolLoopError) Error() string { return string(e) }
