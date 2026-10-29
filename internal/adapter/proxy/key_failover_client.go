package proxy

import (
	"context"
	"net/http"

	"github.com/bornholm/genai/llm"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// Request metadata naming the API key of the provider a request goes through,
// when the provider spreads its subscription plan over several keys.
const (
	// MetaProviderKey is the key the subscription enforcer chose
	// (model.ProviderKeyID).
	MetaProviderKey = "xolo.provider.key"
	// metaProviderKeyFallbacks are the other keys the request may move to when
	// the chosen one turns out exhausted upstream ([]model.ProviderKeyID).
	metaProviderKeyFallbacks = "xolo.provider.key.fallbacks"
	// MetaProviderKeyUsed is the key the request last went through, the one its
	// usage is recorded on (model.ProviderKeyID).
	MetaProviderKeyUsed = "xolo.provider.key.used"
)

// KeyExhaustionHandler is told that an API key of a provider answered an
// upstream 429 the request then moved away from, so that the key is put in
// cooldown like one the request had ended on.
type KeyExhaustionHandler func(ctx context.Context, metadata map[string]any, key model.ProviderKeyID, header http.Header)

// keyedClient is the client of one API key.
type keyedClient struct {
	key    model.ProviderKeyID
	client llm.Client
}

// keyFailoverClient sends a request through the first key of a list and moves
// it to the next one when a key answers that its plan is exhausted, so that a
// user whose key runs out keeps being served, without an error, by another key
// with room left. Only an exhaustion moves the request: any other error, a
// capacity 429 of the provider included, is the provider's whatever the key.
//
// A stream moves only when the provider refuses it before the first chunk; a
// refusal carried by the stream itself ends the request, the error hooks
// putting the key in cooldown for the next one.
type keyFailoverClient struct {
	clients   []keyedClient
	metadata  map[string]any
	exhausted KeyExhaustionHandler
}

func newKeyFailoverClient(clients []keyedClient, metadata map[string]any, exhausted KeyExhaustionHandler) llm.Client {
	if len(clients) == 1 {
		return clients[0].client
	}
	return &keyFailoverClient{clients: clients, metadata: metadata, exhausted: exhausted}
}

// try runs call on each key in turn until one does not answer an exhaustion.
func try[T any](ctx context.Context, c *keyFailoverClient, call func(llm.Client) (T, error)) (T, error) {
	var (
		res T
		err error
	)
	for i, kc := range c.clients {
		c.metadata[MetaProviderKeyUsed] = kc.key
		res, err = call(kc.client)
		header, exhausted := planExhaustion(err)
		if !exhausted || i == len(c.clients)-1 {
			return res, err
		}
		if c.exhausted != nil {
			c.exhausted(ctx, c.metadata, kc.key, header)
		}
	}
	return res, err
}

// planExhaustion reports whether err is an upstream 429 putting the refusal
// on the plan, with the headers that say when to come back.
func planExhaustion(err error) (http.Header, bool) {
	var httpErr *llm.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusTooManyRequests || isUpstreamCapacityError(httpErr.Body) {
		return nil, false
	}
	return httpErr.Header, true
}

func (c *keyFailoverClient) ChatCompletion(ctx context.Context, funcs ...llm.ChatCompletionOptionFunc) (llm.ChatCompletionResponse, error) {
	return try(ctx, c, func(client llm.Client) (llm.ChatCompletionResponse, error) {
		return client.ChatCompletion(ctx, funcs...)
	})
}

func (c *keyFailoverClient) ChatCompletionStream(ctx context.Context, funcs ...llm.ChatCompletionOptionFunc) (<-chan llm.StreamChunk, error) {
	return try(ctx, c, func(client llm.Client) (<-chan llm.StreamChunk, error) {
		return client.ChatCompletionStream(ctx, funcs...)
	})
}

func (c *keyFailoverClient) Embeddings(ctx context.Context, inputs []string, funcs ...llm.EmbeddingsOptionFunc) (llm.EmbeddingsResponse, error) {
	return try(ctx, c, func(client llm.Client) (llm.EmbeddingsResponse, error) {
		return client.Embeddings(ctx, inputs, funcs...)
	})
}

func (c *keyFailoverClient) Transcription(ctx context.Context, audio []byte, funcs ...llm.TranscriptionOptionFunc) (llm.TranscriptionResponse, error) {
	return try(ctx, c, func(client llm.Client) (llm.TranscriptionResponse, error) {
		return client.Transcription(ctx, audio, funcs...)
	})
}

var _ llm.Client = &keyFailoverClient{}
