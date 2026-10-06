package events

import (
	"context"
	"strings"
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
)

type recordingEmitter struct {
	events []model.Event
}

func (r *recordingEmitter) Emit(_ context.Context, e model.Event) {
	r.events = append(r.events, e)
}

func provisioningContext() context.Context {
	return model.WithActor(context.Background(), model.Actor{
		URI:       "urn:test:provisioner",
		RequestID: strings.Repeat("a", 32),
	})
}

func emitOne(t *testing.T, ctx context.Context) map[string]string {
	t.Helper()
	emitter := &recordingEmitter{}
	emit(ctx, emitter, model.OrgID("org"), model.SeverityInfo, "test.event", "message", nil)
	if len(emitter.events) != 1 {
		t.Fatalf("expected one event, got %d", len(emitter.events))
	}
	return emitter.events[0].Attributes()
}

func TestEmit_ProvisioningActorOnly(t *testing.T) {
	attrs := emitOne(t, provisioningContext())

	want := map[string]string{
		"actor":      "urn:test:provisioner",
		"actor_uri":  "urn:test:provisioner",
		"actor_id":   "",
		"request_id": strings.Repeat("a", 32),
	}
	for key, value := range want {
		if attrs[key] != value {
			t.Errorf("%s = %q, want %q", key, attrs[key], value)
		}
	}
}

func TestEmit_UserTakesPrecedenceOverProvisioningActor(t *testing.T) {
	user := model.NewUser(model.TenantID("tenant"), "test", "sub-1", "u@example.com", "Alice", true, "user")
	attrs := emitOne(t, httpCtx.SetUser(provisioningContext(), user))

	want := map[string]string{
		"actor":      "Alice",
		"actor_id":   string(user.ID()),
		"actor_uri":  "urn:test:provisioner",
		"request_id": strings.Repeat("a", 32),
	}
	for key, value := range want {
		if attrs[key] != value {
			t.Errorf("%s = %q, want %q", key, attrs[key], value)
		}
	}
}

func TestEmit_WithoutActorOrUser(t *testing.T) {
	emitter := &recordingEmitter{}
	emit(context.Background(), emitter, model.OrgID("org"), model.SeverityInfo, "test.event", "message", nil)
	if len(emitter.events) != 0 {
		t.Fatalf("expected no event, got %d", len(emitter.events))
	}
}

func TestEmit_NilEmitter(t *testing.T) {
	emit(provisioningContext(), nil, model.OrgID("org"), model.SeverityInfo, "test.event", "message", nil)
}
