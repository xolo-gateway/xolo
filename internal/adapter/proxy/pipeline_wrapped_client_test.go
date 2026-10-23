package proxy

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
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
// cancels, closing it with the error a provider reports in that case. The
// chunk is first, or a text one when first is nil.
type hangingClient struct {
	multiChunkClient
	first llm.StreamChunk
}

func (c *hangingClient) ChatCompletionStream(ctx context.Context, _ ...llm.ChatCompletionOptionFunc) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, 2)
	go func() {
		defer close(ch)
		first := c.first
		if first == nil {
			first = llm.NewStreamChunk(llm.NewStreamDelta(llm.RoleAssistant, "Bonjour [PERSON_1]"))
		}
		ch <- first
		<-ctx.Done()
		ch <- llm.NewErrorStreamChunk(ctx.Err())
	}()
	return ch, nil
}

func (c *hangingClient) ChatCompletion(ctx context.Context, _ ...llm.ChatCompletionOptionFunc) (llm.ChatCompletionResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// recordingExecutor may rewrite the response and records the context and the
// error flag its backward pass received. When set, entered is closed as
// Backward starts, and Backward then waits for release.
type recordingExecutor struct {
	entered   chan struct{}
	release   chan struct{}
	done      chan struct{}
	ctxErr    error
	hadErr    bool
	toolCalls string
	// bounded tells whether the backward pass had a deadline.
	bounded bool
}

func (e *recordingExecutor) Forward(_ context.Context, _ model.PipelineNode, _ map[string]interface{}, _ pipeline.ExecutionContext) (*pipeline.ForwardResult, error) {
	return &pipeline.ForwardResult{}, nil
}

func (e *recordingExecutor) Backward(ctx context.Context, in pipeline.BackwardInput) (*pipeline.BackwardResult, error) {
	if e.entered != nil {
		close(e.entered)
	}
	if e.release != nil {
		<-e.release
	}
	e.ctxErr = ctx.Err()
	_, e.bounded = ctx.Deadline()
	e.hadErr = in.HadError
	e.toolCalls = in.ToolCallsJSON
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
	if !exec.bounded {
		t.Error("the backward pass has no deadline: a hanging node would hold the goroutine forever")
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
// of blocking forever on a channel nobody reads. The caller leaves while the
// backward pass runs, so the source was fully buffered with a live context and
// only the re-emission can notice the cancellation.
func TestPipelineWrappedClient_ChatCompletionStream_BufferedEmitStopsWhenCallerLeaves(t *testing.T) {
	parts := make([]string, 32)
	for i := range parts {
		parts[i] = "x"
	}
	exec := &recordingExecutor{entered: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
	wrapped := NewPipelineWrappedClient(&multiChunkClient{parts: parts}, newRecordingEngine(exec), newTestForwardExecution(), pipeline.ExecutionContext{})

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := wrapped.ChatCompletionStream(ctx)
	if err != nil {
		t.Fatalf("ChatCompletionStream() error = %v", err)
	}

	<-exec.entered
	cancel()
	close(exec.release)
	<-exec.done
	if exec.hadErr {
		t.Fatal("the source was fully read before the caller left: the backward pass must not see an error")
	}
	if exec.ctxErr != nil {
		t.Errorf("the backward pass saw a canceled context (%v): PostResponse would fail on it", exec.ctxErr)
	}
	if !exec.bounded {
		t.Error("the backward pass has no deadline: a hanging node would hold the goroutine forever")
	}

	deadline := time.After(2 * time.Second)
	received := 0
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				if received != 0 {
					t.Errorf("received %d chunks after the caller left, want none", received)
				}
				return
			}
			received++
		case <-deadline:
			t.Fatal("the stream was not closed once the caller left")
		}
	}
}

// passiveRecordingExecutor records like recordingExecutor but declares it
// never rewrites the response, so the stream takes the passthrough path.
type passiveRecordingExecutor struct {
	*recordingExecutor
}

func (e passiveRecordingExecutor) ModifiesResponse(context.Context, model.PipelineNode) bool {
	return false
}

// lingeringClient streams its parts, then its completion chunk when complete
// is set, and closes the stream only once the caller is gone: the provider
// stopping because the request was canceled.
type lingeringClient struct {
	multiChunkClient
	complete bool
}

func (c *lingeringClient) ChatCompletionStream(ctx context.Context, _ ...llm.ChatCompletionOptionFunc) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, len(c.parts)+1)
	go func() {
		defer close(ch)
		for _, p := range c.parts {
			ch <- llm.NewStreamChunk(llm.NewStreamDelta(llm.RoleAssistant, p))
		}
		if c.complete {
			ch <- llm.NewCompleteStreamChunk(llm.NewChatCompletionUsage(3, 7, 10))
		}
		<-ctx.Done()
	}()
	return ch, nil
}

// The backward pass is told the answer is partial when the caller left before
// the provider finished it, and only then: on the passthrough path the source
// simply ends, on the buffered path the answer may be whole though nobody
// reads it.
func TestPipelineWrappedClient_ChatCompletionStream_CallerLeavesHadError(t *testing.T) {
	for name, tc := range map[string]struct {
		passthrough bool
		complete    bool
		hadErr      bool
	}{
		"passthrough cut":   {passthrough: true, complete: false, hadErr: true},
		"passthrough whole": {passthrough: true, complete: true, hadErr: false},
		"buffered cut":      {passthrough: false, complete: false, hadErr: true},
		"buffered whole":    {passthrough: false, complete: true, hadErr: false},
	} {
		t.Run(name, func(t *testing.T) {
			exec := &recordingExecutor{done: make(chan struct{})}
			registry := pipeline.NewRegistry()
			if tc.passthrough {
				registry.Register("rewrite", passiveRecordingExecutor{exec})
			} else {
				registry.Register("rewrite", exec)
			}
			client := &lingeringClient{multiChunkClient: multiChunkClient{parts: []string{"a", "b"}}, complete: tc.complete}
			wrapped := NewPipelineWrappedClient(client, pipeline.NewEngine(registry), newTestForwardExecution(), pipeline.ExecutionContext{})

			ctx, cancel := context.WithCancel(context.Background())
			ch, err := wrapped.ChatCompletionStream(ctx)
			if err != nil {
				t.Fatalf("ChatCompletionStream() error = %v", err)
			}
			if tc.passthrough {
				<-ch
				<-ch
			}
			cancel()
			received := make(chan []llm.StreamChunk, 1)
			go func() {
				var chunks []llm.StreamChunk
				for chunk := range ch {
					chunks = append(chunks, chunk)
				}
				received <- chunks
			}()

			select {
			case <-exec.done:
			case <-time.After(2 * time.Second):
				t.Fatal("the backward pass did not run")
			}
			if exec.hadErr != tc.hadErr {
				t.Errorf("backward pass hadError = %v, want %v", exec.hadErr, tc.hadErr)
			}
			if tc.passthrough {
				return
			}
			// The proxy runs its post-response hooks, which record usage,
			// only once it got a chunk: a stream closed empty would run its
			// error hooks alone.
			var chunks []llm.StreamChunk
			select {
			case chunks = <-received:
			case <-time.After(2 * time.Second):
				t.Fatal("the stream was not closed")
			}
			if len(chunks) != 1 || chunks[0].Error() != nil {
				t.Fatalf("got %d chunks, want the one carrying the usage", len(chunks))
			}
			if chunks[0].IsComplete() != tc.complete {
				t.Errorf("chunk complete = %v, want %v", chunks[0].IsComplete(), tc.complete)
			}
			if tc.complete && (chunks[0].Usage() == nil || chunks[0].Usage().TotalTokens() != 10) {
				t.Errorf("the chunk does not carry the usage the provider reported")
			}
		})
	}
}

// A caller that leaves once the whole answer was relayed, while the backward
// pass runs, must not cancel it.
func TestPipelineWrappedClient_ChatCompletionStream_PassthroughBackwardOutlivesCaller(t *testing.T) {
	exec := &recordingExecutor{entered: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
	registry := pipeline.NewRegistry()
	registry.Register("rewrite", passiveRecordingExecutor{exec})
	wrapped := NewPipelineWrappedClient(&multiChunkClient{parts: []string{"a", "b", "c"}}, pipeline.NewEngine(registry), newTestForwardExecution(), pipeline.ExecutionContext{})

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := wrapped.ChatCompletionStream(ctx)
	if err != nil {
		t.Fatalf("ChatCompletionStream() error = %v", err)
	}
	go func() {
		for range ch {
		}
	}()

	<-exec.entered
	cancel()
	close(exec.release)
	<-exec.done
	if exec.hadErr {
		t.Error("the answer was fully relayed: the backward pass must not see an error")
	}
	if exec.ctxErr != nil {
		t.Errorf("the backward pass saw a canceled context (%v): PostResponse would fail on it", exec.ctxErr)
	}
	if !exec.bounded {
		t.Error("the backward pass has no deadline: a hanging node would hold the goroutine forever")
	}
}

func assertDetachedBackward(t *testing.T, exec *recordingExecutor) {
	t.Helper()
	if exec.ctxErr != nil {
		t.Errorf("the backward pass saw a canceled context (%v): PostResponse would fail on it", exec.ctxErr)
	}
	if !exec.bounded {
		t.Error("the backward pass has no deadline: a hanging node would hold the goroutine forever")
	}
}

// A caller that leaves while the provider answers must not cancel the
// backward pass that follows the failed call.
func TestPipelineWrappedClient_ChatCompletion_BackwardOutlivesCallerOnError(t *testing.T) {
	exec := &recordingExecutor{done: make(chan struct{})}
	wrapped := NewPipelineWrappedClient(&hangingClient{}, newRecordingEngine(exec), newTestForwardExecution(), pipeline.ExecutionContext{})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := wrapped.ChatCompletion(ctx)
		errCh <- err
	}()
	cancel()

	select {
	case <-exec.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the backward pass never ran after the caller left")
	}
	if !exec.hadErr {
		t.Error("the call failed: the backward pass must be told so")
	}
	assertDetachedBackward(t, exec)
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Errorf("ChatCompletion() error = %v, want context.Canceled", err)
	}
}

// A caller that leaves while the backward pass runs must not cancel it.
func TestPipelineWrappedClient_ChatCompletion_BackwardOutlivesCaller(t *testing.T) {
	exec := &recordingExecutor{entered: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
	wrapped := NewPipelineWrappedClient(&toolCallClient{}, newRecordingEngine(exec), newTestForwardExecution(), pipeline.ExecutionContext{})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _, _ = wrapped.ChatCompletion(ctx) }()

	<-exec.entered
	cancel()
	close(exec.release)
	<-exec.done
	if exec.hadErr {
		t.Error("the call succeeded: the backward pass must not see an error")
	}
	assertDetachedBackward(t, exec)
}

// A caller gone before the rewrite still leaves the nodes the tool calls the
// model emitted, as the rewriting path does.
func TestPipelineWrappedClient_ChatCompletionStream_BufferedCancelKeepsToolCalls(t *testing.T) {
	exec := &recordingExecutor{done: make(chan struct{})}
	first := llm.NewStreamChunk(llm.NewStreamDelta(llm.RoleAssistant, "", llm.NewToolCallDelta(0, "call_1", "Read", `{"path":"/home/PERSON_1"}`)))
	wrapped := NewPipelineWrappedClient(&hangingClient{first: first}, newRecordingEngine(exec), newTestForwardExecution(), pipeline.ExecutionContext{})

	ctx, cancel := context.WithCancel(context.Background())
	if _, err := wrapped.ChatCompletionStream(ctx); err != nil {
		t.Fatalf("ChatCompletionStream() error = %v", err)
	}
	cancel()

	select {
	case <-exec.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the backward pass never ran after the caller left")
	}
	if !strings.Contains(exec.toolCalls, "call_1") {
		t.Errorf("backward pass tool calls = %q, want the streamed call", exec.toolCalls)
	}
}

// stallingExecutor may rewrite the response but only returns once its
// context is done, as a plugin that hangs.
type stallingExecutor struct{ recordingExecutor }

func (e *stallingExecutor) Backward(ctx context.Context, _ pipeline.BackwardInput) (*pipeline.BackwardResult, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// The backward pass is bounded by the configured timeout, 30 s by default,
// and unbounded at zero; it never ends with the caller.
func TestPipelineWrappedClient_BackwardContextTimeout(t *testing.T) {
	caller, cancel := context.WithCancel(context.Background())
	cancel()
	for name, tc := range map[string]struct {
		timeout  time.Duration
		bounded  bool
		override bool
	}{
		"default":    {DefaultBackwardTimeout, true, false},
		"configured": {time.Minute, true, true},
		"zero":       {0, false, true},
	} {
		t.Run(name, func(t *testing.T) {
			client := NewPipelineWrappedClient(nil, nil, nil, pipeline.ExecutionContext{})
			if tc.override {
				client.WithBackwardTimeout(tc.timeout)
			}
			ctx, cancelBack := client.detachedBackwardContext(caller)
			defer cancelBack()
			if ctx.Err() != nil {
				t.Fatalf("the backward context ended with the caller: %v", ctx.Err())
			}
			deadline, ok := ctx.Deadline()
			if ok != tc.bounded {
				t.Fatalf("deadline set = %v, want %v", ok, tc.bounded)
			}
			if ok && time.Until(deadline) > tc.timeout {
				t.Errorf("deadline in %v, want at most %v", time.Until(deadline), tc.timeout)
			}
		})
	}
}

// A node that hangs is cut by the timeout: the answer goes out as the model
// wrote it, and the log says the pass timed out.
func TestPipelineWrappedClient_ChatCompletionStream_BackwardTimeout(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	registry := pipeline.NewRegistry()
	registry.Register("rewrite", &stallingExecutor{})
	exec := &pipeline.ForwardExecution{ExecutedNodes: []pipeline.ExecutedNode{
		{Node: model.PipelineNode{ID: "first", Type: "rewrite"}},
		{Node: model.PipelineNode{ID: "last", Type: "rewrite"}},
	}}
	wrapped := NewPipelineWrappedClient(&multiChunkClient{parts: []string{"Bonjour ", "[PERSON_1]"}}, pipeline.NewEngine(registry), exec, pipeline.ExecutionContext{}).
		WithBackwardTimeout(20 * time.Millisecond)

	ch, err := wrapped.ChatCompletionStream(context.Background())
	if err != nil {
		t.Fatalf("ChatCompletionStream() error = %v", err)
	}

	done := make(chan string, 1)
	go func() { done <- collectContent(t, ch) }()
	select {
	case content := <-done:
		if content != "Bonjour [PERSON_1]" {
			t.Errorf("content = %q, want the answer as the model wrote it", content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the stream was not closed once the backward pass timed out")
	}
	if !strings.Contains(logs.String(), "backward pass timed out") {
		t.Errorf("logs = %q, want the timeout told apart", logs.String())
	}
	// The node the deadline cut is named once; the other one is skipped
	// instead of failing on an expired context and being logged as cut too.
	if n := strings.Count(logs.String(), "backward pass timed out for a node"); n != 1 {
		t.Errorf("logs = %q, want one node reported as cut, got %d", logs.String(), n)
	}
	if !strings.Contains(logs.String(), "skipping the remaining nodes") {
		t.Errorf("logs = %q, want the remaining nodes reported as skipped", logs.String())
	}
}
