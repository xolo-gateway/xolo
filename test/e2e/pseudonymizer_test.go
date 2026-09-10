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
	if !strings.Contains(upstream.Raw, "PERSON_1") || !strings.Contains(upstream.Raw, "LOCATION_1") {
		t.Errorf("provider request lacks the expected placeholders: %s", upstream.Raw)
	}

	// The provider echoes the placeholders: the client must get the names back.
	if !strings.Contains(res.Content, "Jean Dupont") || !strings.Contains(res.Content, "Lyon") {
		t.Errorf("answer not deanonymized: %q", res.Content)
	}
	if strings.Contains(res.Content, "PERSON_1") {
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
