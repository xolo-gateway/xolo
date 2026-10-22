package proxy

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bornholm/genai/llm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/pipeline"
)

// rewriteExecutor is a test NodeExecutor whose Backward pass replaces a
// placeholder in the response content, simulating the pseudonymizer plugin.
type rewriteExecutor struct {
	from, to string
}

func (e *rewriteExecutor) Forward(_ context.Context, _ model.PipelineNode, _ map[string]interface{}, _ pipeline.ExecutionContext) (*pipeline.ForwardResult, error) {
	return &pipeline.ForwardResult{}, nil
}

func (e *rewriteExecutor) Backward(_ context.Context, in pipeline.BackwardInput) (*pipeline.BackwardResult, error) {
	return &pipeline.BackwardResult{
		ModifiedResponseContent: strings.ReplaceAll(in.ResponseContent, e.from, e.to),
		ModifiedToolCallsJSON:   strings.ReplaceAll(in.ToolCallsJSON, e.from, e.to),
	}, nil
}

func newTestForwardExecution() *pipeline.ForwardExecution {
	return &pipeline.ForwardExecution{
		ExecutedNodes: []pipeline.ExecutedNode{
			{Node: model.PipelineNode{ID: "rewrite", Type: "rewrite"}},
		},
	}
}

func newTestEngine(from, to string) *pipeline.Engine {
	registry := pipeline.NewRegistry()
	registry.Register("rewrite", &rewriteExecutor{from: from, to: to})
	return pipeline.NewEngine(registry)
}

// multiChunkClient streams the given content split across several delta chunks.
type multiChunkClient struct {
	parts []string
}

func (c *multiChunkClient) ChatCompletion(_ context.Context, _ ...llm.ChatCompletionOptionFunc) (llm.ChatCompletionResponse, error) {
	return nil, nil
}

func (c *multiChunkClient) ChatCompletionStream(_ context.Context, _ ...llm.ChatCompletionOptionFunc) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, len(c.parts)+1)
	go func() {
		defer close(ch)
		for _, p := range c.parts {
			ch <- llm.NewStreamChunk(llm.NewStreamDelta(llm.RoleAssistant, p))
		}
		ch <- llm.NewCompleteStreamChunk(llm.NewChatCompletionUsage(0, 0, 0))
	}()
	return ch, nil
}

func (c *multiChunkClient) Embeddings(_ context.Context, _ []string, _ ...llm.EmbeddingsOptionFunc) (llm.EmbeddingsResponse, error) {
	return nil, nil
}

func (c *multiChunkClient) Transcription(_ context.Context, _ []byte, _ ...llm.TranscriptionOptionFunc) (llm.TranscriptionResponse, error) {
	return nil, nil
}

var _ llm.Client = (*multiChunkClient)(nil)

func collectContent(t *testing.T, ch <-chan llm.StreamChunk) string {
	t.Helper()
	var b strings.Builder
	for chunk := range ch {
		if d := chunk.Delta(); d != nil {
			b.WriteString(d.Content())
		}
	}
	return b.String()
}

// observerExecutor never rewrites the response but records what the backward
// pass received, standing in for a plugin that only collects metrics.
type observerExecutor struct {
	mu      sync.Mutex
	content string
	called  bool
}

func (e *observerExecutor) Forward(_ context.Context, _ model.PipelineNode, _ map[string]interface{}, _ pipeline.ExecutionContext) (*pipeline.ForwardResult, error) {
	return &pipeline.ForwardResult{}, nil
}

func (e *observerExecutor) ModifiesResponse(context.Context, model.PipelineNode) bool { return false }

func (e *observerExecutor) Backward(_ context.Context, in pipeline.BackwardInput) (*pipeline.BackwardResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.called = true
	e.content = in.ResponseContent
	return &pipeline.BackwardResult{}, nil
}

func (e *observerExecutor) seen() (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.content, e.called
}

// gatedClient emits a first chunk, then waits for the test to unblock it before
// emitting the rest. A client that buffers the whole stream can never deliver
// the first chunk, so reading it proves chunks flow through live.
type gatedClient struct {
	gate chan struct{}
}

func (c *gatedClient) ChatCompletion(_ context.Context, _ ...llm.ChatCompletionOptionFunc) (llm.ChatCompletionResponse, error) {
	return nil, nil
}

func (c *gatedClient) ChatCompletionStream(_ context.Context, _ ...llm.ChatCompletionOptionFunc) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk)
	go func() {
		defer close(ch)
		ch <- llm.NewStreamChunk(llm.NewStreamDelta(llm.RoleAssistant, "premier "))
		<-c.gate
		ch <- llm.NewStreamChunk(llm.NewStreamDelta(llm.RoleAssistant, "morceau."))
		ch <- llm.NewCompleteStreamChunk(llm.NewChatCompletionUsage(3, 7, 10))
	}()
	return ch, nil
}

func (c *gatedClient) Embeddings(_ context.Context, _ []string, _ ...llm.EmbeddingsOptionFunc) (llm.EmbeddingsResponse, error) {
	return nil, nil
}

func (c *gatedClient) Transcription(_ context.Context, _ []byte, _ ...llm.TranscriptionOptionFunc) (llm.TranscriptionResponse, error) {
	return nil, nil
}

var _ llm.Client = (*gatedClient)(nil)

// When no node can rewrite the response, chunks must reach the caller as the
// provider produces them instead of being held until the stream ends.
func TestPipelineWrappedClient_ChatCompletionStream_NoBufferingWhenNoModifier(t *testing.T) {
	obs := &observerExecutor{}
	registry := pipeline.NewRegistry()
	registry.Register("observer", obs)
	engine := pipeline.NewEngine(registry)

	exec := &pipeline.ForwardExecution{
		ExecutedNodes: []pipeline.ExecutedNode{
			{Node: model.PipelineNode{ID: "observer", Type: "observer"}},
		},
	}

	inner := &gatedClient{gate: make(chan struct{})}
	wrapped := NewPipelineWrappedClient(inner, engine, exec, pipeline.ExecutionContext{})

	ch, err := wrapped.ChatCompletionStream(context.Background())
	if err != nil {
		t.Fatalf("ChatCompletionStream() error = %v", err)
	}

	// The provider is blocked on the gate, so this read can only succeed if
	// the first chunk was forwarded rather than buffered.
	select {
	case chunk, ok := <-ch:
		if !ok {
			t.Fatal("stream closed before the first chunk")
		}
		if got := chunk.Delta().Content(); got != "premier " {
			t.Errorf("first chunk = %q, want %q", got, "premier ")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first chunk was withheld: the stream is still being buffered")
	}

	close(inner.gate)

	if got, want := "premier "+collectContent(t, ch), "premier morceau."; got != want {
		t.Errorf("content = %q, want %q", got, want)
	}

	// The backward pass still runs, on the whole response.
	content, called := obs.seen()
	if !called {
		t.Fatal("backward pass did not run")
	}
	if want := "premier morceau."; content != want {
		t.Errorf("backward content = %q, want %q", content, want)
	}
}

// A node that can rewrite responses in general but declared during its forward
// pass that it will not for this execution — e.g. the pseudonymizer when it
// found nothing sensitive — must not force the stream to be buffered.
func TestPipelineWrappedClient_ChatCompletionStream_NoResponseRewriteStreamsLive(t *testing.T) {
	engine := newTestEngine("premier", "REWRITTEN")
	exec := &pipeline.ForwardExecution{
		ExecutedNodes: []pipeline.ExecutedNode{
			{Node: model.PipelineNode{ID: "rewrite", Type: "rewrite"}, NoResponseRewrite: true},
		},
	}

	inner := &gatedClient{gate: make(chan struct{})}
	wrapped := NewPipelineWrappedClient(inner, engine, exec, pipeline.ExecutionContext{})

	ch, err := wrapped.ChatCompletionStream(context.Background())
	if err != nil {
		t.Fatalf("ChatCompletionStream() error = %v", err)
	}

	select {
	case chunk, ok := <-ch:
		if !ok {
			t.Fatal("stream closed before the first chunk")
		}
		if got := chunk.Delta().Content(); got != "premier " {
			t.Errorf("first chunk = %q, want %q", got, "premier ")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first chunk was withheld: NoResponseRewrite was ignored")
	}

	close(inner.gate)

	// The rewrite must not be applied: the node said it would not rewrite.
	if got, want := "premier "+collectContent(t, ch), "premier morceau."; got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

// An executor that does not declare ResponseModifier may rewrite the response,
// so the stream must still be buffered for it.
func TestPipelineWrappedClient_UndeclaredExecutorStillBuffers(t *testing.T) {
	engine := newTestEngine("a", "b")
	if !engine.MayModifyResponse(context.Background(), newTestForwardExecution()) {
		t.Error("an executor without ModifiesResponse must be assumed to modify the response")
	}
}

func TestPipelineWrappedClient_ChatCompletionStream_Deanonymize(t *testing.T) {
	inner := &multiChunkClient{parts: []string{"Bonjour [PERSON_1], votre email est ", "[EMAIL_1]."}}
	engine := newTestEngine("[EMAIL_1]", "wpetit@cadoles.com")
	wrapped := NewPipelineWrappedClient(inner, engine, newTestForwardExecution(), pipeline.ExecutionContext{})

	ch, err := wrapped.ChatCompletionStream(context.Background())
	if err != nil {
		t.Fatalf("ChatCompletionStream() error = %v", err)
	}

	got := collectContent(t, ch)
	want := "Bonjour [PERSON_1], votre email est wpetit@cadoles.com."
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestPipelineWrappedClient_ChatCompletionStream_Unmodified(t *testing.T) {
	inner := &multiChunkClient{parts: []string{"Bonjour ", "tout le monde."}}
	engine := newTestEngine("[EMAIL_1]", "wpetit@cadoles.com")
	wrapped := NewPipelineWrappedClient(inner, engine, newTestForwardExecution(), pipeline.ExecutionContext{})

	ch, err := wrapped.ChatCompletionStream(context.Background())
	if err != nil {
		t.Fatalf("ChatCompletionStream() error = %v", err)
	}

	got := collectContent(t, ch)
	want := "Bonjour tout le monde."
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

// hangingClient emits one chunk, then holds the stream open until the caller
// cancels, closing it with the error a provider reports in that case.
type hangingClient struct{ multiChunkClient }

func (c *hangingClient) ChatCompletionStream(ctx context.Context, _ ...llm.ChatCompletionOptionFunc) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, 2)
	go func() {
		defer close(ch)
		ch <- llm.NewStreamChunk(llm.NewStreamDelta(llm.RoleAssistant, "Bonjour [PERSON_1]"))
		<-ctx.Done()
		ch <- llm.NewErrorStreamChunk(ctx.Err())
	}()
	return ch, nil
}

// recordingExecutor may rewrite the response and records the context and the
// error flag its backward pass received. Backward waits for release when set.
type recordingExecutor struct {
	release chan struct{}
	done    chan struct{}
	ctxErr  error
	hadErr  bool
}

func (e *recordingExecutor) Forward(_ context.Context, _ model.PipelineNode, _ map[string]interface{}, _ pipeline.ExecutionContext) (*pipeline.ForwardResult, error) {
	return &pipeline.ForwardResult{}, nil
}

func (e *recordingExecutor) Backward(ctx context.Context, in pipeline.BackwardInput) (*pipeline.BackwardResult, error) {
	if e.release != nil {
		<-e.release
	}
	e.ctxErr = ctx.Err()
	e.hadErr = in.HadError
	close(e.done)
	return &pipeline.BackwardResult{ModifiedResponseContent: in.ResponseContent + "!"}, nil
}

func newRecordingEngine(exec *recordingExecutor) *pipeline.Engine {
	registry := pipeline.NewRegistry()
	registry.Register("rewrite", exec)
	return pipeline.NewEngine(registry)
}

// A caller that goes away while the response is buffered must not leave the
// backward pass with a canceled context: the pseudonymizer's PostResponse
// would fail on it.
func TestPipelineWrappedClient_ChatCompletionStream_BufferedCancelRunsBackward(t *testing.T) {
	exec := &recordingExecutor{done: make(chan struct{})}
	wrapped := NewPipelineWrappedClient(&hangingClient{}, newRecordingEngine(exec), newTestForwardExecution(), pipeline.ExecutionContext{})

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := wrapped.ChatCompletionStream(ctx)
	if err != nil {
		t.Fatalf("ChatCompletionStream() error = %v", err)
	}
	cancel()

	select {
	case <-exec.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the backward pass did not run")
	}
	if exec.ctxErr != nil {
		t.Errorf("backward pass context error = %v, want none", exec.ctxErr)
	}
	if !exec.hadErr {
		t.Error("the backward pass must be told the response is partial")
	}

	select {
	case _, ok := <-ch:
		for ok {
			_, ok = <-ch
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the stream was not closed")
	}
}

// Once the caller is gone, re-emitting the buffered chunks must stop instead
// of blocking forever on a channel nobody reads.
func TestPipelineWrappedClient_ChatCompletionStream_BufferedEmitStopsWhenCallerLeaves(t *testing.T) {
	parts := make([]string, 32)
	for i := range parts {
		parts[i] = "x"
	}
	exec := &recordingExecutor{release: make(chan struct{}), done: make(chan struct{})}
	wrapped := NewPipelineWrappedClient(&multiChunkClient{parts: parts}, newRecordingEngine(exec), newTestForwardExecution(), pipeline.ExecutionContext{})

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := wrapped.ChatCompletionStream(ctx)
	if err != nil {
		t.Fatalf("ChatCompletionStream() error = %v", err)
	}
	cancel()
	close(exec.release)
	<-exec.done
	// Nobody reads while the chunks are re-emitted: only what fits in the
	// channel's buffer may be waiting there, the rest must be dropped.
	time.Sleep(50 * time.Millisecond)

	deadline := time.After(2 * time.Second)
	received := 0
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				if received >= len(parts) {
					t.Errorf("received %d chunks after the caller left, want only the buffered ones", received)
				}
				return
			}
			received++
		case <-deadline:
			t.Fatal("the stream was not closed once the caller left")
		}
	}
}
