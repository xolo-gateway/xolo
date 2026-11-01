package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bornholm/go-anon/pkg/anonymizer"
	"github.com/bornholm/go-anon/pkg/ner"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// blindRecognizer finds nothing: what it should have found stays in clear,
// for the verification to catch.
type blindRecognizer struct{}

func (blindRecognizer) Recognize(context.Context, string) ([]ner.Entity, error) { return nil, nil }

// preRequestWithBlindRecognizer runs PreRequest with cfg on a message whose
// email the recognizer leaves in clear.
func preRequestWithBlindRecognizer(t *testing.T, cfg Config) (*proto.PreRequestOutput, *captureHost) {
	t.Helper()
	return preRequestBlind(t, cfg, []map[string]any{{"role": "user", "content": "Écrire à jean.dupont@example.com demain."}})
}

// preRequestBlind runs PreRequest with cfg on messages, with a recognizer that
// finds nothing.
func preRequestBlind(t *testing.T, cfg Config, messages []map[string]any) (*proto.PreRequestOutput, *captureHost) {
	t.Helper()
	host := &captureHost{HostClient: newFakeUIHost()}
	p := newPlugin()
	host.attachTo(p)
	if _, err := p.ensureStore(cfg); err != nil {
		t.Fatalf("ensure store: %v", err)
	}
	p.mu.Lock()
	p.anons[cfg.Language] = anonymizer.New(blindRecognizer{}, anonymizer.Config{Strategy: anonymizer.TagReplace, ConsistentMap: true})
	p.mu.Unlock()

	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	msgs, _ := json.Marshal(messages)
	out, err := p.PreRequest(context.Background(), &proto.PreRequestInput{
		Ctx:          &proto.RequestContext{OrgId: "org-1", UserId: "user-1", NodeId: "node-1", ConfigJson: string(cfgJSON)},
		MessagesJson: string(msgs),
	})
	if err != nil {
		t.Fatalf("PreRequest: %v", err)
	}
	return out, host
}

// Reporting a leak lets the request leave, pseudonymized as far as it could
// be, and says so in an event: before, the observation kept its report to
// itself and no event was ever emitted.
func TestPreRequest_ReportedLeakEmitsAnEvent(t *testing.T) {
	cfg := attachmentConfig(t)
	cfg.Verification = true

	out, host := preRequestWithBlindRecognizer(t, cfg)
	if !out.Allowed {
		t.Fatalf("a reported leak must not block the request: %+v", out)
	}
	evt := host.waitForEventOfType(t, "sensitive-data.leak")
	if evt.Attributes["action"] != leakReported || evt.Attributes["leak_count"] == "" || evt.Attributes["leak_count"] == "0" {
		t.Errorf("leak event attributes = %v, want action=reported and a count", evt.Attributes)
	}
	for _, v := range evt.Attributes {
		if strings.Contains(v, "jean.dupont") {
			t.Errorf("the leak event carries the leaked value: %v", evt.Attributes)
		}
	}
}

// Blocking refuses the request and says so in the same event.
func TestPreRequest_BlockedLeakEmitsAnEvent(t *testing.T) {
	cfg := attachmentConfig(t)
	cfg.Verification, cfg.VerificationStrict, cfg.VerificationOnLeak = true, true, "block"

	out, host := preRequestWithBlindRecognizer(t, cfg)
	if out.Allowed {
		t.Fatalf("a blocked leak must refuse the request")
	}
	if evt := host.waitForEventOfType(t, "sensitive-data.leak"); evt.Attributes["action"] != leakBlocked {
		t.Errorf("action = %q, want blocked", evt.Attributes["action"])
	}
}

// One value repeated, here sent again in the history, is one leak.
func TestPreRequest_ReportedLeaksAreCountedOnce(t *testing.T) {
	cfg := attachmentConfig(t)
	cfg.Verification = true

	_, host := preRequestBlind(t, cfg, []map[string]any{
		{"role": "user", "content": "Écrire à jean.dupont@example.com demain."},
		{"role": "assistant", "content": "Je préviens Jean.Dupont@example.com."},
		{"role": "user", "content": "Merci, écris à jean.dupont@example.com."},
	})
	if evt := host.waitForEventOfType(t, "sensitive-data.leak"); evt.Attributes["leak_count"] != "1" {
		t.Errorf("leak_count = %q, want 1 for one repeated value", evt.Attributes["leak_count"])
	}
}

// A leak let through in one content is reported even when the request is then
// refused for another reason, here an attachment that cannot be read.
func TestPreRequest_ReportedLeakSurvivesALaterRefusal(t *testing.T) {
	cfg := attachmentConfig(t)
	cfg.Verification = true
	cfg.UnsupportedAttachments = "block"

	out, host := preRequestBlind(t, cfg, []map[string]any{
		{"role": "user", "content": "Écrire à jean.dupont@example.com demain."},
		{"role": "user", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/scan.png"}}}},
	})
	if out.Allowed {
		t.Fatalf("the unreadable attachment must refuse the request")
	}
	// Nothing reached the model: the event must not say the request left.
	if evt := host.waitForEventOfType(t, "sensitive-data.leak"); evt.Attributes["action"] != leakRefused {
		t.Errorf("action = %q, want refused", evt.Attributes["action"])
	}
}

// A placeholder already in the content is located in the source, not in the
// output: two distinct ones are two leaks, one repeated is one.
func TestObservedLeakKey_ResidualPlaceholderUsesTheSource(t *testing.T) {
	source := "Voir ⟦PERSON_1_abcdef⟧ et ⟦PERSON_2_abcdef⟧, puis ⟦PERSON_1_abcdef⟧."
	output := "x"
	key := func(s string) string {
		i := strings.Index(source, s)
		return observedLeakKey(source, output, anonymizer.Leak{Kind: anonymizer.LeakResidualPlaceholderSource, Start: i, End: i + len(s)})
	}
	last := strings.LastIndex(source, "⟦PERSON_1_abcdef⟧")
	repeated := observedLeakKey(source, output, anonymizer.Leak{Kind: anonymizer.LeakResidualPlaceholderSource, Start: last, End: last + len("⟦PERSON_1_abcdef⟧")})
	if key("⟦PERSON_1_abcdef⟧") == key("⟦PERSON_2_abcdef⟧") {
		t.Errorf("two distinct placeholders give one key")
	}
	if key("⟦PERSON_1_abcdef⟧") != repeated {
		t.Errorf("one repeated placeholder gives two keys")
	}
}
