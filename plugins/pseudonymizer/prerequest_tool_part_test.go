package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const toolPartSecret = "sophie.guerin@exemple.fr"

// An agent's own tool traffic must survive the filter. Before this, every
// tool_result was read as an attachment with no inline bytes, so the request was
// refused outright — or, under the "remove" policy, stripped of the very blocks
// the agent had asked for, which left it running blind for twenty turns.
func TestPreRequest_ToolResultIsPseudonymizedNotRemoved(t *testing.T) {
	cfg := attachmentConfig(t)
	out := preRequestWithParts(t, cfg, []any{
		map[string]any{
			"type":        "tool_result",
			"tool_use_id": "toolu_1",
			"content":     "Fichier lu. Contact du dossier : " + toolPartSecret,
		},
	})

	if !out.Allowed {
		t.Fatalf("request refused: %s", out.RejectionReason)
	}

	parts := userMessageParts(t, out)
	if len(parts) != 1 {
		t.Fatalf("expected the tool_result to survive, got %d part(s): %#v", len(parts), parts)
	}

	part, ok := parts[0].(map[string]any)
	if !ok {
		t.Fatalf("tool_result is not an object: %#v", parts[0])
	}
	if part["type"] != "tool_result" {
		t.Errorf("part type changed: %#v", part["type"])
	}
	if part["tool_use_id"] != "toolu_1" {
		t.Errorf("tool_use_id must be preserved, got %#v", part["tool_use_id"])
	}

	content, _ := part["content"].(string)
	if strings.Contains(content, toolPartSecret) {
		t.Errorf("tool_result content was forwarded unpseudonymized: %q", content)
	}
	if !strings.Contains(content, "Fichier lu") {
		t.Errorf("tool_result content lost its text: %q", content)
	}
}

// A list-shaped tool_result is the common case with Claude Code.
func TestPreRequest_ToolResultTextBlocksArePseudonymized(t *testing.T) {
	cfg := attachmentConfig(t)
	out := preRequestWithParts(t, cfg, []any{
		map[string]any{
			"type":        "tool_result",
			"tool_use_id": "toolu_2",
			"content": []any{
				map[string]any{"type": "text", "text": "ligne 1 : " + toolPartSecret},
				map[string]any{"type": "text", "text": "ligne 2 : rien de sensible"},
			},
		},
	})

	if !out.Allowed {
		t.Fatalf("request refused: %s", out.RejectionReason)
	}
	if strings.Contains(out.ModifiedMessagesJson, toolPartSecret) {
		t.Errorf("a text block of the tool_result escaped the filter: %s", out.ModifiedMessagesJson)
	}
	if !strings.Contains(out.ModifiedMessagesJson, "rien de sensible") {
		t.Errorf("the innocuous block was dropped: %s", out.ModifiedMessagesJson)
	}
}

// The arguments of a call are rewritten; what pairs the call with its result is
// not. Renaming an id would break the history the model reads.
func TestPreRequest_ToolUseArgumentsArePseudonymized(t *testing.T) {
	cfg := attachmentConfig(t)
	out := preRequestWithParts(t, cfg, []any{
		map[string]any{
			"type": "tool_use",
			"id":   "toolu_3",
			"name": "Grep",
			"input": map[string]any{
				"pattern": toolPartSecret,
				"path":    "/srv/dossiers",
			},
		},
	})

	if !out.Allowed {
		t.Fatalf("request refused: %s", out.RejectionReason)
	}

	part, ok := userMessageParts(t, out)[0].(map[string]any)
	if !ok {
		t.Fatalf("tool_use is not an object")
	}
	if part["id"] != "toolu_3" || part["name"] != "Grep" {
		t.Errorf("id and name must be preserved, got id=%#v name=%#v", part["id"], part["name"])
	}

	input, ok := part["input"].(map[string]any)
	if !ok {
		t.Fatalf("input is not an object: %#v", part["input"])
	}
	if pattern, _ := input["pattern"].(string); strings.Contains(pattern, toolPartSecret) {
		t.Errorf("tool arguments were forwarded unpseudonymized: %q", pattern)
	}
	if _, ok := input["path"]; !ok {
		t.Errorf("argument names must be preserved: %#v", input)
	}
}

// A tool that returns an image is not text, and the plugin does not pretend
// otherwise: such a block keeps the attachment policy.
func TestPreRequest_ToolResultWithNonTextPayloadKeepsThePolicy(t *testing.T) {
	cfg := attachmentConfig(t)
	out := preRequestWithParts(t, cfg, []any{
		map[string]any{
			"type":        "tool_result",
			"tool_use_id": "toolu_4",
			"content": []any{
				map[string]any{"type": "image", "source": map[string]any{"type": "base64"}},
			},
		},
	})

	if out.Allowed {
		t.Fatalf("a tool_result carrying an image must follow the attachment policy, got it allowed")
	}
	if !strings.Contains(out.RejectionReason, reasonNonTextToolPart) {
		t.Errorf("the refusal should name the reason, got: %s", out.RejectionReason)
	}
}

func TestAnonymizeLeaves_KeepsShapeAndKeys(t *testing.T) {
	upper := func(s string) (string, error) { return strings.ToUpper(s), nil }

	walked, err := anonymizeLeaves(map[string]any{
		"chemin": "a",
		"liste":  []any{"b", map[string]any{"imbrique": "c"}},
		"nombre": float64(42),
	}, upper)
	if err != nil {
		t.Fatalf("anonymizeLeaves: %v", err)
	}

	encoded, err := json.Marshal(walked)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(encoded)
	want := `{"chemin":"A","liste":["B",{"imbrique":"C"}],"nombre":42}`
	if got != want {
		t.Errorf("shape or keys changed:\n got %s\nwant %s", got, want)
	}
}
