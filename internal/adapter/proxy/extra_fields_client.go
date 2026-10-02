package proxy

import (
	"context"
	"net/http"

	"github.com/bornholm/genai/llm"
	"github.com/pkg/errors"
)

// extraFieldsClient wraps an llm.Client to inject a fixed set of provider-specific
// extra body fields into every chat-completion request. It is used to apply a
// model's configured ExtraBody (e.g. MiniMax's "reasoning_split") to all calls
// routed to that model.
type extraFieldsClient struct {
	inner  llm.Client
	fields map[string]any
}

// newExtraFieldsClient returns a client that appends llm.WithExtraFields(fields)
// to every chat-completion call. When fields is empty the inner client is
// returned unchanged so no wrapper overhead is added.
func newExtraFieldsClient(inner llm.Client, fields map[string]any) llm.Client {
	if len(fields) == 0 {
		return inner
	}
	return &extraFieldsClient{inner: inner, fields: fields}
}

func (c *extraFieldsClient) withExtra(funcs []llm.ChatCompletionOptionFunc) []llm.ChatCompletionOptionFunc {
	out := make([]llm.ChatCompletionOptionFunc, 0, len(funcs)+1)
	out = append(out, funcs...)
	out = append(out, llm.WithExtraFields(c.fields))
	return out
}

func (c *extraFieldsClient) ChatCompletion(ctx context.Context, funcs ...llm.ChatCompletionOptionFunc) (llm.ChatCompletionResponse, error) {
	return c.inner.ChatCompletion(ctx, c.withExtra(funcs)...)
}

func (c *extraFieldsClient) ChatCompletionStream(ctx context.Context, funcs ...llm.ChatCompletionOptionFunc) (<-chan llm.StreamChunk, error) {
	return c.inner.ChatCompletionStream(ctx, c.withExtra(funcs)...)
}

func (c *extraFieldsClient) Embeddings(ctx context.Context, inputs []string, funcs ...llm.EmbeddingsOptionFunc) (llm.EmbeddingsResponse, error) {
	return c.inner.Embeddings(ctx, inputs, funcs...)
}

// RelayMessages implements llm.MessagesRelayClient: the fields are merged into
// the relayed body, where they override the client's, as they override the
// options of a translated request.
func (c *extraFieldsClient) RelayMessages(ctx context.Context, body []byte, header http.Header) (<-chan llm.StreamChunk, error) {
	merged, err := setBodyFields(body, c.fields)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return llm.RelayMessages(ctx, c.inner, merged, header)
}

// SupportsMessagesRelay reports on the wrapped client, see
// llm.SupportsMessagesRelay.
func (c *extraFieldsClient) SupportsMessagesRelay() bool {
	return llm.SupportsMessagesRelay(c.inner)
}

// Transcription is passed through unchanged: the configured extra fields target
// chat completions only.
func (c *extraFieldsClient) Transcription(ctx context.Context, audio []byte, funcs ...llm.TranscriptionOptionFunc) (llm.TranscriptionResponse, error) {
	return c.inner.Transcription(ctx, audio, funcs...)
}

var (
	_ llm.Client              = (*extraFieldsClient)(nil)
	_ llm.MessagesRelayClient = (*extraFieldsClient)(nil)
)
