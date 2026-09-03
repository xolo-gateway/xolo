package proxy

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/bornholm/genai/llm"
)

// stallingClient never answers until its context is done, and streams chunks
// on demand through the chunks channel.
type stallingClient struct {
	chunks chan llm.StreamChunk
}

func (s *stallingClient) ChatCompletion(ctx context.Context, _ ...llm.ChatCompletionOptionFunc) (llm.ChatCompletionResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (s *stallingClient) ChatCompletionStream(ctx context.Context, _ ...llm.ChatCompletionOptionFunc) (<-chan llm.StreamChunk, error) {
	out := make(chan llm.StreamChunk)
	go func() {
		defer close(out)
		for {
			select {
			case chunk := <-s.chunks:
				out <- chunk
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (s *stallingClient) Embeddings(ctx context.Context, _ []string, _ ...llm.EmbeddingsOptionFunc) (llm.EmbeddingsResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (s *stallingClient) Transcription(ctx context.Context, _ []byte, _ ...llm.TranscriptionOptionFunc) (llm.TranscriptionResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func assertGatewayTimeout(t *testing.T, err error) {
	t.Helper()
	var httpErr *llm.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected llm.HTTPError, got %T: %v", err, err)
	}
	if httpErr.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("expected status 504, got %d", httpErr.StatusCode)
	}
}

func TestTimeoutClientChatCompletion(t *testing.T) {
	client := newTimeoutClient(&stallingClient{}, 20*time.Millisecond)

	_, err := client.ChatCompletion(context.Background())
	assertGatewayTimeout(t, err)
}

func TestTimeoutClientEmbeddings(t *testing.T) {
	client := newTimeoutClient(&stallingClient{}, 20*time.Millisecond)

	_, err := client.Embeddings(context.Background(), []string{"a"})
	assertGatewayTimeout(t, err)
}

func TestTimeoutClientKeepsClientCancellation(t *testing.T) {
	client := newTimeoutClient(&stallingClient{}, time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	_, err := client.ChatCompletion(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	var httpErr *llm.HTTPError
	if errors.As(err, &httpErr) {
		t.Fatalf("a client cancellation must not be reported as a gateway timeout")
	}
}

func TestTimeoutClientStreamFirstChunk(t *testing.T) {
	client := newTimeoutClient(&stallingClient{chunks: make(chan llm.StreamChunk)}, 20*time.Millisecond)

	stream, err := client.ChatCompletionStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	chunk, ok := <-stream
	if !ok {
		t.Fatal("expected an error chunk before the stream closes")
	}
	assertGatewayTimeout(t, chunk.Error())

	if _, ok := <-stream; ok {
		t.Fatal("expected the stream to close after the error chunk")
	}
}

func TestTimeoutClientStreamResetsOnChunks(t *testing.T) {
	inner := &stallingClient{chunks: make(chan llm.StreamChunk)}
	client := newTimeoutClient(inner, 50*time.Millisecond)

	stream, err := client.ChatCompletionStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Four chunks spaced under the timeout but summing well above it.
	for i := 0; i < 4; i++ {
		time.Sleep(30 * time.Millisecond)
		inner.chunks <- llm.NewStreamChunk(nil)
		chunk := <-stream
		if chunk.Error() != nil {
			t.Fatalf("unexpected error on chunk %d: %v", i, chunk.Error())
		}
	}

	chunk := <-stream
	assertGatewayTimeout(t, chunk.Error())
}
