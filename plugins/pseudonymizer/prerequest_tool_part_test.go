package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const toolPartSecret = "sophie.guerin@exemple.fr"

// imageToolResult is a tool that answered with something the plugin cannot read
// — a screenshot, say.
func imageToolResult(id string) map[string]any {
	return map[string]any{
		"type":        "tool_result",
		"tool_use_id": id,
		"content": []any{
			map[string]any{"type": "image", "source": map[string]any{"type": "base64"}},
		},
	}
}

// toolResultPart decodes the single tool_result left in the request.
func toolResultPart(t *testing.T, parts []any) map[string]any {
	t.Helper()

	if len(parts) != 1 {
		t.Fatalf("expected exactly one part, got %d: %#v", len(parts), parts)
	}
	part, ok := parts[0].(map[string]any)
	if !ok {
		t.Fatalf("part is not an object: %#v", parts[0])
	}
	return part
}

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

	part := toolResultPart(t, userMessageParts(t, out))
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

	part := toolResultPart(t, userMessageParts(t, out))
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

// THE 400 THIS PATCH MUST NOT CAUSE. Under "remove", dropping the tool_result
// would leave its tool_use unpaired in the previous assistant message, and the
// Messages API refuses the whole request on the orphan id — one screenshot
// breaking an entire session. The block stays, its payload is replaced.
func TestPreRequest_NonTextToolResultKeepsItsPlaceUnderRemove(t *testing.T) {
	cfg := attachmentConfig(t)
	cfg.UnsupportedAttachments = "remove"
	out := preRequestWithParts(t, cfg, []any{imageToolResult("toolu_4")})

	if !out.Allowed {
		t.Fatalf("request refused: %s", out.RejectionReason)
	}

	part := toolResultPart(t, userMessageParts(t, out))
	if part["tool_use_id"] != "toolu_4" {
		t.Errorf("the pairing was lost: %#v", part)
	}
	if !strings.Contains(out.ModifiedMessagesJson, nonTextToolPayloadNotice) {
		t.Errorf("the agent was not told its payload was dropped: %s", out.ModifiedMessagesJson)
	}
	if strings.Contains(out.ModifiedMessagesJson, "base64") {
		t.Errorf("the unreadable payload was forwarded: %s", out.ModifiedMessagesJson)
	}
}

// Same under "block": the attachment policy does not reach tool blocks at all.
// It exists for a document a human attached, not for a pair the protocol needs.
func TestPreRequest_NonTextToolResultDoesNotBlockTheRequest(t *testing.T) {
	cfg := attachmentConfig(t)
	cfg.UnsupportedAttachments = "block"
	out := preRequestWithParts(t, cfg, []any{imageToolResult("toolu_5")})

	if !out.Allowed {
		t.Fatalf("a tool_result must never refuse the request, got: %s", out.RejectionReason)
	}
	if !strings.Contains(out.ModifiedMessagesJson, nonTextToolPayloadNotice) {
		t.Errorf("the agent was not told its payload was dropped: %s", out.ModifiedMessagesJson)
	}
}

// Mixed content: the text was the part we could handle, and it is kept.
func TestPreRequest_MixedToolResultKeepsTheTextItCanRead(t *testing.T) {
	cfg := attachmentConfig(t)
	out := preRequestWithParts(t, cfg, []any{
		map[string]any{
			"type":        "tool_result",
			"tool_use_id": "toolu_6",
			"content": []any{
				map[string]any{"type": "text", "text": "relevé de " + toolPartSecret},
				map[string]any{"type": "image", "source": map[string]any{"type": "base64"}},
			},
		},
	})

	if !out.Allowed {
		t.Fatalf("request refused: %s", out.RejectionReason)
	}

	blocks, ok := toolResultPart(t, userMessageParts(t, out))["content"].([]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("expected two blocks, got %#v", blocks)
	}
	first, _ := blocks[0].(map[string]any)
	text, _ := first["text"].(string)
	if strings.Contains(text, toolPartSecret) {
		t.Errorf("the readable text escaped the filter: %q", text)
	}
	if !strings.Contains(text, "relevé de") {
		t.Errorf("the readable text was dropped with the image: %q", text)
	}
	second, _ := blocks[1].(map[string]any)
	if second["text"] != nonTextToolPayloadNotice {
		t.Errorf("the image was not replaced by the notice: %#v", second)
	}
}

// A shape the spec does not describe still must not reach the provider.
func TestPreRequest_ToolResultWithObjectContentIsReplaced(t *testing.T) {
	cfg := attachmentConfig(t)
	out := preRequestWithParts(t, cfg, []any{
		map[string]any{
			"type":        "tool_result",
			"tool_use_id": "toolu_7",
			"content":     map[string]any{"inattendu": toolPartSecret},
		},
	})

	if !out.Allowed {
		t.Fatalf("request refused: %s", out.RejectionReason)
	}
	if strings.Contains(out.ModifiedMessagesJson, toolPartSecret) {
		t.Errorf("an unreadable shape was forwarded as is: %s", out.ModifiedMessagesJson)
	}
	part := toolResultPart(t, userMessageParts(t, out))
	if part["content"] != nonTextToolPayloadNotice {
		t.Errorf("content should have been replaced by the notice, got %#v", part["content"])
	}
}

// A bare string in a content list is not valid per the spec, but it is text all
// the same: it gets the same answer as the text block next to it.
func TestPreRequest_BareStringInToolResultIsPseudonymized(t *testing.T) {
	cfg := attachmentConfig(t)
	out := preRequestWithParts(t, cfg, []any{
		map[string]any{
			"type":        "tool_result",
			"tool_use_id": "toolu_8",
			"content":     []any{"écrit par " + toolPartSecret},
		},
	})

	if !out.Allowed {
		t.Fatalf("request refused: %s", out.RejectionReason)
	}
	if strings.Contains(out.ModifiedMessagesJson, toolPartSecret) {
		t.Errorf("a bare string escaped the filter: %s", out.ModifiedMessagesJson)
	}
	if !strings.Contains(out.ModifiedMessagesJson, "écrit par") {
		t.Errorf("the bare string was dropped instead of rewritten: %s", out.ModifiedMessagesJson)
	}
}

// When the anonymizer itself fails, the error travels up so the caller can
// account for it — the block is not quietly forwarded by this function.
func TestAnonymizeToolPart_PropagatesAnonymizerFailure(t *testing.T) {
	boom := func(string) (string, error) { return "", errors.New("anonymizer down") }

	for name, part := range map[string]map[string]any{
		"tool_use": {
			"type":  "tool_use",
			"id":    "toolu_9",
			"input": map[string]any{"pattern": toolPartSecret},
		},
		"tool_result string": {
			"type":        "tool_result",
			"tool_use_id": "toolu_10",
			"content":     toolPartSecret,
		},
		"tool_result list": {
			"type":        "tool_result",
			"tool_use_id": "toolu_11",
			"content":     []any{map[string]any{"type": "text", "text": toolPartSecret}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := anonymizeToolPart(part, boom); err == nil {
				t.Errorf("expected the failure to travel up, got none")
			}
		})
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
