package proxy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/bornholm/genai/llm"
	genaiProxy "github.com/bornholm/genai/proxy"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

type recordingEmitter struct {
	events []model.Event
}

func (r *recordingEmitter) Emit(_ context.Context, event model.Event) {
	r.events = append(r.events, event)
}

func newFailedRequest() *genaiProxy.ProxyRequest {
	return &genaiProxy.ProxyRequest{
		Model:    "cadoles/test-model",
		UserID:   "user-1",
		Metadata: map[string]any{},
	}
}

func TestEventEmitterHookOnErrorRecordsFailure(t *testing.T) {
	emitter := &recordingEmitter{}
	hook := NewXoloEventEmitterHook(emitter)

	upstreamErr := llm.NewHTTPError(http.StatusGatewayTimeout, strings.Repeat("x", 1000))
	if _, err := hook.OnError(context.Background(), newFailedRequest(), upstreamErr); err != nil {
		t.Fatal(err)
	}

	if len(emitter.events) != 1 {
		t.Fatalf("expected one event, got %d", len(emitter.events))
	}

	event := emitter.events[0]
	if event.Type() != model.EventTypeProxyRequestFailed {
		t.Fatalf("expected type %q, got %q", model.EventTypeProxyRequestFailed, event.Type())
	}
	if event.Severity() != model.SeverityWarning {
		t.Fatalf("expected warning severity, got %q", event.Severity())
	}
	if got := event.Attributes()["status"]; got != "504" {
		t.Fatalf("expected status attribute 504, got %q", got)
	}
	if got := event.Attributes()["model"]; got != "cadoles/test-model" {
		t.Fatalf("expected model attribute, got %q", got)
	}
	if got := event.Attributes()["error"]; len([]rune(got)) != maxErrorAttributeLength+1 {
		t.Fatalf("expected the error attribute to be truncated, got %d runes", len([]rune(got)))
	}
}

func TestEventEmitterHookOnErrorDefaultsTo500(t *testing.T) {
	emitter := &recordingEmitter{}
	hook := NewXoloEventEmitterHook(emitter)

	if _, err := hook.OnError(context.Background(), newFailedRequest(), errors.New("boom")); err != nil {
		t.Fatal(err)
	}

	if len(emitter.events) != 1 {
		t.Fatalf("expected one event, got %d", len(emitter.events))
	}
	if got := emitter.events[0].Attributes()["status"]; got != "500" {
		t.Fatalf("expected status attribute 500, got %q", got)
	}
}

func TestEventEmitterHookOnErrorIgnoresClientCancellation(t *testing.T) {
	emitter := &recordingEmitter{}
	hook := NewXoloEventEmitterHook(emitter)

	if _, err := hook.OnError(context.Background(), newFailedRequest(), context.Canceled); err != nil {
		t.Fatal(err)
	}

	if len(emitter.events) != 0 {
		t.Fatalf("expected no event for a client cancellation, got %d", len(emitter.events))
	}
}

// eventsOfType picks the events of one type out of what the emitter recorded.
func eventsOfType(events []model.Event, eventType string) []model.Event {
	var found []model.Event
	for _, event := range events {
		if event.Type() == eventType {
			found = append(found, event)
		}
	}
	return found
}

// TestEventEmitterHookEmitsRequestAlongsideInterruption pins the pairing: a
// stream that ended early still produced a request, so anything counting proxy
// calls has to keep seeing it. The interruption event comes on top, never
// instead.
func TestEventEmitterHookEmitsRequestAlongsideInterruption(t *testing.T) {
	emitter := &recordingEmitter{}
	hook := NewXoloEventEmitterHook(emitter)

	req := newFailedRequest()
	res := &genaiProxy.ProxyResponse{
		TokensUsed: &genaiProxy.TokenUsage{PromptTokens: 10, CompletionTokens: 5},
		Interruption: &genaiProxy.StreamInterruption{
			Cause:         genaiProxy.StreamInterruptionUpstream,
			Err:           errors.New("upstream exploded"),
			ChunksEmitted: 12,
		},
	}

	if _, err := hook.PostResponse(context.Background(), req, res); err != nil {
		t.Fatal(err)
	}

	if got := len(eventsOfType(emitter.events, model.EventTypeProxyRequest)); got != 1 {
		t.Errorf("proxy.request events = %d, want 1: an interrupted call is still a call", got)
	}

	interrupted := eventsOfType(emitter.events, model.EventTypeProxyStreamInterrupted)
	if len(interrupted) != 1 {
		t.Fatalf("proxy.stream.interrupted events = %d, want 1", len(interrupted))
	}

	attrs := interrupted[0].Attributes()
	if got := attrs["cause"]; got != string(genaiProxy.StreamInterruptionUpstream) {
		t.Errorf("cause attribute = %q, want %q", got, genaiProxy.StreamInterruptionUpstream)
	}
	if got := attrs["chunks_emitted"]; got != "12" {
		t.Errorf("chunks_emitted attribute = %q, want \"12\"", got)
	}
	if got := attrs["error"]; got != "upstream exploded" {
		t.Errorf("error attribute = %q, want the upstream error", got)
	}
	if got := attrs["model"]; got != "cadoles/test-model" {
		t.Errorf("model attribute = %q, want it carried over from the request", got)
	}

	// The interruption attributes must not leak onto the request event, which
	// other consumers parse.
	plain := eventsOfType(emitter.events, model.EventTypeProxyRequest)[0]
	if _, exists := plain.Attributes()["cause"]; exists {
		t.Error("proxy.request carries a cause attribute it never had before")
	}
}

// TestEventEmitterHookRatesInterruptionSeverity walks the causes. An unknown
// one has to be rated a fault, the way the usage status treats it, or a record
// flagged as a fault would show up as routine in the event stream.
func TestEventEmitterHookRatesInterruptionSeverity(t *testing.T) {
	cases := []struct {
		cause genaiProxy.StreamInterruptionCause
		want  model.EventSeverity
	}{
		{genaiProxy.StreamInterruptionUpstream, model.SeverityWarning},
		{genaiProxy.StreamInterruptionWriteFailed, model.SeverityWarning},
		{genaiProxy.StreamInterruptionClientGone, model.SeverityInfo},
		{genaiProxy.StreamInterruptionTruncated, model.SeverityInfo},
		{genaiProxy.StreamInterruptionCause("something_new"), model.SeverityWarning},
	}

	for _, tc := range cases {
		t.Run(string(tc.cause), func(t *testing.T) {
			emitter := &recordingEmitter{}
			hook := NewXoloEventEmitterHook(emitter)

			res := &genaiProxy.ProxyResponse{
				TokensUsed:   &genaiProxy.TokenUsage{},
				Interruption: &genaiProxy.StreamInterruption{Cause: tc.cause},
			}
			if _, err := hook.PostResponse(context.Background(), newFailedRequest(), res); err != nil {
				t.Fatal(err)
			}

			interrupted := eventsOfType(emitter.events, model.EventTypeProxyStreamInterrupted)
			if len(interrupted) != 1 {
				t.Fatalf("proxy.stream.interrupted events = %d, want 1", len(interrupted))
			}
			if got := interrupted[0].Severity(); got != tc.want {
				t.Errorf("severity = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestEventEmitterHookCompletedStreamEmitsRequestOnly guards the normal path.
func TestEventEmitterHookCompletedStreamEmitsRequestOnly(t *testing.T) {
	emitter := &recordingEmitter{}
	hook := NewXoloEventEmitterHook(emitter)

	res := &genaiProxy.ProxyResponse{TokensUsed: &genaiProxy.TokenUsage{PromptTokens: 10}}
	if _, err := hook.PostResponse(context.Background(), newFailedRequest(), res); err != nil {
		t.Fatal(err)
	}

	if got := len(eventsOfType(emitter.events, model.EventTypeProxyRequest)); got != 1 {
		t.Errorf("proxy.request events = %d, want 1", got)
	}
	if got := len(eventsOfType(emitter.events, model.EventTypeProxyStreamInterrupted)); got != 0 {
		t.Errorf("proxy.stream.interrupted events = %d, want none", got)
	}
}
