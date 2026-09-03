package proxy

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/bornholm/genai/llm"
	"github.com/pkg/errors"
)

// errUpstreamTimeout is the cancellation cause set when the watchdog fires,
// which tells a context error apart from a client that went away.
var errUpstreamTimeout = errors.New("upstream timeout")

// timeoutClient bounds the time spent waiting on the upstream provider.
//
// A plain completion, an embeddings or a transcription call gets a deadline on
// its whole duration. A streamed completion gets a watchdog instead: it fires
// when the first chunk, or the next one, takes longer than the timeout, so a
// long answer that keeps flowing is never cut. Either way the caller receives
// an llm.HTTPError with status 504 that the proxy turns into an explicit
// gateway timeout, rather than letting the reverse proxy in front of Xolo cut
// the connection with an opaque error.
//
// It is meant to be the outermost wrapper of the client chain: the timeout
// then bounds the whole call, retries included, and a timeout is never
// retried.
type timeoutClient struct {
	inner   llm.Client
	timeout time.Duration
}

// newTimeoutClient wraps inner; a zero or negative timeout returns inner
// unchanged.
func newTimeoutClient(inner llm.Client, timeout time.Duration) llm.Client {
	if timeout <= 0 {
		return inner
	}
	return &timeoutClient{inner: inner, timeout: timeout}
}

func (c *timeoutClient) timeoutError() error {
	return llm.NewHTTPError(http.StatusGatewayTimeout, fmt.Sprintf("upstream provider did not answer within %s", c.timeout))
}

// translate replaces the context error raised by the watchdog with the 504.
// Any other error, a client cancellation included, is returned as is.
func (c *timeoutClient) translate(callCtx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(context.Cause(callCtx), errUpstreamTimeout) && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
		return c.timeoutError()
	}
	return err
}

func (c *timeoutClient) ChatCompletion(ctx context.Context, funcs ...llm.ChatCompletionOptionFunc) (llm.ChatCompletionResponse, error) {
	callCtx, cancel := context.WithTimeoutCause(ctx, c.timeout, errUpstreamTimeout)
	defer cancel()

	res, err := c.inner.ChatCompletion(callCtx, funcs...)
	if err != nil {
		return nil, c.translate(callCtx, err)
	}
	return res, nil
}

func (c *timeoutClient) ChatCompletionStream(ctx context.Context, funcs ...llm.ChatCompletionOptionFunc) (<-chan llm.StreamChunk, error) {
	callCtx, cancel := context.WithCancelCause(ctx)
	watchdog := time.AfterFunc(c.timeout, func() { cancel(errUpstreamTimeout) })

	stream, err := c.inner.ChatCompletionStream(callCtx, funcs...)
	if err != nil {
		watchdog.Stop()
		cancel(nil)
		return nil, c.translate(callCtx, err)
	}

	out := make(chan llm.StreamChunk)

	go func() {
		defer close(out)
		defer cancel(nil)
		defer watchdog.Stop()

		reported := false

		for chunk := range stream {
			watchdog.Reset(c.timeout)

			if chunkErr := chunk.Error(); chunkErr != nil {
				if translated := c.translate(callCtx, chunkErr); translated != chunkErr {
					chunk = llm.NewErrorStreamChunk(translated)
				}
				reported = true
			}

			select {
			case out <- chunk:
			case <-ctx.Done():
				return
			}
		}

		// The inner stream may close silently once its context is canceled:
		// the client must still learn why the answer stopped.
		if !reported && errors.Is(context.Cause(callCtx), errUpstreamTimeout) {
			select {
			case out <- llm.NewErrorStreamChunk(c.timeoutError()):
			case <-ctx.Done():
			}
		}
	}()

	return out, nil
}

func (c *timeoutClient) Embeddings(ctx context.Context, inputs []string, funcs ...llm.EmbeddingsOptionFunc) (llm.EmbeddingsResponse, error) {
	callCtx, cancel := context.WithTimeoutCause(ctx, c.timeout, errUpstreamTimeout)
	defer cancel()

	res, err := c.inner.Embeddings(callCtx, inputs, funcs...)
	if err != nil {
		return nil, c.translate(callCtx, err)
	}
	return res, nil
}

func (c *timeoutClient) Transcription(ctx context.Context, audio []byte, funcs ...llm.TranscriptionOptionFunc) (llm.TranscriptionResponse, error) {
	callCtx, cancel := context.WithTimeoutCause(ctx, c.timeout, errUpstreamTimeout)
	defer cancel()

	res, err := c.inner.Transcription(callCtx, audio, funcs...)
	if err != nil {
		return nil, c.translate(callCtx, err)
	}
	return res, nil
}

var _ llm.Client = (*timeoutClient)(nil)
