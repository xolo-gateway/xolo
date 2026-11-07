package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bornholm/genai/llm"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// failingClient fails every call.
type failingClient struct{ dummyClient }

type chunksClient struct {
	dummyClient
	chunks []llm.StreamChunk
}

type errorClient struct {
	dummyClient
	err error
}

func (c *errorClient) ChatCompletion(context.Context, ...llm.ChatCompletionOptionFunc) (llm.ChatCompletionResponse, error) {
	return nil, c.err
}

type waitingClient struct{ dummyClient }

func (waitingClient) ChatCompletionStream(context.Context, ...llm.ChatCompletionOptionFunc) (<-chan llm.StreamChunk, error) {
	return make(chan llm.StreamChunk), nil
}

type abandonedClient struct {
	dummyClient
	done chan struct{}
}

type delayedAbandonedClient struct {
	dummyClient
	release chan struct{}
	done    chan struct{}
}

func (c *delayedAbandonedClient) ChatCompletionStream(context.Context, ...llm.ChatCompletionOptionFunc) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, 1)
	go func() {
		defer close(c.done)
		defer close(ch)
		ch <- llm.NewErrorStreamChunk(errors.New("upstream"))
		<-c.release
		ch <- llm.NewCompleteStreamChunk(nil)
	}()
	return ch, nil
}

func (c *abandonedClient) ChatCompletionStream(context.Context, ...llm.ChatCompletionOptionFunc) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk)
	go func() {
		defer close(c.done)
		defer close(ch)
		ch <- llm.NewErrorStreamChunk(errors.New("upstream"))
		ch <- llm.NewCompleteStreamChunk(nil)
	}()
	return ch, nil
}

func (c *chunksClient) ChatCompletionStream(_ context.Context, _ ...llm.ChatCompletionOptionFunc) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, len(c.chunks))
	for _, chunk := range c.chunks {
		ch <- chunk
	}
	close(ch)
	return ch, nil
}

func (failingClient) ChatCompletion(context.Context, ...llm.ChatCompletionOptionFunc) (llm.ChatCompletionResponse, error) {
	return nil, errors.New("upstream down")
}

func (failingClient) ChatCompletionStream(context.Context, ...llm.ChatCompletionOptionFunc) (<-chan llm.StreamChunk, error) {
	return nil, errors.New("upstream down")
}

type fakeResolver struct {
	clients  map[string]llm.Client
	resolved []string
}

func (r *fakeResolver) ResolveRealModel(_ context.Context, _ model.OrgID, proxyName string) (llm.Client, string, model.LLMModelID, error) {
	r.resolved = append(r.resolved, proxyName)
	c, ok := r.clients[proxyName]
	if !ok {
		return nil, "", "", errors.New("unknown model " + proxyName)
	}
	return c, "real-" + proxyName, model.LLMModelID("id-" + proxyName), nil
}

func fallbackNode(models ...string) model.PipelineNode {
	data, _ := json.Marshal(model.ModelFallbackNodeData{Models: models})
	return model.PipelineNode{ID: "fb", Type: model.NodeTypeModelFallback, Data: data}
}

func TestModelFallback_PrimaryAnswers(t *testing.T) {
	r := &fakeResolver{clients: map[string]llm.Client{"a": newDummyClient("from a"), "b": newDummyClient("from b")}}
	res, err := NewModelFallbackExecutor(r).Forward(context.Background(), fallbackNode("a", "b"), nil, ExecutionContext{})
	if err != nil {
		t.Fatal(err)
	}
	if res.ResolvedModel != "real-a" || res.ResolvedModelID != "id-a" {
		t.Errorf("primary must be reported as resolved model: %s %s", res.ResolvedModel, res.ResolvedModelID)
	}
	resp, err := res.ResolvedClient.ChatCompletion(context.Background())
	if err != nil || resp.Message().Content() != "from a" {
		t.Fatalf("expected the primary answer, got %v %v", resp, err)
	}
	if len(r.resolved) != 1 {
		t.Errorf("secondary must not be resolved when unused, resolved: %v", r.resolved)
	}
	if real, id, ok := res.ModelOutcome.UsedModel(); !ok || real != "real-a" || id != "id-a" {
		t.Errorf("used model: %s %s %v", real, id, ok)
	}
}

func TestModelFallback_SwitchesOnFailure(t *testing.T) {
	r := &fakeResolver{clients: map[string]llm.Client{"a": &failingClient{}, "b": &failingClient{}, "c": newDummyClient("from c")}}
	res, err := NewModelFallbackExecutor(r).Forward(context.Background(), fallbackNode("a", "b", "c"), nil, ExecutionContext{})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := res.ResolvedClient.ChatCompletion(context.Background())
	if err != nil || resp.Message().Content() != "from c" {
		t.Fatalf("expected the third model to answer, got %v %v", resp, err)
	}
	if real, id, ok := res.ModelOutcome.UsedModel(); !ok || real != "real-c" || id != "id-c" {
		t.Errorf("usage must be attributed to the model that answered: %s %s %v", real, id, ok)
	}
	// Streaming follows the same chain.
	ch, err := res.ResolvedClient.ChatCompletionStream(context.Background())
	if err != nil || ch == nil {
		t.Fatalf("stream fallback: %v", err)
	}
}

func TestModelFallback_AllFail(t *testing.T) {
	r := &fakeResolver{clients: map[string]llm.Client{"a": &failingClient{}}}
	res, err := NewModelFallbackExecutor(r).Forward(context.Background(), fallbackNode("a", "missing"), nil, ExecutionContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := res.ResolvedClient.ChatCompletion(context.Background()); err == nil {
		t.Error("expected an error when every candidate fails")
	}
	if _, _, ok := res.ModelOutcome.UsedModel(); ok {
		t.Error("no model must be reported as used")
	}
}

func TestModelFallback_PortFirstAndValidation(t *testing.T) {
	r := &fakeResolver{clients: map[string]llm.Client{"port": newDummyClient("p"), "cfg": newDummyClient("c")}}
	res, err := NewModelFallbackExecutor(r).Forward(context.Background(), fallbackNode("cfg"), map[string]interface{}{"model_name": "port"}, ExecutionContext{})
	if err != nil || res.ResolvedModel != "real-port" {
		t.Errorf("connected model_name must come first: %v %v", res, err)
	}
	if _, err := NewModelFallbackExecutor(r).Forward(context.Background(), fallbackNode(), nil, ExecutionContext{}); err == nil {
		t.Error("no model must fail")
	}
	if _, err := NewModelFallbackExecutor(r).Forward(context.Background(), fallbackNode("missing", "cfg"), nil, ExecutionContext{}); err == nil {
		t.Error("an unresolvable primary must fail at pipeline time")
	}
}

func TestModelFallback_StreamFirstErrorFallsBack(t *testing.T) {
	upstream := errors.New("upstream 429")
	r := &fakeResolver{clients: map[string]llm.Client{
		"a": &chunksClient{chunks: []llm.StreamChunk{llm.NewErrorStreamChunk(upstream)}},
		"b": newDummyClient("from b"),
	}}
	res, err := NewModelFallbackExecutor(r).Forward(context.Background(), fallbackNode("a", "missing", "b"), nil, ExecutionContext{})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := res.ResolvedClient.ChatCompletionStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first := <-ch
	if first == nil || first.Delta() == nil || first.Delta().Content() != "from b" {
		t.Fatalf("first chunk = %v", first)
	}
	for range ch {
	}
	if real, _, ok := res.ModelOutcome.UsedModel(); !ok || real != "real-b" {
		t.Fatalf("used model = %q, %v", real, ok)
	}
}

func TestModelFallback_EmptyStreamFallsBack(t *testing.T) {
	r := &fakeResolver{clients: map[string]llm.Client{"a": &chunksClient{}, "b": newDummyClient("from b")}}
	res, err := NewModelFallbackExecutor(r).Forward(context.Background(), fallbackNode("a", "b"), nil, ExecutionContext{})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := res.ResolvedClient.ChatCompletionStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first := <-ch; first.Delta().Content() != "from b" {
		t.Fatalf("first chunk = %v", first)
	}
	for range ch {
	}
	if real, _, ok := res.ModelOutcome.UsedModel(); !ok || real != "real-b" {
		t.Fatalf("used model = %q, %v", real, ok)
	}
}

func TestModelFallback_AllEmptyStreamsFail(t *testing.T) {
	r := &fakeResolver{clients: map[string]llm.Client{"a": &chunksClient{}, "b": &chunksClient{}}}
	res, err := NewModelFallbackExecutor(r).Forward(context.Background(), fallbackNode("a", "b"), nil, ExecutionContext{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = res.ResolvedClient.ChatCompletionStream(context.Background())
	if err == nil || !strings.Contains(err.Error(), "stream closed before the first chunk") {
		t.Fatalf("error = %v", err)
	}
	if _, _, ok := res.ModelOutcome.UsedModel(); ok {
		t.Fatal("empty stream attributed to a model")
	}
}

func TestModelFallback_StreamErrorAfterContentDoesNotSwitch(t *testing.T) {
	upstream := errors.New("late error")
	r := &fakeResolver{clients: map[string]llm.Client{
		"a": &chunksClient{chunks: []llm.StreamChunk{llm.NewStreamChunk(llm.NewStreamDelta(llm.RoleAssistant, "partial")), llm.NewErrorStreamChunk(upstream)}},
		"b": newDummyClient("from b"),
	}}
	res, err := NewModelFallbackExecutor(r).Forward(context.Background(), fallbackNode("a", "b"), nil, ExecutionContext{})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := res.ResolvedClient.ChatCompletionStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first, second := <-ch, <-ch
	if first.Delta().Content() != "partial" || !errors.Is(second.Error(), upstream) {
		t.Fatal("stream was not forwarded intact")
	}
	if len(r.resolved) != 1 {
		t.Fatalf("unexpected fallback: %v", r.resolved)
	}
}

func TestModelFallback_ProviderErrorSurvivesResolutionFailure(t *testing.T) {
	upstream := llm.NewHTTPError(429, "overloaded")
	r := &fakeResolver{clients: map[string]llm.Client{"a": &chunksClient{chunks: []llm.StreamChunk{llm.NewErrorStreamChunk(upstream)}}}}
	res, err := NewModelFallbackExecutor(r).Forward(context.Background(), fallbackNode("a", "missing"), nil, ExecutionContext{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = res.ResolvedClient.ChatCompletionStream(context.Background())
	if !errors.Is(err, upstream) {
		t.Fatalf("got %v, want upstream error", err)
	}
	var httpErr *llm.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != 429 {
		t.Fatalf("lost HTTP status: %v", err)
	}
}

func TestModelFallback_NonStreamSkipsResolutionFailure(t *testing.T) {
	upstream := errors.New("upstream 429")
	r := &fakeResolver{clients: map[string]llm.Client{"a": &errorClient{err: upstream}, "b": newDummyClient("from b")}}
	res, err := NewModelFallbackExecutor(r).Forward(context.Background(), fallbackNode("a", "missing", "b"), nil, ExecutionContext{})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		resp, err := res.ResolvedClient.ChatCompletion(context.Background())
		if err != nil || resp.Message().Content() != "from b" {
			t.Fatalf("response = %v, error = %v", resp, err)
		}
	}
	res, err = NewModelFallbackExecutor(r).Forward(context.Background(), fallbackNode("a", "missing"), nil, ExecutionContext{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = res.ResolvedClient.ChatCompletion(context.Background())
	if !errors.Is(err, upstream) {
		t.Fatalf("got %v, want upstream error", err)
	}
}

func TestModelFallback_StreamWaitHonorsCancellation(t *testing.T) {
	r := &fakeResolver{clients: map[string]llm.Client{"a": &waitingClient{}, "b": newDummyClient("from b")}}
	res, err := NewModelFallbackExecutor(r).Forward(context.Background(), fallbackNode("a", "b"), nil, ExecutionContext{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := res.ResolvedClient.ChatCompletionStream(ctx); done <- err }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if len(r.resolved) != 1 {
		t.Fatalf("unexpected fallback: %v", r.resolved)
	}
}

func TestModelFallback_DrainsAbandonedStream(t *testing.T) {
	a := &abandonedClient{done: make(chan struct{})}
	r := &fakeResolver{clients: map[string]llm.Client{"a": a, "b": newDummyClient("from b")}}
	res, err := NewModelFallbackExecutor(r).Forward(context.Background(), fallbackNode("a", "b"), nil, ExecutionContext{})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := res.ResolvedClient.ChatCompletionStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	select {
	case <-a.done:
	case <-time.After(2 * time.Second):
		t.Fatal("abandoned producer did not finish")
	}
}

func TestModelFallback_ConfiguredDrainTimeout(t *testing.T) {
	a := &delayedAbandonedClient{release: make(chan struct{}), done: make(chan struct{})}
	r := &fakeResolver{clients: map[string]llm.Client{"a": a, "b": newDummyClient("from b")}}
	res, err := NewModelFallbackExecutor(r).WithDrainTimeout(3*time.Second).Forward(context.Background(), fallbackNode("a", "b"), nil, ExecutionContext{})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := res.ResolvedClient.ChatCompletionStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	time.Sleep(1200 * time.Millisecond)
	close(a.release)
	select {
	case <-a.done:
	case <-time.After(time.Second):
		t.Fatal("configured drain did not consume delayed chunk")
	}
}

func TestModelFallback_ConfiguredDrainTimeoutStopsWaiting(t *testing.T) {
	a := &delayedAbandonedClient{release: make(chan struct{}), done: make(chan struct{})}
	r := &fakeResolver{clients: map[string]llm.Client{"a": a, "b": newDummyClient("from b")}}
	res, err := NewModelFallbackExecutor(r).WithDrainTimeout(50*time.Millisecond).Forward(context.Background(), fallbackNode("a", "b"), nil, ExecutionContext{})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := res.ResolvedClient.ChatCompletionStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	select {
	case <-a.done:
		t.Fatal("drain completed before its configured timeout")
	case <-time.After(200 * time.Millisecond):
	}
	close(a.release)
	select {
	case <-a.done:
	case <-time.After(time.Second):
		t.Fatal("abandoned producer did not finish after release")
	}
}
