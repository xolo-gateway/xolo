package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	goanon "github.com/bornholm/go-anon"
	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

func TestInjectPlaceholderInstruction_NewSystemMessage(t *testing.T) {
	messages := []map[string]any{
		{"role": "user", "content": "Bonjour [PERSON_1], votre email est [EMAIL_1]."},
	}
	mapping := map[string]string{
		"[PERSON_1]": "Jean Martin",
		"[EMAIL_1]":  "jean.martin@example.com",
	}
	cfg := defaultConfig()

	got := injectPlaceholderInstruction(messages, mapping, cfg, "fr")

	if len(got) != 2 {
		t.Fatalf("len(messages) = %d, want 2", len(got))
	}
	if role, _ := got[0]["role"].(string); role != "system" {
		t.Fatalf("messages[0].role = %q, want system", role)
	}
	if content, _ := got[0]["content"].(string); content != instructionText("fr") {
		t.Errorf("messages[0].content = %q, want the instruction", content)
	}
	// Original user message must be untouched.
	if got[1]["content"] != "Bonjour [PERSON_1], votre email est [EMAIL_1]." {
		t.Errorf("user message modified: %v", got[1]["content"])
	}
}

func TestInjectPlaceholderInstruction_AppendsToExistingSystemMessage(t *testing.T) {
	messages := []map[string]any{
		{"role": "system", "content": "Tu es un assistant utile."},
		{"role": "user", "content": "Bonjour [PERSON_1] !"},
	}
	mapping := map[string]string{"[PERSON_1]": "Jean Martin"}
	cfg := defaultConfig()

	got := injectPlaceholderInstruction(messages, mapping, cfg, "fr")

	if len(got) != 2 {
		t.Fatalf("len(messages) = %d, want 2", len(got))
	}
	content, _ := got[0]["content"].(string)
	// The client's system prompt must stay a prefix: it is what the upstream
	// prompt cache matches on.
	if !strings.HasPrefix(content, "Tu es un assistant utile.") {
		t.Errorf("client system prompt is no longer a prefix: %q", content)
	}
	if !strings.HasSuffix(content, instructionText("fr")) {
		t.Errorf("instruction not appended: %q", content)
	}
}

// A conversation that finds new entities on each turn must keep sending the
// same instruction, otherwise the upstream prompt cache breaks right before the
// conversation on every turn (#85).
func TestInjectPlaceholderInstruction_StableAcrossMappings(t *testing.T) {
	cfg := defaultConfig()
	mappings := []map[string]string{
		{"[PERSON_1]": "Jean Martin"},
		{"[PERSON_1]": "Jean Martin", "[EMAIL_1]": "jean.martin@example.com"},
		{"[PERSON_1]": "Jean Martin", "[EMAIL_1]": "jean.martin@example.com", "[LOCATION_1]": "Lyon"},
	}

	for _, language := range []string{"fr", "en", "es"} {
		var first string
		for i, mapping := range mappings {
			messages := []map[string]any{{"role": "user", "content": "Bonjour"}}
			got := injectPlaceholderInstruction(messages, mapping, cfg, language)
			content, _ := got[0]["content"].(string)
			if i == 0 {
				first = content
				continue
			}
			if content != first {
				t.Errorf("%s: instruction changed with the mapping:\n%q\n%q", language, first, content)
			}
		}
	}
}

func TestInjectPlaceholderInstruction_NoEntities(t *testing.T) {
	messages := []map[string]any{{"role": "user", "content": "Bonjour !"}}
	cfg := defaultConfig()

	got := injectPlaceholderInstruction(messages, nil, cfg, "fr")

	if len(got) != 1 {
		t.Fatalf("len(messages) = %d, want 1 (unchanged)", len(got))
	}
}

func TestInjectPlaceholderInstruction_RedactStrategySkipped(t *testing.T) {
	messages := []map[string]any{{"role": "user", "content": "Bonjour ████ !"}}
	mapping := map[string]string{"████████████": "William Petit"}
	cfg := defaultConfig()
	cfg.Strategy = "redact"

	got := injectPlaceholderInstruction(messages, mapping, cfg, "fr")

	if len(got) != 1 {
		t.Fatalf("len(messages) = %d, want 1 (unchanged)", len(got))
	}
}

func TestInjectPlaceholderInstruction_Disabled(t *testing.T) {
	messages := []map[string]any{{"role": "user", "content": "Bonjour [PERSON_1] !"}}
	mapping := map[string]string{"[PERSON_1]": "William Petit"}
	cfg := defaultConfig()
	cfg.InjectInstruction = false

	got := injectPlaceholderInstruction(messages, mapping, cfg, "fr")

	if len(got) != 1 {
		t.Fatalf("len(messages) = %d, want 1 (unchanged)", len(got))
	}
}

type captureHost struct {
	pluginsdk.HostClient
	mu    sync.Mutex
	event pluginsdk.Event
}

func (c *captureHost) EmitEvent(_ context.Context, e pluginsdk.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.event = e
	return nil
}

func (c *captureHost) waitForEvent(t *testing.T) pluginsdk.Event {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		got := c.event
		c.mu.Unlock()
		if got.Type != "" {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.event
}

// assertNoEvent fails if an event turns up within the window.
//
// The positive counterpart polls until one arrives; asserting the absence of
// one has to wait out the window instead, because emitEvent publishes from a
// goroutine. Reading the field straight after PreRequest would pass whether the
// event was coming or not, and would race the writer.
func (c *captureHost) assertNoEvent(t *testing.T, window time.Duration) {
	t.Helper()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		got := c.event
		c.mu.Unlock()
		if got.Type != "" {
			t.Fatalf("an event was emitted: %s %v", got.Type, got.Attributes)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHandleVerificationError_Allow(t *testing.T) {
	cfg := defaultConfig()
	cfg.VerificationStrict = true
	cfg.VerificationOnLeak = "allow"

	verr := &goanon.VerificationError{
		Report: &goanon.VerificationReport{
			Leaks: []goanon.Leak{{Kind: goanon.LeakRegexHit, Type: goanon.TypeEMAIL, Start: 0, End: 10}},
		},
	}
	host := &captureHost{}

	out := handleVerificationError(&proto.PreRequestInput{}, verr, cfg, host)

	if out == nil {
		t.Fatalf("expected non-nil output")
	}
	if !out.Allowed {
		t.Errorf("expected Allowed=true (allow), got false")
	}
	got := host.waitForEvent(t)
	if got.Type != "sensitive-data.leak" {
		t.Errorf("expected event type sensitive-data.leak, got %q", got.Type)
	}
	if got.Severity != "error" {
		t.Errorf("expected severity error, got %q", got.Severity)
	}
}

func TestHandleVerificationError_Block(t *testing.T) {
	cfg := defaultConfig()
	cfg.VerificationStrict = true
	cfg.VerificationOnLeak = "block"

	verr := &goanon.VerificationError{
		Report: &goanon.VerificationReport{
			Leaks: []goanon.Leak{{Kind: goanon.LeakKnownEntity, Type: goanon.TypePER, Start: 5, End: 15}},
		},
	}
	host := &captureHost{}

	out := handleVerificationError(&proto.PreRequestInput{}, verr, cfg, host)

	if out == nil {
		t.Fatalf("expected non-nil output")
	}
	if out.Allowed {
		t.Errorf("expected Allowed=false (block), got true")
	}
	got := host.waitForEvent(t)
	if got.Type != "sensitive-data.leak" {
		t.Errorf("expected event to be emitted even when blocking, got type %q", got.Type)
	}
}

func TestHandleVerificationError_NonVerificationError(t *testing.T) {
	cfg := defaultConfig()
	host := &captureHost{}

	out := handleVerificationError(&proto.PreRequestInput{}, context.Canceled, cfg, host)

	if out != nil {
		t.Errorf("expected nil output for non-VerificationError, got %+v", out)
	}
	if host.event.Type != "" {
		t.Errorf("expected no event emitted, got %q", host.event.Type)
	}
}

// Every path that lets the request through without anonymizing stashes no
// state, so PostResponse has nothing to restore. Saying so lets the host stream
// the response instead of buffering it whole waiting for a rewrite that will
// never come.
func TestPassthroughOutput_DeclaresNoResponseRewrite(t *testing.T) {
	out := passthroughOutput()

	if !out.Allowed {
		t.Error("passthrough output must allow the request")
	}
	if !out.NoResponseRewrite {
		t.Error("a passthrough leaves the response untouched and must say so")
	}
	if len(out.NodeState) != 0 {
		t.Errorf("passthrough must not stash state, got %d bytes", len(out.NodeState))
	}
}

// A leak detected with the "allow" policy still passes through untouched.
func TestHandleVerificationError_AllowDeclaresNoResponseRewrite(t *testing.T) {
	cfg := defaultConfig()
	cfg.VerificationOnLeak = "allow"

	verr := &goanon.VerificationError{
		Report: &goanon.VerificationReport{
			Leaks: []goanon.Leak{{Kind: goanon.LeakRegexHit, Type: goanon.TypeEMAIL, Start: 0, End: 10}},
		},
	}

	out := handleVerificationError(&proto.PreRequestInput{}, verr, cfg, nil)

	if out == nil {
		t.Fatal("expected non-nil output")
	}
	if !out.NoResponseRewrite {
		t.Error("the allow path rewrites nothing and must say so")
	}
}

// The placeholder nonce must not change between two requests of the same
// sender, or each turn rewrites the whole history and breaks the upstream
// prompt cache (#85).
func TestNewSession_NonceIsStablePerSender(t *testing.T) {
	alice := &proto.RequestContext{OrgId: "org", UserId: "alice", NodeId: "node"}
	bob := &proto.RequestContext{OrgId: "org", UserId: "bob", NodeId: "node"}

	if a, b := newSession(alice).Nonce(), newSession(alice).Nonce(); a != b {
		t.Errorf("same sender, different nonces: %q, %q", a, b)
	}
	if a, b := newSession(alice).Nonce(), newSession(bob).Nonce(); a == b {
		t.Errorf("different senders share the nonce %q", a)
	}
}
