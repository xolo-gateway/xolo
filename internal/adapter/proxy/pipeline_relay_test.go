package proxy

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bornholm/genai/llm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/pipeline"
)

// relayingClient relays a fixed list of Anthropic stream events.
type relayingClient struct {
	multiChunkClient
	events []string
}

func (c *relayingClient) RelayMessages(context.Context, []byte, http.Header) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, len(c.events))
	for i, event := range c.events {
		ch <- llm.NewRawEventChunk([]byte(event), llm.NewChatCompletionUsage(10, 5, 15), i == len(c.events)-1)
	}
	close(ch)
	return ch, nil
}

func sse(eventType, data string) string {
	return "event: " + eventType + "\ndata: " + data + "\n\n"
}

// pseudonymizedTurn is a text block and a tool call whose placeholder is split
// across deltas, as the API streams them, followed by the safety verdict
// Claude Code needs back untouched.
var pseudonymizedTurn = []string{
	sse("message_start", `{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":10}}}`),
	sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
	sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi PER"}}`),
	sse("ping", `{"type":"ping"}`),
	sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"SON_1 <3"}}`),
	sse("content_block_stop", `{"type":"content_block_stop","index":0}`),
	sse("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_01","name":"Bash","input":{}}}`),
	sse("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"ls /home/PER"}}`),
	sse("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"SON_1\"}"}}`),
	sse("content_block_stop", `{"type":"content_block_stop","index":1}`),
	sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","safeguard_results":[{"type":"dangerous_tool_use","status":{"type":"available","tool_uses":{"toolu_01":{"type":"evaluated","outcome":"not_flagged"}}}}]},"usage":{"output_tokens":5}}`),
	sse("message_stop", `{"type":"message_stop"}`),
}

func relayedEvents(t *testing.T, ch <-chan llm.StreamChunk) []string {
	t.Helper()
	var events []string
	for chunk := range ch {
		raw, ok := chunk.(llm.RawEventChunk)
		if !ok {
			t.Fatalf("chunk %T is not a relayed event", chunk)
		}
		events = append(events, string(raw.RawEvent()))
	}
	return events
}

func TestPipelineWrappedClient_RelayMessages_RestoresHeldContent(t *testing.T) {
	inner := &relayingClient{events: pseudonymizedTurn}
	wrapped := NewPipelineWrappedClient(inner, newTestEngine("PERSON_1", "Alice"), newTestForwardExecution(), pipeline.ExecutionContext{})

	if !llm.SupportsMessagesRelay(wrapped) {
		t.Fatal("the pipeline wrapper must pass the relay on")
	}
	ch, err := wrapped.RelayMessages(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	events := relayedEvents(t, ch)

	// message_start and the ping are not held back.
	if events[0] != pseudonymizedTurn[0] || events[1] != pseudonymizedTurn[3] {
		t.Errorf("message_start and ping must go through first, got:\n%s\n%s", events[0], events[1])
	}
	// The verdict and the end of the message come after the content, as sent.
	n := len(events)
	if events[n-2] != pseudonymizedTurn[10] || events[n-1] != pseudonymizedTurn[11] {
		t.Errorf("message_delta / message_stop altered:\n%s\n%s", events[n-2], events[n-1])
	}

	var text, input strings.Builder
	for _, event := range events {
		parsed := parseRelayedEvent([]byte(event))
		text.WriteString(parsed.Delta.Text)
		input.WriteString(parsed.Delta.PartialJSON)
		if strings.Contains(event, "PERSON_1") {
			t.Errorf("placeholder left in:\n%s", event)
		}
	}
	if text.String() != "Hi Alice <3" {
		t.Errorf("text = %q", text.String())
	}
	if input.String() != `{"command":"ls /home/Alice"}` {
		t.Errorf("tool input = %q", input.String())
	}
	if !strings.Contains(strings.Join(events, ""), `"id":"toolu_01"`) {
		t.Error("the tool_use id the verdict is keyed by must survive")
	}
}

func TestPipelineWrappedClient_RelayMessages_UnchangedContentIsForwardedAsIs(t *testing.T) {
	inner := &relayingClient{events: pseudonymizedTurn}
	wrapped := NewPipelineWrappedClient(inner, newTestEngine("NOT_THERE", "x"), newTestForwardExecution(), pipeline.ExecutionContext{})

	ch, err := wrapped.RelayMessages(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	events := relayedEvents(t, ch)

	// Only the ping moves, ahead of the held content.
	want := append([]string{pseudonymizedTurn[0], pseudonymizedTurn[3]}, pseudonymizedTurn[1:3]...)
	want = append(want, pseudonymizedTurn[4:]...)
	if strings.Join(events, "") != strings.Join(want, "") {
		t.Errorf("events rewritten although nothing changed:\n%s", strings.Join(events, ""))
	}
}

func TestPipelineWrappedClient_RelayMessages_ObserverSeesRelayedText(t *testing.T) {
	obs := &observerExecutor{}
	registry := pipeline.NewRegistry()
	registry.Register("observer", obs)
	exec := &pipeline.ForwardExecution{ExecutedNodes: []pipeline.ExecutedNode{{Node: model.PipelineNode{ID: "observer", Type: "observer"}}}}
	wrapped := NewPipelineWrappedClient(&relayingClient{events: pseudonymizedTurn}, pipeline.NewEngine(registry), exec, pipeline.ExecutionContext{})

	ch, err := wrapped.RelayMessages(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(relayedEvents(t, ch), ""); got != strings.Join(pseudonymizedTurn, "") {
		t.Errorf("a pass-through relay must not touch the events:\n%s", got)
	}
	if content, called := obs.seen(); !called || content != "Hi PERSON_1 <3" {
		t.Errorf("observer saw %q (called=%v)", content, called)
	}
}

func TestPipelineWrappedClient_SupportsMessagesRelay_FollowsInner(t *testing.T) {
	wrapped := NewPipelineWrappedClient(&multiChunkClient{}, newTestEngine("a", "b"), newTestForwardExecution(), pipeline.ExecutionContext{})
	if llm.SupportsMessagesRelay(wrapped) {
		t.Error("a pipeline over a non-relaying client must not claim the relay")
	}
}

// countingRewriter is rewriteExecutor counting its backward passes.
type countingRewriter struct {
	rewriteExecutor
	mu     sync.Mutex
	passes int
}

func (e *countingRewriter) Backward(ctx context.Context, in pipeline.BackwardInput) (*pipeline.BackwardResult, error) {
	e.mu.Lock()
	e.passes++
	e.mu.Unlock()
	return e.rewriteExecutor.Backward(ctx, in)
}

func (e *countingRewriter) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.passes
}

func newCountingRelay(events []string) (*PipelineWrappedClient, *countingRewriter) {
	rewriter := &countingRewriter{rewriteExecutor: rewriteExecutor{from: "PERSON_1", to: "Alice"}}
	registry := pipeline.NewRegistry()
	registry.Register("rewrite", rewriter)
	return NewPipelineWrappedClient(&relayingClient{events: events}, pipeline.NewEngine(registry), newTestForwardExecution(), pipeline.ExecutionContext{}), rewriter
}

func TestPipelineWrappedClient_RelayMessages_RestoresEachTextBlockInPlace(t *testing.T) {
	events := []string{
		sse("message_start", `{"type":"message_start","message":{"id":"msg_1"}}`),
		sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Looking up PERSON_1."}}`),
		sse("content_block_stop", `{"type":"content_block_stop","index":0}`),
		sse("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"server_tool_use","id":"srvtoolu_01","name":"web_search","input":{}}}`),
		sse("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"PERSON_1\"}"}}`),
		sse("content_block_stop", `{"type":"content_block_stop","index":1}`),
		sse("content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`),
		sse("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"PERSON_1 is "}}`),
		sse("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"found."}}`),
		sse("content_block_stop", `{"type":"content_block_stop","index":2}`),
		sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`),
		sse("message_stop", `{"type":"message_stop"}`),
	}
	wrapped, rewriter := newCountingRelay(events)

	ch, err := wrapped.RelayMessages(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := relayedEvents(t, ch)

	texts := map[int]string{}
	var serverInput string
	for _, event := range got {
		parsed := parseRelayedEvent([]byte(event))
		texts[parsed.Index] += parsed.Delta.Text
		if parsed.Index == 1 {
			serverInput += parsed.Delta.PartialJSON
		}
	}
	if texts[0] != "Looking up Alice." || texts[2] != "Alice is found." {
		t.Errorf("text blocks = %q / %q, want each restored in its own block", texts[0], texts[2])
	}
	if serverInput != `{"query":"PERSON_1"}` {
		t.Errorf("server tool input = %q: restoring it would send the real value upstream next turn", serverInput)
	}
	if rewriter.count() != 1 {
		t.Errorf("backward passes = %d, want 1", rewriter.count())
	}
}

func TestPipelineWrappedClient_RelayMessages_BackwardPassRunsOnce(t *testing.T) {
	cases := map[string][]string{
		"error before any content": {
			sse("message_start", `{"type":"message_start","message":{"id":"msg_1"}}`),
			sse("error", `{"type":"error","error":{"type":"overloaded_error"}}`),
		},
		"unknown event amid the content": {
			sse("message_start", `{"type":"message_start","message":{"id":"msg_1"}}`),
			sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
			sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi PERSON_1"}}`),
			sse("future_event", `{"type":"future_event"}`),
			sse("content_block_stop", `{"type":"content_block_stop","index":0}`),
			sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`),
			sse("message_stop", `{"type":"message_stop"}`),
		},
	}
	for name, events := range cases {
		t.Run(name, func(t *testing.T) {
			wrapped, rewriter := newCountingRelay(events)
			ch, err := wrapped.RelayMessages(context.Background(), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			got := relayedEvents(t, ch)
			if len(got) != len(events) {
				t.Errorf("events = %d, want %d", len(got), len(events))
			}
			if rewriter.count() != 1 {
				t.Errorf("backward passes = %d, want 1", rewriter.count())
			}
		})
	}
}

func TestPipelineWrappedClient_RelayMessages_BackwardPassRunsWhenTheClientLeaves(t *testing.T) {
	wrapped, rewriter := newCountingRelay(pseudonymizedTurn)
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := wrapped.RelayMessages(ctx, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	<-ch // message_start, then the client goes away
	cancel()

	deadline := time.After(2 * time.Second)
	for rewriter.count() == 0 {
		select {
		case <-deadline:
			t.Fatal("the backward pass never ran after the client left")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if rewriter.count() != 1 {
		t.Errorf("backward passes = %d, want 1", rewriter.count())
	}
}
