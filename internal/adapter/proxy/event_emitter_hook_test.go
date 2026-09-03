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
