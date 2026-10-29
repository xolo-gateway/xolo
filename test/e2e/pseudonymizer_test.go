//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

const (
	eventDetected = "plugin.pseudonymizer.sensitive-data.detected"
	eventBlocked  = "plugin.pseudonymizer.request.blocked"
)

// TestPseudonymizer_TagStrategy_RoundTrip checks the nominal path: personal
// data is replaced before the provider sees it, restored in the answer, and
// the detection is recorded as an event.
func TestPseudonymizer_TagStrategy_RoundTrip(t *testing.T) {
	snap := snapshotEvents(t)
	before := len(env.provider.Requests())

	res := chat(t, tokenAlice, modelTagStrategy, "Bonjour, je m'appelle Jean Dupont et j'habite à Lyon.")

	if res.Status != 200 {
		t.Fatalf("status = %d, body = %s", res.Status, res.Body)
	}

	requests := env.provider.Requests()
	if len(requests) != before+1 {
		t.Fatalf("provider received %d request(s), want 1", len(requests)-before)
	}
	upstream := requests[len(requests)-1]

	// What reached the provider carries placeholders, never the raw names.
	for _, leak := range []string{"Jean Dupont", "Lyon"} {
		if strings.Contains(upstream.Raw, leak) {
			t.Errorf("provider saw %q in the request: %s", leak, upstream.Raw)
		}
	}
	if !strings.Contains(upstream.Raw, "⟦PERSON_") || !strings.Contains(upstream.Raw, "⟦LOCATION_") {
		t.Errorf("provider request lacks the expected placeholders: %s", upstream.Raw)
	}

	// The provider echoes the placeholders: the client must get the names back.
	if !strings.Contains(res.Content, "Jean Dupont") || !strings.Contains(res.Content, "Lyon") {
		t.Errorf("answer not deanonymized: %q", res.Content)
	}
	if strings.Contains(res.Content, "⟦PERSON_") {
		t.Errorf("placeholder leaked into the answer: %q", res.Content)
	}

	evt := waitForEvent(t, snap, eventDetected)
	if got := evt.Attributes()["types"]; got != "LOC:1,PER:1" {
		t.Errorf("event types = %q, want LOC:1,PER:1", got)
	}
	if got := evt.Attributes()["entities"]; got != "2" {
		t.Errorf("event entities = %q, want 2", got)
	}
}

// TestPseudonymizer_NoPersonalData_Passthrough checks that an innocuous
// message goes through untouched and raises no detection event.
func TestPseudonymizer_NoPersonalData_Passthrough(t *testing.T) {
	snap := snapshotEvents(t)
	const question = "Peux-tu m'expliquer la différence entre une liste et un tuple ?"

	res := chat(t, tokenAlice, modelTagStrategy, question)

	if res.Status != 200 {
		t.Fatalf("status = %d, body = %s", res.Status, res.Body)
	}
	requests := env.provider.Requests()
	if last := requests[len(requests)-1]; !strings.Contains(last.Raw, question) {
		t.Errorf("provider did not receive the message verbatim: %s", last.Raw)
	}
	if res.Content != "Bien reçu : "+question {
		t.Errorf("answer = %q, want the provider echo untouched", res.Content)
	}

	assertNoEvent(t, snap, eventDetected)
}

// TestPseudonymizer_HashWithoutKey_FailsClosed checks that a hash strategy
// with no HMAC key on the node refuses the request instead of forwarding
// personal data in clear, and reports the refusal as an event.
func TestPseudonymizer_HashWithoutKey_FailsClosed(t *testing.T) {
	snap := snapshotEvents(t)
	before := len(env.provider.Requests())

	res := chat(t, tokenAlice, modelHashStrategy, "Bonjour, je m'appelle Jean Dupont.")

	if res.Status != 403 {
		t.Fatalf("status = %d, want 403; body = %s", res.Status, res.Body)
	}
	if !strings.Contains(res.Body, "clé HMAC") {
		t.Errorf("rejection body does not name the missing key: %s", res.Body)
	}
	if got := len(env.provider.Requests()) - before; got != 0 {
		t.Errorf("provider received %d request(s), want none", got)
	}

	evt := waitForEvent(t, snap, eventBlocked)
	if got := evt.Attributes()["reason"]; got != "hash_key_missing" {
		t.Errorf("event reason = %q, want hash_key_missing", got)
	}
	if evt.Severity() != "error" {
		t.Errorf("event severity = %q, want error", evt.Severity())
	}
}

func TestPseudonymizer_PersonalHMACScope(t *testing.T) {
	before := len(env.provider.Requests())
	result := chat(t, tokenAlice, "~/e2e-personal-hash", "Bonjour, je m'appelle Jean Dupont.")
	if result.Status != 200 {
		t.Fatalf("personal HMAC: status=%d body=%s", result.Status, result.Body)
	}
	requests := env.provider.Requests()
	if len(requests) != before+1 {
		t.Fatalf("provider calls=%d, want 1", len(requests)-before)
	}
	if strings.Contains(requests[len(requests)-1].Raw, "Jean Dupont") {
		t.Fatal("personal data reached provider")
	}
	// Another user's identically named graph and node cannot use Alice's key.
	before = len(requests)
	result = chat(t, tokenCarol, "~/e2e-personal-hash", "Bonjour, je m'appelle Jean Dupont.")
	if result.Status != 403 || !strings.Contains(result.Body, "clé HMAC") {
		t.Fatalf("other user's graph: status=%d body=%s", result.Status, result.Body)
	}
	result = chat(t, tokenAlice, modelHashStrategy, "Bonjour, je m'appelle Jean Dupont.")
	if result.Status != 403 || !strings.Contains(result.Body, "clé HMAC") {
		t.Fatalf("org graph: status=%d body=%s", result.Status, result.Body)
	}
	if len(env.provider.Requests()) != before {
		t.Fatal("missing keys must block before provider")
	}
}

// TestPseudonymizer_MessagesStreamRestoresToolCallArguments checks the return
// leg of a tool call on the Anthropic Messages route, streamed, through an
// OpenAI-compatible provider: the model writes a placeholder it read into the
// arguments of a call, and the client must get the original value back, not
// the placeholder (#56).
func TestPseudonymizer_MessagesStreamRestoresToolCallArguments(t *testing.T) {
	before := len(env.provider.Requests())

	status, events := postStreamedMessages(t, tokenAlice, map[string]any{
		"model":      modelTagStrategy,
		"max_tokens": 256,
		"stream":     true,
		"tools": []map[string]any{{
			"name":         "sql",
			"description":  "Runs a read-only SQL query.",
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}},
		}},
		"messages": []map[string]any{
			{"role": "user", "content": "Retrouve le client du dossier en cours dans la base."},
			{"role": "assistant", "content": []map[string]any{
				{"type": "text", "text": "Je lis le dossier."},
				{"type": "tool_use", "id": "toolu_read", "name": "sql", "input": map[string]any{"query": "SELECT dossier FROM dossiers LIMIT 1"}},
			}},
			{"role": "user", "content": []map[string]any{
				{"type": "tool_result", "tool_use_id": "toolu_read", "content": "Dossier 42 : le client est Jean Dupont, domicilié à Lyon."},
			}},
		},
	})
	if status != 200 {
		t.Fatalf("status = %d, events = %s", status, events)
	}

	upstream := env.provider.RequestsSince(before)
	if len(upstream) != 1 {
		t.Fatalf("upstream calls = %d, want 1", len(upstream))
	}
	if strings.Contains(upstream[0].Raw, "Jean Dupont") || !strings.Contains(upstream[0].Raw, "⟦PERSON_") {
		t.Fatalf("the provider did not get a pseudonymized request: %s", upstream[0].Raw)
	}

	if text := streamedText(t, events); strings.Contains(text, "⟦") {
		t.Errorf("text = %q, want the placeholder restored", text)
	}
	input := toolUseInput(t, events)
	if !strings.Contains(input, "Jean Dupont") || strings.Contains(input, "⟦") {
		t.Errorf("tool_use input = %q, want the placeholder restored to Jean Dupont\nevents:\n%s", input, events)
	}
}

// TestPseudonymizer_MessagesRelayDoesNotRestoreAnEarlierPlaceholderToSomeoneElse
// checks a placeholder the model reuses from an earlier turn, kept in a
// thinking block the pseudonymizer forwards untouched to an anthropic
// provider: the entity it stands for is no longer in the request, so the
// request's mapping does not hold it. Nothing about him outlives the earlier
// request: the placeholder cannot be restored to him, but it must not name the
// person this request holds either, nor reach the client live.
func TestPseudonymizer_MessagesRelayDoesNotRestoreAnEarlierPlaceholderToSomeoneElse(t *testing.T) {
	// An earlier turn names Jean Dupont: the placeholder the model saw for him.
	before := len(env.provider.Requests())
	if res := messagesRequest(t, tokenAlice, modelClaude, "", []map[string]any{
		{"role": "user", "content": "Le client est Jean Dupont."},
	}); res.Status != 200 {
		t.Fatalf("earlier turn: status = %d, body = %s", res.Status, res.Body)
	}
	earlier := env.provider.RequestsSince(before)
	if len(earlier) != 1 {
		t.Fatalf("earlier turn: upstream calls = %d, want 1", len(earlier))
	}
	jean := placeholderPattern.FindString(earlier[0].Raw)
	if !strings.HasPrefix(jean, "⟦PERSON_") {
		t.Fatalf("earlier turn: no person placeholder in %s", earlier[0].Raw)
	}

	// This turn names Marie Martin only; the model still reasons on Jean
	// Dupont's placeholder, kept in its thinking from the earlier turn.
	before = len(env.provider.Requests())
	status, events := postStreamedMessages(t, tokenAlice, map[string]any{
		"model":      modelClaude,
		"max_tokens": 256,
		"stream":     true,
		"tools": []map[string]any{{
			"name":         "sql",
			"description":  "Runs a read-only SQL query.",
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}},
		}},
		"messages": []map[string]any{
			{"role": "user", "content": "Retrouve le dossier du client."},
			{"role": "assistant", "content": []map[string]any{
				{"type": "thinking", "thinking": "Le client est " + jean + ", je cherche son dossier.", "signature": "sig-e2e"},
				{"type": "tool_use", "id": "toolu_read", "name": "sql", "input": map[string]any{"query": "SELECT dossier FROM dossiers LIMIT 1"}},
			}},
			{"role": "user", "content": []map[string]any{
				{"type": "tool_result", "tool_use_id": "toolu_read", "content": "Dossier 42, suivi par Marie Martin."},
			}},
		},
	})
	if status != 200 {
		t.Fatalf("status = %d, events = %s", status, events)
	}
	upstream := env.provider.RequestsSince(before)
	if len(upstream) != 1 {
		t.Fatalf("upstream calls = %d, want 1", len(upstream))
	}
	if strings.Contains(upstream[0].Raw, "Marie Martin") || !strings.Contains(upstream[0].Raw, "Le client est "+jean) {
		t.Fatalf("the provider did not get the earlier thinking and a pseudonymized request: %s", upstream[0].Raw)
	}
	if first := placeholderPattern.FindString(upstream[0].Raw); first != jean {
		t.Fatalf("the model quotes %q, want the earlier placeholder %q", first, jean)
	}

	input := toolUseInput(t, events)
	if strings.Contains(input, "Marie Martin") {
		t.Errorf("tool_use input = %q: the earlier placeholder of Jean Dupont names Marie Martin", input)
	}
	if strings.Contains(input, jean) {
		t.Errorf("tool_use input = %q, want the earlier placeholder neutralized", input)
	}
}
