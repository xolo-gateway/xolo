package main

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	goanon "github.com/bornholm/go-anon"
	"github.com/bornholm/go-anon/pkg/anonymizer"
	"github.com/bornholm/go-anon/pkg/ner"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// testNonce stands for the nonce of the node under test in the PostResponse
// tests, which hand the plugin its state directly.
const testNonce = "95881f"

// nodeNonce is the nonce PreRequest derives for org-1, user-1 and nodeID.
func nodeNonce(nodeID string) string {
	return newSession(context.Background(), &proto.RequestContext{OrgId: "org-1", UserId: "user-1", NodeId: nodeID}).Nonce()
}

func TestNeutralizePlaceholders(t *testing.T) {
	own := ownedBy(testNonce)
	for name, tc := range map[string]struct {
		in, want string
		count    int
	}{
		"own":          {in: "le ⟦LOCATION_15_95881f⟧ du site", want: "le [[LOCATION_15_95881f]] du site", count: 1},
		"several":      {in: "⟦A_1_95881f⟧ et ⟦B_2_95881f⟧", want: "[[A_1_95881f]] et [[B_2_95881f]]", count: 2},
		"another node": {in: "⟦PERSON_1_a57a4e⟧", want: "⟦PERSON_1_a57a4e⟧"},
		"hashed":       {in: "⟦PERSON_3fa9c1d2e4b5⟧", want: "⟦PERSON_3fa9c1d2e4b5⟧"},
		// Without a key, go-anon truncates a hash to six hexadecimal digits.
		"hashed like the nonce": {in: "⟦PERSON_95881f⟧", want: "⟦PERSON_95881f⟧"},
		"redacted":              {in: "⟦API_KEY_REDACTED⟧", want: "⟦API_KEY_REDACTED⟧"},
		"lone delimiters":       {in: "⟦ et ⟧ ne sont pas un jeton", want: "⟦ et ⟧ ne sont pas un jeton"},
		"not a token":           {in: "⟦deux mots_95881f⟧", want: "⟦deux mots_95881f⟧"},
	} {
		t.Run(name, func(t *testing.T) {
			got, count := neutralizePlaceholders(tc.in, own)
			if got != tc.want || count != tc.count {
				t.Errorf("neutralizePlaceholders(%q) = %q, %d; want %q, %d", tc.in, got, count, tc.want, tc.count)
			}
		})
	}
}

func TestOwnedBy_EmptyNonceOwnsNothing(t *testing.T) {
	if ownedBy("")("⟦PERSON_1_⟧") {
		t.Error("an empty nonce must own nothing")
	}
}

// go-anon refuses a text holding any placeholder: all of them are masked, but
// only the node's own stay inert once the text comes back.
func TestMaskPlaceholders(t *testing.T) {
	masked := maskPlaceholders("⟦LOCATION_15_95881f⟧, ⟦PERSON_1_a57a4e⟧, ⟦API_KEY_REDACTED⟧ et ⟦PERSON_1_a57a4e⟧", ownedBy(testNonce))

	if strings.Contains(masked.Text, "⟦") {
		t.Errorf("a placeholder reaches the anonymizer: %q", masked.Text)
	}
	if masked.Neutralized != 1 {
		t.Errorf("neutralized = %d, want 1", masked.Neutralized)
	}
	want := "[[LOCATION_15_95881f]], ⟦PERSON_1_a57a4e⟧, ⟦API_KEY_REDACTED⟧ et ⟦PERSON_1_a57a4e⟧"
	if got, missing := masked.Unmask(masked.Text); got != want || missing != 0 {
		t.Errorf("unmasked = %q, %d missing; want %q, 0 missing", got, missing, want)
	}
}

// An anonymizer that rewrites the inert form of another node's placeholder
// loses it for that node: Unmask says so rather than failing silently.
func TestMaskPlaceholders_UnmaskReportsWhatTheAnonymizerRewrote(t *testing.T) {
	masked := maskPlaceholders("à ⟦PERSON_1_a57a4e⟧", ownedBy(testNonce))

	got, missing := masked.Unmask("à ⟦MISC_1_95881f⟧")
	if got != "à ⟦MISC_1_95881f⟧" || missing != 1 {
		t.Errorf("unmasked = %q, %d missing; want the text untouched and 1 missing", got, missing)
	}
}

// postResponseWithHost runs PostResponse over a known mapping, as the node of
// nonce testNonce, capturing the events emitted along the way.
func postResponseWithHost(t *testing.T, mapping map[string]string, content, toolCallsJSON string) (*proto.PostResponseOutput, *captureHost) {
	t.Helper()

	state, err := json.Marshal(pluginState{Mapping: mapping, Nonce: testNonce})
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	return postResponseWithState(t, state, content, toolCallsJSON)
}

func postResponseWithState(t *testing.T, state []byte, content, toolCallsJSON string) (*proto.PostResponseOutput, *captureHost) {
	t.Helper()

	host := &captureHost{}
	p := newPlugin()
	host.attachTo(p)

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
// the rest of the conversation (#113).
func TestPostResponse_NeutralizesInventedPlaceholders(t *testing.T) {
	mapping := map[string]string{"⟦LOCATION_1_95881f⟧": "Lyon"}

	out, host := postResponseWithHost(t, mapping,
		"À ⟦LOCATION_1_95881f⟧, vérifiez le SIREN, le ⟦LOCATION_15_95881f⟧ et le ⟦LOCATION_16_95881f⟧.",
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

// A response can carry inventions while the request anonymized nothing the
// model echoes back: the mapping is empty, the node's nonce is still known.
// The text and the tool calls add up in one event.
func TestPostResponse_NeutralizesInventionsWithAnEmptyMapping(t *testing.T) {
	out, host := postResponseWithHost(t, nil,
		"Voir le ⟦LOCATION_3_95881f⟧.",
		`[{"id":"call_1","name":"Read","arguments":"{\"path\":\"/srv/⟦ORGANIZATION_2_95881f⟧/⟦MISC_1_95881f⟧\"}"}]`,
	)

	if want := "Voir le [[LOCATION_3_95881f]]."; out.ModifiedResponseContent != want {
		t.Errorf("content = %q, want %q", out.ModifiedResponseContent, want)
	}
	if got := toolCallArgumentValue(t, out.ModifiedToolCallsJson, "path"); got != "/srv/[[ORGANIZATION_2_95881f]]/[[MISC_1_95881f]]" {
		t.Errorf("path = %q", got)
	}
	evt := host.waitForEventOfType(t, eventPlaceholdersNeutralized)
	if evt.Attributes["placeholders"] != "3" {
		t.Errorf("placeholders = %q, want 3", evt.Attributes["placeholders"])
	}
	if events := host.eventsOfType(eventPlaceholdersNeutralized); len(events) != 1 {
		t.Errorf("events = %d, want 1", len(events))
	}
}

// The placeholders of another pseudonymizer node of the pipeline are restored
// by that node, after this one; the marker of a redacted secret by nothing.
func TestPostResponse_LeavesPlaceholdersItDoesNotOwn(t *testing.T) {
	out, host := postResponseWithHost(t, map[string]string{"⟦PERSON_1_95881f⟧": "Jean Dupont"},
		"⟦PERSON_1_95881f⟧ écrit à ⟦PERSON_1_a57a4e⟧, clé ⟦API_KEY_REDACTED⟧.",
		"",
	)

	if want := "Jean Dupont écrit à ⟦PERSON_1_a57a4e⟧, clé ⟦API_KEY_REDACTED⟧."; out.ModifiedResponseContent != want {
		t.Errorf("content = %q, want %q", out.ModifiedResponseContent, want)
	}
	host.assertNoEventOfType(t, eventPlaceholdersNeutralized)
}

// preRequestOnNode runs PreRequest on a conversation as the node nodeID,
// capturing the events emitted along the way.
func preRequestOnNode(t *testing.T, nodeID string, messages []map[string]any) (*proto.PreRequestOutput, *captureHost) {
	t.Helper()

	cfg := attachmentConfig(t)
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
	host.attachTo(p)

	out, err := p.PreRequest(context.Background(), &proto.PreRequestInput{
		Ctx:          &proto.RequestContext{OrgId: "org-1", UserId: "user-1", NodeId: nodeID, ConfigJson: string(cfgJSON)},
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
	invented := "⟦LOCATION_15_" + nodeNonce("node-1") + "⟧"
	historyText := "Vérifiez le " + invented + " de " + agentBlockSecret
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
			out, host := preRequestOnNode(t, "node-1", messages)

			if !out.Allowed {
				t.Fatalf("request refused: %s", out.RejectionReason)
			}
			if strings.Contains(out.ModifiedMessagesJson, agentBlockSecret) {
				t.Errorf("content forwarded in clear: %s", out.ModifiedMessagesJson)
			}
			if strings.Contains(out.ModifiedMessagesJson, invented) {
				t.Errorf("placeholder forwarded as is: %s", out.ModifiedMessagesJson)
			}
			if !strings.Contains(out.ModifiedMessagesJson, inert(invented)) {
				t.Errorf("neutralized placeholder missing: %s", out.ModifiedMessagesJson)
			}

			evt := host.waitForEventOfType(t, eventPlaceholdersNeutralized)
			if evt.Severity != "info" {
				t.Errorf("severity = %q, want info", evt.Severity)
			}
			if evt.Attributes["direction"] != "request" || evt.Attributes["placeholders"] != "1" {
				t.Errorf("attributes = %v, want direction=request placeholders=1", evt.Attributes)
			}
			host.assertNoEventOfType(t, "passthrough")
		})
	}
}

// A placeholder this node did not write — another node's, a redacted secret's
// — no longer makes go-anon refuse the content: the rest is pseudonymized and
// the placeholder kept as is, for whoever restores it.
func TestPreRequest_ForeignPlaceholderIsKeptAndTheRestPseudonymized(t *testing.T) {
	text := "Écrire à ⟦PERSON_1_a57a4e⟧ (clé ⟦API_KEY_REDACTED⟧) et à " + agentBlockSecret
	out, host := preRequestOnNode(t, "node-1", []map[string]any{{"role": "user", "content": text}})

	if strings.Contains(out.ModifiedMessagesJson, agentBlockSecret) {
		t.Errorf("content forwarded in clear: %s", out.ModifiedMessagesJson)
	}
	for _, placeholder := range []string{"⟦PERSON_1_a57a4e⟧", "⟦API_KEY_REDACTED⟧"} {
		if !strings.Contains(out.ModifiedMessagesJson, placeholder) {
			t.Errorf("%s not kept: %s", placeholder, out.ModifiedMessagesJson)
		}
	}
	host.assertNoEventOfType(t, "passthrough")
	host.assertNoEventOfType(t, eventPlaceholdersNeutralized)
}

// The round trip of #113: what PostResponse hands the client is what the
// client sends back on the next turn, and it must be pseudonymized again.
func TestInventedPlaceholder_NextTurnStaysPseudonymized(t *testing.T) {
	nonce := nodeNonce("node-1")
	state, err := json.Marshal(pluginState{Mapping: map[string]string{"⟦PERSON_1_" + nonce + "⟧": agentBlockSecret}, Nonce: nonce})
	if err != nil {
		t.Fatal(err)
	}
	response, _ := postResponseWithState(t, state, "Écrivez à ⟦PERSON_1_"+nonce+"⟧ au sujet du ⟦LOCATION_15_"+nonce+"⟧.", "")

	out, host := preRequestOnNode(t, "node-1", []map[string]any{
		{"role": "assistant", "content": response.ModifiedResponseContent},
		{"role": "user", "content": "Merci, et ensuite ?"},
	})

	if strings.Contains(out.ModifiedMessagesJson, agentBlockSecret) {
		t.Errorf("history forwarded in clear: %s", out.ModifiedMessagesJson)
	}
	host.assertNoEventOfType(t, "passthrough")
	// Already inert: nothing left to neutralize on this turn.
	host.assertNoEventOfType(t, eventPlaceholdersNeutralized)
}

// A middleware wrapping a virtual model can put two pseudonymizer nodes on the
// same request. The inner node sees the outer node's placeholders on the way
// in and restores first on the way out: it must leave them to the outer node.
func TestChainedNodes_EachRestoresItsOwnPlaceholders(t *testing.T) {
	const innerSecret = "marc.durand@exemple.fr"

	outerOut, _ := preRequestOnNode(t, "node-outer", []map[string]any{
		{"role": "user", "content": "Écrire à " + agentBlockSecret},
	})
	var outerMessages []map[string]any
	if err := json.Unmarshal([]byte(outerOut.ModifiedMessagesJson), &outerMessages); err != nil {
		t.Fatal(err)
	}
	// What the inner node receives: the outer node's output, plus a value the
	// inner node pseudonymizes itself.
	outerMessages = append(outerMessages, map[string]any{"role": "user", "content": "Et en copie " + innerSecret})
	innerOut, innerHost := preRequestOnNode(t, "node-inner", outerMessages)

	outerPlaceholder := "⟦EMAIL_1_" + nodeNonce("node-outer") + "⟧"
	innerPlaceholder := "⟦EMAIL_1_" + nodeNonce("node-inner") + "⟧"
	for _, placeholder := range []string{outerPlaceholder, innerPlaceholder} {
		if !strings.Contains(innerOut.ModifiedMessagesJson, placeholder) {
			t.Fatalf("the model does not see %s: %s", placeholder, innerOut.ModifiedMessagesJson)
		}
	}
	innerHost.assertNoEventOfType(t, "passthrough")
	innerHost.assertNoEventOfType(t, eventPlaceholdersNeutralized)

	// The backward pass runs in reverse order: inner node first.
	response := "J'écris à " + outerPlaceholder + " avec " + innerPlaceholder + " en copie."
	fromInner, innerBackHost := postResponseWithState(t, innerOut.NodeState, response, "")
	fromOuter, _ := postResponseWithState(t, outerOut.NodeState, fromInner.ModifiedResponseContent, "")

	if want := "J'écris à " + agentBlockSecret + " avec " + innerSecret + " en copie."; fromOuter.ModifiedResponseContent != want {
		t.Errorf("restored = %q, want %q", fromOuter.ModifiedResponseContent, want)
	}
	innerBackHost.assertNoEventOfType(t, eventPlaceholdersNeutralized)
}

// regexOnlyRecognizer finds the identifiers and secrets go-anon knows by
// pattern, so placeholders of every form can be produced without a model.
type regexOnlyRecognizer struct{}

func (regexOnlyRecognizer) Recognize(text string) ([]ner.Entity, error) {
	patterns := append(append([]ner.RegexPattern{}, goanon.BuiltinRegexPatterns...), goanon.SecretPatterns()...)
	return ner.RegexEntityFilter(patterns)(text, nil), nil
}

// anyDelimited matches whatever sits between the placeholder delimiters, so a
// change of format in go-anon shows up as a placeholder placeholderPattern
// misses.
var anyDelimited = regexp.MustCompile(`⟦[^⟦⟧]*⟧`)

// placeholderPattern copies the pattern go-anon refuses a text with. If go-anon
// changes its format, masking misses the new placeholders and the content they
// sit in leaves in clear again: produce one of every form with go-anon itself
// and check go-anon accepts the masked text.
func TestMaskPlaceholders_CoversEveryFormGoAnonWrites(t *testing.T) {
	key, err := goanon.ParseHashKey(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	source := "Écrire à " + agentBlockSecret + " depuis 192.168.1.10, IBAN FR7630006000011234567890189, clé AKIAIOSFODNN7EXAMPLE."

	for name, tc := range map[string]struct {
		strategy goanon.Strategy
		opts     []goanon.AnonymizeOption
	}{
		"tag":        {strategy: goanon.TagReplace},
		"consistent": {strategy: goanon.Consistent},
		"hash":       {strategy: goanon.Hash, opts: []goanon.AnonymizeOption{goanon.WithHashKey(key)}},
	} {
		t.Run(name, func(t *testing.T) {
			anon := goanon.NewAnonymizer(regexOnlyRecognizer{}, goanon.Config{Strategy: tc.strategy, ConsistentMap: true})
			session := goanon.NewSession()
			result, err := anon.Anonymize(source, append(tc.opts, anonymizer.WithSession(session))...)
			if err != nil {
				t.Fatalf("anonymize: %v", err)
			}

			placeholders := anyDelimited.FindAllString(result.Text, -1)
			if len(placeholders) < 3 {
				t.Fatalf("expected several placeholders, got %q", result.Text)
			}
			redacted := false
			for _, placeholder := range placeholders {
				if !placeholderPattern.MatchString(placeholder) || placeholderPattern.FindString(placeholder) != placeholder {
					t.Errorf("placeholderPattern misses %q", placeholder)
				}
				redacted = redacted || strings.HasSuffix(placeholder, "_REDACTED⟧")
			}
			if !redacted {
				t.Errorf("no redacted marker produced: %q", result.Text)
			}

			// The next turn: go-anon must accept the masked text, whichever
			// node the placeholders belong to.
			for _, own := range []func(string) bool{ownedBy(session.Nonce()), ownedBy("")} {
				masked := maskPlaceholders(result.Text, own)
				_, err := anon.Anonymize(masked.Text, append(tc.opts, anonymizer.WithSession(goanon.NewSession()))...)
				if errors.Is(err, anonymizer.ErrPlaceholderCollision) {
					t.Errorf("go-anon refuses the masked text %q", masked.Text)
				} else if err != nil {
					t.Errorf("anonymize masked text: %v", err)
				}
			}
		})
	}
}

// Skipping PostResponse lets the host stream the response, and also skips the
// neutralization of invented placeholders: only when the model saw no
// placeholder of the node, live or inert, to imitate.
func TestPreRequest_NoResponseRewriteOnlyWhenTheModelSawNoPlaceholder(t *testing.T) {
	nonce := nodeNonce("node-1")

	for name, tc := range map[string]struct {
		content string
		want    bool
	}{
		"nothing to pseudonymize":       {content: "Bonjour, quel temps fait-il ?", want: true},
		"another node's placeholder":    {content: "Bonjour ⟦PERSON_1_a57a4e⟧", want: true},
		"live placeholder of the node":  {content: "Le ⟦LOCATION_15_" + nonce + "⟧ convient.", want: false},
		"inert placeholder of the node": {content: "Le [[LOCATION_15_" + nonce + "]] convient.", want: false},
		"an entity":                     {content: "Écrire à " + agentBlockSecret, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			out, _ := preRequestOnNode(t, "node-1", []map[string]any{{"role": "user", "content": tc.content}})
			if out.NoResponseRewrite != tc.want {
				t.Errorf("NoResponseRewrite = %v, want %v", out.NoResponseRewrite, tc.want)
			}
		})
	}
}
