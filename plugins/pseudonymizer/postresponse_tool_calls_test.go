package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// postResponseWithToolCalls runs PostResponse over a known mapping.
func postResponseWithToolCalls(t *testing.T, mapping map[string]string, content, toolCallsJSON string) *proto.PostResponseOutput {
	t.Helper()

	state, err := json.Marshal(pluginState{Mapping: mapping})
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}

	out, err := newPlugin().PostResponse(context.Background(), &proto.PostResponseInput{
		ResponseContent:       content,
		ResponseToolCallsJson: toolCallsJSON,
		NodeState:             state,
	})
	if err != nil {
		t.Fatalf("PostResponse: %v", err)
	}
	return out
}

// The model derives a new call from the pseudonymized history it read. Left
// alone, the placeholder reaches the client, which runs the call against it.
func TestPostResponse_RestoresPlaceholdersInToolCalls(t *testing.T) {
	mapping := map[string]string{"⟦PERSON_1⟧": "Jean Dupont"}

	out := postResponseWithToolCalls(t, mapping,
		"Je lis le fichier de ⟦PERSON_1⟧.",
		`[{"id":"call_1","name":"Read","arguments":"{\"path\":\"/home/⟦PERSON_1⟧/notes.md\"}"}]`,
	)

	if !strings.Contains(out.ModifiedResponseContent, "Jean Dupont") {
		t.Errorf("response content not restored: %q", out.ModifiedResponseContent)
	}

	var calls []map[string]any
	if err := json.Unmarshal([]byte(out.ModifiedToolCallsJson), &calls); err != nil {
		t.Fatalf("modified tool calls are not valid JSON (%v): %q", err, out.ModifiedToolCallsJson)
	}
	if len(calls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(calls))
	}
	if calls[0]["id"] != "call_1" || calls[0]["name"] != "Read" {
		t.Errorf("id and name must survive: %#v", calls[0])
	}
	args, _ := calls[0]["arguments"].(string)
	if !strings.Contains(args, "/home/Jean Dupont/notes.md") {
		t.Errorf("arguments not restored: %q", args)
	}
}

func TestPostResponse_ToolCallsWithoutPlaceholders(t *testing.T) {
	out := postResponseWithToolCalls(t, map[string]string{"⟦PERSON_1⟧": "Jean Dupont"},
		"Voilà.",
		`[{"id":"call_1","name":"Read","arguments":"{\"path\":\"/etc/hosts\"}"}]`,
	)

	if out.ModifiedToolCallsJson != "" {
		t.Errorf("tool calls rewritten with nothing to restore: %q", out.ModifiedToolCallsJson)
	}
}

func TestDeanonymizeToolCalls_Shapes(t *testing.T) {
	mapping := map[string]string{"⟦PERSON_1⟧": "Jean Dupont"}

	for name, tc := range map[string]struct {
		in      string
		mapping map[string]string
		wantErr bool
	}{
		"no tool calls":  {in: "", mapping: mapping},
		"empty mapping":  {in: `[{"id":"1","arguments":"⟦PERSON_1⟧"}]`, mapping: nil},
		"malformed JSON": {in: "not json", mapping: mapping, wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := deanonymizeToolCalls(tc.in, tc.mapping)
			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if got != "" {
				t.Errorf("result = %q, want empty", got)
			}
		})
	}
}

// A restored value is not always JSON-safe. Substituting it into the encoded
// arguments would leave a document the client can no longer parse, so the call
// it was supposed to fix fails anyway, just later and with a worse error.
func TestPostResponse_RestoredValueWithABackslashKeepsTheArgumentsParseable(t *testing.T) {
	mapping := map[string]string{"⟦PERSON_1⟧": `C:\Users\sophie`}

	out := postResponseWithToolCalls(t, mapping, "ok",
		`[{"id":"call_1","name":"Read","arguments":"{\"path\":\"⟦PERSON_1⟧/notes.md\"}"}]`,
	)

	if got := toolCallArgumentValue(t, out.ModifiedToolCallsJson, "path"); got != `C:\Users\sophie/notes.md` {
		t.Errorf("path = %q, want %q", got, `C:\Users\sophie/notes.md`)
	}
}

// A postal address spanning two lines is the everyday version of the same
// problem: a raw newline inside a JSON string is not valid JSON.
func TestPostResponse_MultilineRestoredValueKeepsTheArgumentsParseable(t *testing.T) {
	mapping := map[string]string{"⟦ADDRESS_1⟧": "12 rue des Lilas\n75011 Paris"}

	out := postResponseWithToolCalls(t, mapping, "ok",
		`[{"id":"call_1","name":"Send","arguments":"{\"to\":\"⟦ADDRESS_1⟧\"}"}]`,
	)

	if got := toolCallArgumentValue(t, out.ModifiedToolCallsJson, "to"); got != "12 rue des Lilas\n75011 Paris" {
		t.Errorf("to = %q", got)
	}
}

// Arguments that are not a JSON document at all still get their placeholders
// back: the substitution simply happens on the text.
func TestPostResponse_NonJSONArgumentsAreStillRestored(t *testing.T) {
	mapping := map[string]string{"⟦PERSON_1⟧": "Jean Dupont"}

	out := postResponseWithToolCalls(t, mapping, "ok",
		`[{"id":"call_1","name":"Search","arguments":"cherche ⟦PERSON_1⟧"}]`,
	)

	var calls []map[string]any
	if err := json.Unmarshal([]byte(out.ModifiedToolCallsJson), &calls); err != nil {
		t.Fatalf("modified tool calls are not valid JSON (%v): %q", err, out.ModifiedToolCallsJson)
	}
	if args, _ := calls[0]["arguments"].(string); args != "cherche Jean Dupont" {
		t.Errorf("arguments = %q", args)
	}
}

// toolCallArgumentValue decodes the arguments of the first call and returns one
// of its fields, failing the test if the arguments stopped being valid JSON.
func toolCallArgumentValue(t *testing.T, toolCallsJSON, field string) string {
	t.Helper()

	var calls []map[string]any
	if err := json.Unmarshal([]byte(toolCallsJSON), &calls); err != nil {
		t.Fatalf("modified tool calls are not valid JSON (%v): %q", err, toolCallsJSON)
	}
	if len(calls) == 0 {
		t.Fatal("no tool call in the output")
	}

	args, _ := calls[0]["arguments"].(string)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(args), &decoded); err != nil {
		t.Fatalf("the arguments are no longer valid JSON (%v): %q", err, args)
	}

	value, _ := decoded[field].(string)
	return value
}
