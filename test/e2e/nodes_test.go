//go:build e2e

package e2e

import (
	"strconv"
	"strings"
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

const eventTrace = "pipeline.trace"

// traceEvent waits for the pipeline.trace event carrying the given label.
func traceEvent(t *testing.T, snap eventSnapshot, label string) model.Event {
	t.Helper()
	for range 50 {
		for _, evt := range newEvents(t, snap, eventTrace) {
			if evt.Attributes()["label"] == label {
				return evt
			}
		}
		sleepShort()
	}
	t.Fatalf("no %q trace event with label %q", eventTrace, label)
	return nil
}

func portNumber(t *testing.T, evt model.Event, port string) float64 {
	t.Helper()
	raw, ok := evt.Attributes()["port."+port]
	if !ok {
		t.Fatalf("trace event has no port.%s attribute: %v", port, evt.Attributes())
	}
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		t.Fatalf("port.%s = %q is not a number", port, raw)
	}
	return n
}

func portString(t *testing.T, evt model.Event, port string) string {
	t.Helper()
	raw, ok := evt.Attributes()["port."+port]
	if !ok {
		t.Fatalf("trace event has no port.%s attribute: %v", port, evt.Attributes())
	}
	return raw
}

// TestNodes_LogicPipeline runs value, math, compare, select, model_ref,
// sample, context and trace in one pipeline whose outcome is fixed by
// construction, and reads every intermediate value back from the trace event.
func TestNodes_LogicPipeline(t *testing.T) {
	snap := snapshotEvents(t)
	before := len(env.provider.Requests())

	res := chat(t, tokenAlice, vmLogic, "Bonjour !")
	if res.Status != 200 {
		t.Fatalf("status = %d, body = %s", res.Status, res.Body)
	}

	// max(0.2, 0.9) = 0.9 > 0.5 → the strong model is called.
	upstream := env.provider.RequestsSince(before)
	if len(upstream) != 1 || upstream[0].Model != "e2e-strong" {
		t.Fatalf("upstream calls = %+v, want one call to e2e-strong", upstream)
	}

	evt := traceEvent(t, snap, "logic")
	if got := portNumber(t, evt, "score"); got != 0.9 {
		t.Errorf("port.score = %v, want 0.9", got)
	}
	if got := portString(t, evt, "model_name"); got != modelStrong {
		t.Errorf("port.model_name = %q, want %q", got, modelStrong)
	}
	// A 0 % sample never selects, so the canary select takes its false branch.
	if got := portString(t, evt, "selected"); got != "false" {
		t.Errorf("port.selected = %q, want false", got)
	}
	if got := portString(t, evt, "canary"); got != "stable-model" {
		t.Errorf("port.canary = %q, want stable-model", got)
	}
	if got := portString(t, evt, "user_id"); got != "usr-alice" {
		t.Errorf("port.user_id = %q, want usr-alice", got)
	}
	if got := portString(t, evt, "weekday"); got == "" {
		t.Error("port.weekday is empty")
	}
}

// TestNodes_ModelFallback checks that a failing primary model hands over to
// the next one and that the client never sees the failure.
func TestNodes_ModelFallback(t *testing.T) {
	before := len(env.provider.Requests())

	res := chat(t, tokenAlice, vmFallback, "Quel temps fait-il ?")
	if res.Status != 200 {
		t.Fatalf("status = %d, body = %s", res.Status, res.Body)
	}
	if !strings.HasPrefix(res.Content, "Bien reçu : ") {
		t.Errorf("answer = %q, want the fake provider echo", res.Content)
	}

	upstream := env.provider.RequestsSince(before)
	if len(upstream) < 2 {
		t.Fatalf("upstream calls = %d, want the broken model then the fallback", len(upstream))
	}
	if upstream[0].Model != realModelBroken {
		t.Errorf("first upstream model = %q, want %q", upstream[0].Model, realModelBroken)
	}
	if last := upstream[len(upstream)-1].Model; last != "e2e-fast" {
		t.Errorf("last upstream model = %q, want e2e-fast", last)
	}
}
