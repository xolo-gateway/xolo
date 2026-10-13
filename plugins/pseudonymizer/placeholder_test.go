package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// inventedPlaceholder carries a session nonce, but no mapping holds it: the
// model wrote it in place of a common noun (#113).
const inventedPlaceholder = "⟦LOCATION_15_95881f⟧"

func TestNeutralizePlaceholders(t *testing.T) {
	for name, tc := range map[string]struct {
		in, want string
		count    int
	}{
		"indexed":         {in: "le " + inventedPlaceholder + " du site", want: "le [[LOCATION_15_95881f]] du site", count: 1},
		"hashed":          {in: "⟦PERSON_3fa9c1d2⟧", want: "[[PERSON_3fa9c1d2]]", count: 1},
		"redacted":        {in: "clé ⟦API_KEY_REDACTED⟧", want: "clé [[API_KEY_REDACTED]]", count: 1},
		"several":         {in: "⟦A_1_x⟧ et ⟦B_2_y⟧", want: "[[A_1_x]] et [[B_2_y]]", count: 2},
		"lone delimiters": {in: "⟦ et ⟧ ne sont pas un jeton", want: "⟦ et ⟧ ne sont pas un jeton"},
		"not a token":     {in: "⟦deux mots⟧", want: "⟦deux mots⟧"},
		"plain text":      {in: "rien à faire", want: "rien à faire"},
	} {
		t.Run(name, func(t *testing.T) {
			got, count := neutralizePlaceholders(tc.in, nil)
			if got != tc.want || count != tc.count {
				t.Errorf("neutralizePlaceholders(%q) = %q, %d; want %q, %d", tc.in, got, count, tc.want, tc.count)
			}
		})
	}
}

func TestNeutralizePlaceholders_KeepsWhatKeepAccepts(t *testing.T) {
	got, count := neutralizePlaceholders("⟦API_KEY_REDACTED⟧ et "+inventedPlaceholder, isRedactedMarker)
	if want := "⟦API_KEY_REDACTED⟧ et [[LOCATION_15_95881f]]"; got != want || count != 1 {
		t.Errorf("got %q, %d; want %q, 1", got, count, want)
	}
}

// postResponseWithHost runs PostResponse over a known mapping, capturing the
// events emitted along the way.
func postResponseWithHost(t *testing.T, mapping map[string]string, content, toolCallsJSON string) (*proto.PostResponseOutput, *captureHost) {
	t.Helper()

	state, err := json.Marshal(pluginState{Mapping: mapping})
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	host := &captureHost{}
	p := newPlugin()
	p.SetHostClient(host)

	out, err := p.PostResponse(context.Background(), &proto.PostResponseInput{
		Ctx:                   &proto.RequestContext{OrgId: "org-1", UserId: "user-1"},
		ResponseContent:       content,
		ResponseToolCallsJson: toolCallsJSON,
		NodeState:             state,
	})
	if err != nil {
		t.Fatalf("PostResponse: %v", err)
	}
	return out, host
}

// An invented placeholder has no original to restore. Left as is, it reached
// the client, was stored in its history, and switched pseudonymization off for
// the rest of the conversation.
func TestPostResponse_NeutralizesInventedPlaceholders(t *testing.T) {
	mapping := map[string]string{"⟦LOCATION_1_95881f⟧": "Lyon"}

	out, host := postResponseWithHost(t, mapping,
		"À ⟦LOCATION_1_95881f⟧, vérifiez le SIREN, le "+inventedPlaceholder+" et le ⟦LOCATION_16_95881f⟧.",
		"",
	)

	want := "À Lyon, vérifiez le SIREN, le [[LOCATION_15_95881f]] et le [[LOCATION_16_95881f]]."
	if out.ModifiedResponseContent != want {
		t.Errorf("content = %q, want %q", out.ModifiedResponseContent, want)
	}

	evt := host.waitForEventOfType(t, eventPlaceholdersNeutralized)
	if evt.Severity != "warning" {
		t.Errorf("severity = %q, want warning", evt.Severity)
	}
	if evt.Attributes["direction"] != "response" || evt.Attributes["placeholders"] != "2" {
		t.Errorf("attributes = %v, want direction=response placeholders=2", evt.Attributes)
	}
	if evt.OrgID != "org-1" || evt.UserID != "user-1" {
		t.Errorf("event not attributed: org=%q user=%q", evt.OrgID, evt.UserID)
	}
}

// #56: a placeholder in a tool call cannot be told apart from an invented one
// once the mapping is gone. Either way, the client must not run the call
// against it, nor keep it in its history.
func TestPostResponse_NeutralizesInventedPlaceholdersInToolCalls(t *testing.T) {
	mapping := map[string]string{"⟦PERSON_1_95881f⟧": "Jean Dupont"}

	out, host := postResponseWithHost(t, mapping, "ok",
		`[{"id":"call_1","name":"Read","arguments":"{\"path\":\"/home/⟦PERSON_1_95881f⟧/⟦ORGANIZATION_16_95881f⟧.md\"}"}]`,
	)

	if got := toolCallArgumentValue(t, out.ModifiedToolCallsJson, "path"); got != "/home/Jean Dupont/[[ORGANIZATION_16_95881f]].md" {
		t.Errorf("path = %q", got)
	}
	evt := host.waitForEventOfType(t, eventPlaceholdersNeutralized)
	if evt.Attributes["placeholders"] != "1" {
		t.Errorf("placeholders = %q, want 1", evt.Attributes["placeholders"])
	}
}

// The marker of a redacted secret is restored by nothing, by design: the model
// repeating it invented nothing.
func TestPostResponse_LeavesRedactedMarkersAlone(t *testing.T) {
	out, host := postResponseWithHost(t, map[string]string{"⟦PERSON_1_95881f⟧": "Jean Dupont"},
		"⟦PERSON_1_95881f⟧, votre clé ⟦API_KEY_REDACTED⟧ est révoquée.",
		"",
	)

	if want := "Jean Dupont, votre clé ⟦API_KEY_REDACTED⟧ est révoquée."; out.ModifiedResponseContent != want {
		t.Errorf("content = %q, want %q", out.ModifiedResponseContent, want)
	}
	host.assertNoEventOfType(t, eventPlaceholdersNeutralized, 100*time.Millisecond)
}

// preRequestMessagesWithHost runs PreRequest on a conversation, capturing the
// events emitted along the way.
func preRequestMessagesWithHost(t *testing.T, cfg Config, messages []map[string]any) (*proto.PreRequestOutput, *captureHost) {
	t.Helper()

	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	messagesJSON, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("marshal messages: %v", err)
	}

	host := &captureHost{HostClient: newFakeUIHost()}
	p := attachmentPlugin(t, cfg)
	p.SetHostClient(host)

	out, err := p.PreRequest(context.Background(), &proto.PreRequestInput{
		Ctx:          &proto.RequestContext{OrgId: "org-1", UserId: "user-1", NodeId: "node-1", ConfigJson: string(cfgJSON)},
		MessagesJson: string(messagesJSON),
	})
	if err != nil {
		t.Fatalf("PreRequest: %v", err)
	}
	return out, host
}

// The next turn sends the history back with the invented placeholder in it.
// go-anon refuses a text that already holds one, and a refused content used to
// leave in clear: every shape that goes through the anonymizer must neutralize
// it first and still pseudonymize the rest.
func TestPreRequest_PlaceholderInHistoryIsNeutralizedNotForwardedInClear(t *testing.T) {
	historyText := "Vérifiez le " + inventedPlaceholder + " de " + agentBlockSecret
	args, err := json.Marshal(map[string]any{"query": historyText})
	if err != nil {
		t.Fatal(err)
	}

	for name, messages := range map[string][]map[string]any{
		"string content": {
			{"role": "assistant", "content": historyText},
		},
		"text part": {
			{"role": "user", "content": []any{map[string]any{"type": "text", "text": historyText}}},
		},
		"tool_result": {
			{"role": "user", "content": []any{map[string]any{
				"type": "tool_result", "tool_use_id": "toolu_1", "content": historyText,
			}}},
		},
		"tool_calls": {
			{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{
				"id": "call_1", "type": "function",
				"function": map[string]any{"name": "search", "arguments": string(args)},
			}}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			out, host := preRequestMessagesWithHost(t, attachmentConfig(t), messages)

			if !out.Allowed {
				t.Fatalf("request refused: %s", out.RejectionReason)
			}
			if strings.Contains(out.ModifiedMessagesJson, agentBlockSecret) {
				t.Errorf("content forwarded in clear: %s", out.ModifiedMessagesJson)
			}
			if strings.Contains(out.ModifiedMessagesJson, "LOCATION_15_95881f⟧") {
				t.Errorf("placeholder forwarded as is: %s", out.ModifiedMessagesJson)
			}
			if !strings.Contains(out.ModifiedMessagesJson, "[[LOCATION_15_95881f]]") {
				t.Errorf("neutralized placeholder missing: %s", out.ModifiedMessagesJson)
			}

			evt := host.waitForEventOfType(t, eventPlaceholdersNeutralized)
			if evt.Severity != "info" {
				t.Errorf("severity = %q, want info", evt.Severity)
			}
			if evt.Attributes["direction"] != "request" || evt.Attributes["placeholders"] != "1" {
				t.Errorf("attributes = %v, want direction=request placeholders=1", evt.Attributes)
			}
			host.assertNoEventOfType(t, "passthrough", 100*time.Millisecond)
		})
	}
}

// The round trip of #113: what PostResponse hands the client is what the
// client sends back on the next turn, and it must be pseudonymized again.
func TestInventedPlaceholder_NextTurnStaysPseudonymized(t *testing.T) {
	response, _ := postResponseWithHost(t, map[string]string{"⟦PERSON_1_95881f⟧": agentBlockSecret},
		"Écrivez à ⟦PERSON_1_95881f⟧ au sujet du "+inventedPlaceholder+".",
		"",
	)

	out, host := preRequestMessagesWithHost(t, attachmentConfig(t), []map[string]any{
		{"role": "assistant", "content": response.ModifiedResponseContent},
		{"role": "user", "content": "Merci, et ensuite ?"},
	})

	if strings.Contains(out.ModifiedMessagesJson, agentBlockSecret) {
		t.Errorf("history forwarded in clear: %s", out.ModifiedMessagesJson)
	}
	host.assertNoEventOfType(t, "passthrough", 100*time.Millisecond)
	// Already inert: nothing left to neutralize on this turn.
	host.assertNoEventOfType(t, eventPlaceholdersNeutralized, 0)
}
