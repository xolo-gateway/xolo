package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/bornholm/genai/llm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/pipeline"
)

// stallingClient never answers until its context is done, and streams chunks
// on demand through the chunks channel.
type stallingClient struct {
	chunks chan llm.StreamChunk
}

type fallbackTimeoutResolver struct {
	primary llm.Client
	backup  llm.Client
}

func (r fallbackTimeoutResolver) ResolveRealModel(_ context.Context, _ model.OrgID, name string) (llm.Client, string, model.LLMModelID, error) {
	if name == "primary" {
		return r.primary, name, model.LLMModelID(name), nil
	}
	return r.backup, name, model.LLMModelID(name), nil
}

type fallbackAnswerClient struct{ stallingClient }

func (fallbackAnswerClient) ChatCompletionStream(context.Context, ...llm.ChatCompletionOptionFunc) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, 1)
	ch <- llm.NewStreamChunk(llm.NewStreamDelta(llm.RoleAssistant, "backup"))
	close(ch)
	return ch, nil
}

func TestFallbackWaitsForUpstreamTimeout(t *testing.T) {
	primary := newTimeoutClient(&stallingClient{chunks: make(chan llm.StreamChunk)}, 30*time.Millisecond)
	resolver := fallbackTimeoutResolver{primary: primary, backup: &fallbackAnswerClient{}}
	data, err := json.Marshal(model.ModelFallbackNodeData{Models: []string{"primary", "backup"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := pipeline.NewModelFallbackExecutor(resolver).Forward(context.Background(), model.PipelineNode{ID: "fallback", Type: model.NodeTypeModelFallback, Data: data}, nil, pipeline.ExecutionContext{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := result.ResolvedClient.ChatCompletionStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	chunk, ok := <-stream
	if !ok || chunk.Error() != nil || chunk.Delta().Content() != "backup" {
		t.Fatalf("unexpected fallback chunk: %v", chunk)
	}
	for range stream {
	}
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
