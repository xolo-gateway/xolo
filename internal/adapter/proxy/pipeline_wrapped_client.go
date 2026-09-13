package proxy

import (
	"bytes"
	"context"
	"log/slog"

	"github.com/bornholm/genai/llm"
	"github.com/xolo-gateway/xolo/internal/pipeline"
)

// PipelineWrappedClient wraps an llm.Client to run the pipeline's backward pass
// (post-response processing) after each LLM call.
type PipelineWrappedClient struct {
	inner       llm.Client
	engine      *pipeline.Engine
	forwardExec *pipeline.ForwardExecution
	ec          pipeline.ExecutionContext
}

// NewPipelineWrappedClient creates a PipelineWrappedClient.
func NewPipelineWrappedClient(
	inner llm.Client,
	engine *pipeline.Engine,
	forwardExec *pipeline.ForwardExecution,
	ec pipeline.ExecutionContext,
) *PipelineWrappedClient {
	return &PipelineWrappedClient{inner: inner, engine: engine, forwardExec: forwardExec, ec: ec}
}

// ChatCompletion calls the inner client and runs the backward pass on the result.
func (c *PipelineWrappedClient) ChatCompletion(ctx context.Context, funcs ...llm.ChatCompletionOptionFunc) (llm.ChatCompletionResponse, error) {
	resp, err := c.inner.ChatCompletion(ctx, funcs...)
	if err != nil {
		c.runBackward(ctx, "", nil, true)
		return resp, err
	}

	content := ""
	if msg := resp.Message(); msg != nil {
		content = msg.Content()
	}
	tokens := extractResponseTokens(resp)

	outcome, backErr := c.engine.RunBackwardWithToolCalls(ctx, c.forwardExec, content, encodeToolCalls(resp.ToolCalls()), tokens, false)
	if backErr != nil {
		slog.WarnContext(ctx, "pipeline backward pass failed", slog.Any("error", backErr))
		return resp, nil
	}

	rewrittenCalls := decodeToolCalls(ctx, outcome.ToolCallsJSON, resp.ToolCalls())
	if outcome.ResponseContent != content || rewrittenCalls != nil {
		return &wrappedChatCompletionResponse{
			inner:             resp,
			modifiedContent:   outcome.ResponseContent,
			modifiedToolCalls: rewrittenCalls,
		}, nil
	}
	return resp, nil
}

// ChatCompletionStream runs the pipeline's backward pass over the streamed
// response.
//
// When no executed node can rewrite the response content, chunks are forwarded
// to the caller as they arrive and the backward pass runs once the stream ends:
// the client keeps a real token-by-token stream. Only when a node may rewrite
// the text — a plugin with the POST_RESPONSE capability, e.g. the
// pseudonymizer's de-anonymization step — is the stream buffered, since the
// rewrite needs the whole text before anything can be emitted.
func (c *PipelineWrappedClient) ChatCompletionStream(ctx context.Context, funcs ...llm.ChatCompletionOptionFunc) (<-chan llm.StreamChunk, error) {
	sourceCh, err := c.inner.ChatCompletionStream(ctx, funcs...)
	if err != nil {
		return nil, err
	}

	if !c.engine.MayModifyResponse(ctx, c.forwardExec) {
		return c.streamPassthrough(ctx, sourceCh), nil
	}

	outCh := make(chan llm.StreamChunk, 8)
	go func() {
		defer close(outCh)

		var chunks []llm.StreamChunk
		var buf bytes.Buffer
		var lastTokens *pipeline.TokensUsed
		calls := &streamedToolCalls{}

		for chunk := range sourceCh {
			chunks = append(chunks, chunk)
			if d := chunk.Delta(); d != nil {
				buf.WriteString(d.Content())
				calls.collect(d.ToolCalls())
			}
			if u := chunk.Usage(); u != nil {
				lastTokens = &pipeline.TokensUsed{
					Prompt:     u.PromptTokens(),
					Completion: u.CompletionTokens(),
				}
			}
		}

		content := buf.String()
		outcome, backErr := c.engine.RunBackwardWithToolCalls(ctx, c.forwardExec, content, calls.json(), lastTokens, false)
		modified := content
		rewrittenCalls := []llm.ToolCallDelta(nil)
		if backErr != nil {
			slog.WarnContext(ctx, "pipeline backward pass (stream) failed", slog.Any("error", backErr))
		} else {
			modified = outcome.ResponseContent
			rewrittenCalls = calls.rewritten(ctx, outcome.ToolCallsJSON)
		}

		if modified == content && rewrittenCalls == nil {
			for _, ch := range chunks {
				outCh <- ch
			}
			return
		}

		// Re-emit chunks with the modified content: the full modified text is
		// placed on the first delta chunk carrying content, and subsequent
		// content deltas are emptied. Rewritten tool calls follow the same rule
		// — the whole argument payload rides on the first chunk that carried
		// tool call deltas, which the Anthropic and OpenAI stream writers both
		// accept, and the later ones carry none. Other chunk types (usage,
		// reasoning, complete…) are passed through unchanged.
		replaced := false
		toolCallsEmitted := false
		for _, ch := range chunks {
			d := ch.Delta()
			if d == nil {
				outCh <- ch
				continue
			}
			hasToolCalls := rewrittenCalls != nil && len(d.ToolCalls()) > 0
			if d.Content() == "" && !hasToolCalls {
				outCh <- ch
				continue
			}

			override := &contentOverrideDelta{StreamDelta: d, content: d.Content()}
			if d.Content() != "" {
				if !replaced {
					override.content = modified
					replaced = true
				} else {
					override.content = ""
				}
			}
			if hasToolCalls {
				override.overrideToolCalls = true
				if !toolCallsEmitted {
					override.toolCalls = rewrittenCalls
					toolCallsEmitted = true
				}
			}
			outCh <- &contentOverrideChunk{StreamChunk: ch, delta: override}
		}
	}()

	return outCh, nil
}

// streamPassthrough forwards every chunk as it arrives while accumulating the
// content and token usage needed by the backward pass, which runs once the
// source stream is exhausted. Nothing is held back, so the caller sees the
// provider's own streaming cadence.
func (c *PipelineWrappedClient) streamPassthrough(ctx context.Context, sourceCh <-chan llm.StreamChunk) <-chan llm.StreamChunk {
	outCh := make(chan llm.StreamChunk, 8)

	go func() {
		defer close(outCh)

		var buf bytes.Buffer
		var lastTokens *pipeline.TokensUsed

		for chunk := range sourceCh {
			if d := chunk.Delta(); d != nil {
				buf.WriteString(d.Content())
			}
			if u := chunk.Usage(); u != nil {
				lastTokens = &pipeline.TokensUsed{
					Prompt:     u.PromptTokens(),
					Completion: u.CompletionTokens(),
				}
			}

			select {
			case outCh <- chunk:
			case <-ctx.Done():
				// The caller is gone. Still run the backward pass below so
				// nodes that record state (usage, quotas…) see the partial
				// response instead of nothing.
				c.runBackward(context.WithoutCancel(ctx), buf.String(), lastTokens, true)
				return
			}
		}

		// The response was already delivered: a rewrite here would be ignored,
		// which is exactly why this path was chosen.
		c.runBackward(ctx, buf.String(), lastTokens, false)
	}()

	return outCh
}

// Embeddings is passed through unchanged.
func (c *PipelineWrappedClient) Embeddings(ctx context.Context, inputs []string, funcs ...llm.EmbeddingsOptionFunc) (llm.EmbeddingsResponse, error) {
	return c.inner.Embeddings(ctx, inputs, funcs...)
}

// Transcription is passed through unchanged: the pipeline's backward pass
// operates on chat completion content only.
func (c *PipelineWrappedClient) Transcription(ctx context.Context, audio []byte, funcs ...llm.TranscriptionOptionFunc) (llm.TranscriptionResponse, error) {
	return c.inner.Transcription(ctx, audio, funcs...)
}

func (c *PipelineWrappedClient) runBackward(ctx context.Context, content string, tokens *pipeline.TokensUsed, hadError bool) {
	if _, err := c.engine.RunBackward(ctx, c.forwardExec, content, tokens, hadError); err != nil {
		slog.WarnContext(ctx, "pipeline backward pass failed", slog.Any("error", err))
	}
}

func extractResponseTokens(resp llm.ChatCompletionResponse) *pipeline.TokensUsed {
	if resp == nil {
		return nil
	}
	u := resp.Usage()
	if u == nil {
		return nil
	}
	return &pipeline.TokensUsed{Prompt: u.PromptTokens(), Completion: u.CompletionTokens()}
}

// wrappedChatCompletionResponse replaces the message content while keeping
// everything else from the original response.
type wrappedChatCompletionResponse struct {
	inner             llm.ChatCompletionResponse
	modifiedContent   string
	modifiedToolCalls []llm.ToolCall
}

func (r *wrappedChatCompletionResponse) Message() llm.Message {
	return &modifiedMessage{
		original:  r.inner.Message(),
		content:   r.modifiedContent,
		toolCalls: r.ToolCalls(),
	}
}

func (r *wrappedChatCompletionResponse) ToolCalls() []llm.ToolCall {
	if r.modifiedToolCalls != nil {
		return r.modifiedToolCalls
	}
	return r.inner.ToolCalls()
}
func (r *wrappedChatCompletionResponse) Usage() llm.ChatCompletionUsage { return r.inner.Usage() }

// modifiedMessage replaces Content() while delegating everything else.
type modifiedMessage struct {
	original  llm.Message
	content   string
	toolCalls []llm.ToolCall
}

func (m *modifiedMessage) Role() llm.Role  { return m.original.Role() }
func (m *modifiedMessage) Content() string { return m.content }

// ToolCalls keeps the message a llm.ToolCallsMessage when the original was one,
// carrying the rewritten calls rather than the ones the provider sent.
func (m *modifiedMessage) ToolCalls() []llm.ToolCall { return m.toolCalls }
func (m *modifiedMessage) Attachments() []llm.Attachment {
	if a, ok := m.original.(interface{ Attachments() []llm.Attachment }); ok {
		return a.Attachments()
	}
	return nil
}

// contentOverrideChunk replaces the Delta() of a StreamChunk while delegating
// everything else (type, usage, error, completion flag).
type contentOverrideChunk struct {
	llm.StreamChunk
	delta llm.StreamDelta
}

func (c *contentOverrideChunk) Delta() llm.StreamDelta { return c.delta }

// contentOverrideDelta replaces the Content() of a StreamDelta while
// delegating role, tool calls, reasoning and audio fields to the original.
type contentOverrideDelta struct {
	llm.StreamDelta
	content string
	// overrideToolCalls tells ToolCalls to answer with toolCalls — possibly
	// none — instead of delegating. A nil slice and "do not override" are two
	// different answers here.
	overrideToolCalls bool
	toolCalls         []llm.ToolCallDelta
}

func (d *contentOverrideDelta) Content() string { return d.content }

func (d *contentOverrideDelta) ToolCalls() []llm.ToolCallDelta {
	if d.overrideToolCalls {
		return d.toolCalls
	}
	return d.StreamDelta.ToolCalls()
}

func (d *contentOverrideDelta) Reasoning() string {
	if r, ok := d.StreamDelta.(llm.ReasoningStreamDelta); ok {
		return r.Reasoning()
	}
	return ""
}

func (d *contentOverrideDelta) ReasoningDetails() []llm.ReasoningDetail {
	if r, ok := d.StreamDelta.(llm.ReasoningStreamDelta); ok {
		return r.ReasoningDetails()
	}
	return nil
}

var _ llm.Client = (*PipelineWrappedClient)(nil)
var _ llm.ReasoningStreamDelta = (*contentOverrideDelta)(nil)
