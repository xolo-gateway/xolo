package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bornholm/go-anon/pkg/ner"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// preRequestFR runs PreRequest on a single French user message with the given
// node config, capturing the events emitted along the way.
func preRequestFR(t *testing.T, cfgJSON, text string) (*proto.PreRequestOutput, *captureHost) {
	t.Helper()
	// The secret store is consulted by the hash strategy: back the capture
	// with the in-memory fake so that lookup answers "no key".
	host := &captureHost{HostClient: newFakeUIHost()}
	p := newPlugin()
	p.SetHostClient(host)

	msgs, err := json.Marshal([]map[string]any{{"role": "user", "content": text}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.PreRequest(context.Background(), &proto.PreRequestInput{
		Ctx:          &proto.RequestContext{OrgId: "org-1", UserId: "user-1", NodeId: "node-1", ConfigJson: cfgJSON},
		MessagesJson: string(msgs),
	})
	if err != nil {
		t.Fatalf("PreRequest: %v", err)
	}
	return out, host
}

func TestPreRequest_ConfigError_EmitsPassthroughEvent(t *testing.T) {
	out, host := preRequestFR(t, `{"verification_on_leak":"nope"}`, "Bonjour Jean Dupont")

	if !out.Allowed || !out.NoResponseRewrite {
		t.Fatalf("expected an allowed passthrough, got allowed=%v norewrite=%v", out.Allowed, out.NoResponseRewrite)
	}
	evt := host.waitForEvent(t)
	if evt.Type != "passthrough" {
		t.Fatalf("event type = %q, want passthrough", evt.Type)
	}
	if evt.Severity != "error" {
		t.Errorf("severity = %q, want error", evt.Severity)
	}
	if evt.Attributes["reason"] != passthroughConfigError {
		t.Errorf("reason = %q, want %q", evt.Attributes["reason"], passthroughConfigError)
	}
	if evt.Attributes["error"] == "" {
		t.Error("error attribute missing")
	}
}

func TestPreRequest_AnonymizerUnavailable_EmitsPassthroughEvent(t *testing.T) {
	// Offline mode on an empty cache: no model can be loaded.
	cfg := `{"offline":true,"cache_dir":"` + t.TempDir() + `","language":"fr"}`
	out, host := preRequestFR(t, cfg, "Bonjour Jean Dupont")

	if !out.Allowed || !out.NoResponseRewrite {
		t.Fatalf("expected an allowed passthrough, got allowed=%v norewrite=%v", out.Allowed, out.NoResponseRewrite)
	}
	evt := host.waitForEvent(t)
	if evt.Type != "passthrough" {
		t.Fatalf("event type = %q, want passthrough", evt.Type)
	}
	if evt.Attributes["reason"] != passthroughAnonymizerInit {
		t.Errorf("reason = %q, want %q", evt.Attributes["reason"], passthroughAnonymizerInit)
	}
}

func TestPreRequest_HashWithoutKey_BlocksRequest(t *testing.T) {
	// The hash strategy without a HMAC key cannot protect anything: the
	// request is refused (fail-closed) and the refusal is reported.
	out, host := preRequestFR(t, `{"strategy":"hash","language":"fr"}`, "Bonjour, je m'appelle Jean Dupont.")

	if out.Allowed {
		t.Fatalf("expected the request to be rejected, got %+v", out)
	}
	if out.RejectionReason == "" {
		t.Error("rejection reason missing")
	}
	evt := host.waitForEvent(t)
	if evt.Type != "request.blocked" {
		t.Fatalf("event type = %q, want request.blocked", evt.Type)
	}
	if evt.Attributes["reason"] != "hash_key_missing" {
		t.Errorf("reason = %q, want hash_key_missing", evt.Attributes["reason"])
	}
}

func TestPreRequest_Detection_EmitsTypedSummary(t *testing.T) {
	if testing.Short() {
		t.Skip("needs the NER model")
	}
	out, host := preRequestFR(t, `{"language":"fr"}`, "Bonjour, je m'appelle Jean Dupont et j'habite à Lyon.")

	if !out.Allowed || out.NoResponseRewrite {
		t.Fatalf("expected a rewritten request, got allowed=%v norewrite=%v", out.Allowed, out.NoResponseRewrite)
	}
	evt := host.waitForEvent(t)
	if evt.Type != "sensitive-data.detected" {
		t.Fatalf("event type = %q, want sensitive-data.detected", evt.Type)
	}
	if evt.Attributes["entities"] != "2" {
		t.Errorf("entities = %q, want 2", evt.Attributes["entities"])
	}
	if evt.Attributes["types"] != "LOC:1,PER:1" {
		t.Errorf("types = %q, want LOC:1,PER:1", evt.Attributes["types"])
	}
}

func TestSummarizeEntities(t *testing.T) {
	counts := map[string]int{}
	countEntities(counts, []ner.Entity{{Type: "PER"}, {Type: "LOC"}, {Type: "PER"}})

	if got := summarizeEntities(counts); got != "LOC:1,PER:2" {
		t.Errorf("summarizeEntities = %q, want LOC:1,PER:2", got)
	}
	if got := summarizeEntities(map[string]int{}); got != "" {
		t.Errorf("summarizeEntities(empty) = %q, want empty", got)
	}
}
