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
