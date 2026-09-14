package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

const agentBlockSecret = "sophie.guerin@exemple.fr"

// preRequestPartsWithHost is preRequestWithParts plus a captureHost, for tests
// that need to assert on an emitted event rather than just the output body.
func preRequestPartsWithHost(t *testing.T, cfg Config, parts []any) (*proto.PreRequestOutput, *captureHost) {
	t.Helper()

	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	messages := []map[string]any{{"role": "user", "content": parts}}
	messagesJSON, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("marshal messages: %v", err)
	}

	host := &captureHost{HostClient: newFakeUIHost()}
	p := attachmentPlugin(t, cfg)
	p.SetHostClient(host)

	out, err := p.PreRequest(context.Background(), &proto.PreRequestInput{
		Ctx:          &proto.RequestContext{OrgId: "org-1", ConfigJson: string(cfgJSON)},
		MessagesJson: string(messagesJSON),
	})
	if err != nil {
		t.Fatalf("PreRequest: %v", err)
	}
	return out, host
}

// Server-side and MCP tools carry the same call/result shapes as a
// client-side tool_use/tool_result. Before this, only the literal types
// "tool_use" and "tool_result" were recognized, so these fell through to the
// attachment path exactly like the blocks fixed in #13 — refused under
// "block", stripped blind under "remove".

func TestPreRequest_ServerToolUseArgumentsArePseudonymized(t *testing.T) {
	cfg := attachmentConfig(t)
	out := preRequestWithParts(t, cfg, []any{
		map[string]any{
			"type": "server_tool_use",
			"id":   "srvtoolu_1",
			"name": "code_execution",
			"input": map[string]any{
				"query": "adresse de " + agentBlockSecret,
			},
		},
	})

	if !out.Allowed {
		t.Fatalf("request refused: %s", out.RejectionReason)
	}
	part := toolResultPart(t, userMessageParts(t, out))
	if part["id"] != "srvtoolu_1" || part["name"] != "code_execution" {
		t.Errorf("id and name must be preserved, got id=%#v name=%#v", part["id"], part["name"])
	}
	input, _ := part["input"].(map[string]any)
	if query, _ := input["query"].(string); strings.Contains(query, agentBlockSecret) {
		t.Errorf("server_tool_use arguments were forwarded unpseudonymized: %q", query)
	}
}

// Per the web search tool's docs, the assistant's content blocks for a
// search turn round-trip as a whole, not field by field — so unlike a plain
// server_tool_use, the query here is left untouched, same as the
// web_search_tool_result it pairs with. It is still scanned read-only so the
// leak is not silent.
func TestPreRequest_WebSearchServerToolUseQueryIsLeftUntouched(t *testing.T) {
	cfg := attachmentConfig(t)
	out, host := preRequestPartsWithHost(t, cfg, []any{
		map[string]any{
			"type": "server_tool_use",
			"id":   "srvtoolu_6",
			"name": "web_search",
			"input": map[string]any{
				"query": "adresse de " + agentBlockSecret,
			},
		},
	})

	if !out.Allowed {
		t.Fatalf("request refused: %s", out.RejectionReason)
	}
	part := toolResultPart(t, userMessageParts(t, out))
	input, _ := part["input"].(map[string]any)
	if query, _ := input["query"].(string); query != "adresse de "+agentBlockSecret {
		t.Errorf("web_search query was rewritten, got %q", query)
	}
	evt := host.waitForEvent(t)
	if evt.Type != "sensitive-data.detected" {
		t.Fatalf("event type = %q, want sensitive-data.detected (the leak went unreported)", evt.Type)
	}
	if evt.Attributes["leak_entities"] == "0" || evt.Attributes["leak_entities"] == "" {
		t.Errorf("leak_entities = %q, want a non-zero count", evt.Attributes["leak_entities"])
	}
}

// An unrecognized part type is forwarded untouched, but not silently: any
// free text buried in it is scanned read-only, the same principle as
// thinking, so an operator on the "block" policy is not left inferring
// nothing got through when something did.
func TestPreRequest_UnrecognizedPartTypeLeakIsReported(t *testing.T) {
	cfg := attachmentConfig(t)
	out, host := preRequestPartsWithHost(t, cfg, []any{
		map[string]any{
			"type": "search_result",
			"source": map[string]any{
				"title":   "Fiche client",
				"content": []any{map[string]any{"type": "text", "text": "contact : " + agentBlockSecret}},
			},
		},
	})

	if !out.Allowed {
		t.Fatalf("request refused: %s", out.RejectionReason)
	}
	if !strings.Contains(out.ModifiedMessagesJson, agentBlockSecret) {
		t.Fatalf("the part itself should still be forwarded verbatim: %s", out.ModifiedMessagesJson)
	}
	evt := host.waitForEvent(t)
	if evt.Type != "sensitive-data.detected" {
		t.Fatalf("event type = %q, want sensitive-data.detected (the leak went unreported)", evt.Type)
	}
	if evt.Attributes["entities"] != "0" {
		t.Errorf("entities (pseudonymized count) = %q, want 0: nothing here was rewritten", evt.Attributes["entities"])
	}
	if evt.Attributes["leak_entities"] == "0" || evt.Attributes["leak_entities"] == "" {
		t.Errorf("leak_entities = %q, want a non-zero count", evt.Attributes["leak_entities"])
	}
}

func TestPreRequest_MCPToolUseArgumentsArePseudonymized(t *testing.T) {
	cfg := attachmentConfig(t)
	out := preRequestWithParts(t, cfg, []any{
		map[string]any{
			"type": "mcp_tool_use",
			"id":   "mcptoolu_1",
			"name": "search_customers",
			"input": map[string]any{
				"email": agentBlockSecret,
			},
		},
	})

	if !out.Allowed {
		t.Fatalf("request refused: %s", out.RejectionReason)
	}
	part := toolResultPart(t, userMessageParts(t, out))
	input, _ := part["input"].(map[string]any)
	if email, _ := input["email"].(string); strings.Contains(email, agentBlockSecret) {
		t.Errorf("mcp_tool_use arguments were forwarded unpseudonymized: %q", email)
	}
}

// A web_search_tool_result's `content` is a list of `web_search_result`
// blocks — title, url, page_age, and an encrypted_content the API requires
// back byte for byte on the next turn, the same provider-signature contract
// as `thinking` — never `text` blocks. The documentation asks for the
// assistant's content blocks to round-trip as a whole, so even `title`, which
// is free text, is forwarded rather than singled out for rewriting — and, as
// for every other clear-text path, the leak is reported rather than silent.
func TestPreRequest_WebSearchToolResultIsLeftUntouched(t *testing.T) {
	cfg := attachmentConfig(t)
	block := map[string]any{
		"type":              "web_search_result",
		"title":             "Dossier client " + agentBlockSecret,
		"url":               "https://example.com/dossier",
		"encrypted_content": "EnCrYpTeD-signed-payload==",
		"page_age":          "3 days ago",
	}
	out, host := preRequestPartsWithHost(t, cfg, []any{
		map[string]any{
			"type":        "web_search_tool_result",
			"tool_use_id": "srvtoolu_2",
			"content":     []any{block},
		},
	})

	if !out.Allowed {
		t.Fatalf("request refused: %s", out.RejectionReason)
	}
	part := toolResultPart(t, userMessageParts(t, out))
	if part["tool_use_id"] != "srvtoolu_2" {
		t.Errorf("the pairing was lost: %#v", part)
	}
	content, ok := part["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("expected the single web_search_result block, got %#v", part["content"])
	}
	got, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("block is not an object: %#v", content[0])
	}
	if got["encrypted_content"] != block["encrypted_content"] {
		t.Errorf("encrypted_content was altered, the provider will reject it next turn: %#v", got["encrypted_content"])
	}
	if got["title"] != block["title"] {
		t.Errorf("block was rewritten instead of forwarded as is: %#v", got)
	}

	// THE ASSERTION WHOSE ABSENCE LET THE SILENT PASSTHROUGH THROUGH. The
	// secret sits in `title` and survives on purpose; what must not survive is
	// the operator having no way to know it did.
	evt := host.waitForEvent(t)
	if evt.Type != "sensitive-data.detected" {
		t.Fatalf("event type = %q, want sensitive-data.detected (the leak went unreported)", evt.Type)
	}
	if evt.Attributes["leak_entities"] == "0" || evt.Attributes["leak_entities"] == "" {
		t.Errorf("leak_entities = %q, want a non-zero count", evt.Attributes["leak_entities"])
	}
}

// A code_execution_tool_result's `content` is an object — {type, stdout,
// stderr, return_code} — not a list. return_code must stay a number and type
// is a protocol enum: only stdout/stderr are free text, and only those get
// pseudonymized.
func TestPreRequest_CodeExecutionToolResultIsPseudonymized(t *testing.T) {
	cfg := attachmentConfig(t)
	out := preRequestWithParts(t, cfg, []any{
		map[string]any{
			"type":        "code_execution_tool_result",
			"tool_use_id": "srvtoolu_3",
			"content": map[string]any{
				"type":        "code_execution_result",
				"stdout":      "client : " + agentBlockSecret,
				"stderr":      "",
				"return_code": float64(0),
			},
		},
	})

	if !out.Allowed {
		t.Fatalf("request refused: %s", out.RejectionReason)
	}
	if strings.Contains(out.ModifiedMessagesJson, agentBlockSecret) {
		t.Errorf("code_execution_tool_result stdout was forwarded unpseudonymized: %s", out.ModifiedMessagesJson)
	}
	part := toolResultPart(t, userMessageParts(t, out))
	content, ok := part["content"].(map[string]any)
	if !ok {
		t.Fatalf("content is not an object: %#v", part["content"])
	}
	if content["type"] != "code_execution_result" {
		t.Errorf("content type must be preserved, got %#v", content["type"])
	}
	if content["return_code"] != float64(0) {
		t.Errorf("return_code must stay a number, got %#v", content["return_code"])
	}
	stdout, _ := content["stdout"].(string)
	if strings.Contains(stdout, agentBlockSecret) {
		t.Errorf("stdout was forwarded unpseudonymized: %q", stdout)
	}
	if !strings.Contains(stdout, "client :") {
		t.Errorf("stdout text was dropped instead of rewritten: %q", stdout)
	}
}

// The real, current API (code_execution_20250825 and later) never emits a
// bare "code_execution_tool_result": running a command answers
// "bash_code_execution_tool_result", content.type
// "bash_code_execution_result", with a nested "content" list of generated
// file references alongside stdout/stderr/return_code. Only the older,
// legacy tool version used the bare name this plugin originally assumed.
func TestPreRequest_BashCodeExecutionToolResultIsPseudonymized(t *testing.T) {
	cfg := attachmentConfig(t)
	out := preRequestWithParts(t, cfg, []any{
		map[string]any{
			"type":        "bash_code_execution_tool_result",
			"tool_use_id": "srvtoolu_4",
			"content": map[string]any{
				"type":        "bash_code_execution_result",
				"stdout":      "contact : " + agentBlockSecret,
				"stderr":      "",
				"return_code": float64(0),
				"content":     []any{map[string]any{"type": "code_execution_output", "file_id": "file_abc"}},
			},
		},
	})

	if !out.Allowed {
		t.Fatalf("request refused: %s", out.RejectionReason)
	}
	part := toolResultPart(t, userMessageParts(t, out))
	content, ok := part["content"].(map[string]any)
	if !ok {
		t.Fatalf("content is not an object: %#v", part["content"])
	}
	if content["type"] != "bash_code_execution_result" {
		t.Errorf("content type must be preserved, got %#v", content["type"])
	}
	if content["return_code"] != float64(0) {
		t.Errorf("return_code must stay a number, got %#v", content["return_code"])
	}
	files, ok := content["content"].([]any)
	if !ok || len(files) != 1 {
		t.Errorf("the generated-file list must be preserved untouched, got %#v", content["content"])
	}
	stdout, _ := content["stdout"].(string)
	if strings.Contains(stdout, agentBlockSecret) {
		t.Errorf("stdout was forwarded unpseudonymized: %q", stdout)
	}
	if !strings.Contains(stdout, "contact :") {
		t.Errorf("stdout text was dropped instead of rewritten: %q", stdout)
	}
}

// text_editor_code_execution_tool_result (view/create/str_replace file
// operations) is a known, documented gap: its content shape varies by command
// and none of it is consistently free text, so it is not specifically
// handled. What matters here is that it does NOT reproduce #16 — an
// unrecognized part type must be forwarded, not refused or stripped.
func TestPreRequest_TextEditorCodeExecutionToolResultDoesNotBlockTheRequest(t *testing.T) {
	cfg := attachmentConfig(t)
	out := preRequestWithParts(t, cfg, []any{
		map[string]any{
			"type":        "text_editor_code_execution_tool_result",
			"tool_use_id": "srvtoolu_5",
			"content": map[string]any{
				"type":    "text_editor_code_execution_view_result",
				"content": "contact : " + agentBlockSecret,
			},
		},
	})

	if !out.Allowed {
		t.Fatalf("an unrecognized part type must never refuse the request, got: %s", out.RejectionReason)
	}
}

func TestPreRequest_MCPToolResultIsPseudonymized(t *testing.T) {
	cfg := attachmentConfig(t)
	out := preRequestWithParts(t, cfg, []any{
		map[string]any{
			"type":        "mcp_tool_result",
			"tool_use_id": "mcptoolu_2",
			"content": []any{
				map[string]any{"type": "text", "text": "client : " + agentBlockSecret},
			},
		},
	})

	if !out.Allowed {
		t.Fatalf("request refused: %s", out.RejectionReason)
	}
	if strings.Contains(out.ModifiedMessagesJson, agentBlockSecret) {
		t.Errorf("mcp_tool_result content was forwarded unpseudonymized: %s", out.ModifiedMessagesJson)
	}
}

// THE 403 #16 IS ABOUT. A `thinking` block carries no inline bytes any more
// than a tool block does, so before this it fell through to the same
// attachment path and got refused under the default "block" policy — right
// after the very first turn of any client using extended thinking.
func TestPreRequest_ThinkingBlockDoesNotBlockTheRequest(t *testing.T) {
	cfg := attachmentConfig(t)
	out := preRequestWithParts(t, cfg, []any{
		map[string]any{
			"type":      "thinking",
			"thinking":  "l'utilisateur mentionne " + agentBlockSecret,
			"signature": "sig-abc123",
		},
	})

	if !out.Allowed {
		t.Fatalf("a thinking block must never refuse the request, got: %s", out.RejectionReason)
	}
}

// A thinking block is provider-signed: the signature covers the exact text
// the model produced, so it must survive byte for byte, secret and all. There
// is no way to pseudonymize the visible half of a signed pair without
// invalidating it.
func TestPreRequest_ThinkingBlockIsLeftByteForByteUntouched(t *testing.T) {
	cfg := attachmentConfig(t)
	out := preRequestWithParts(t, cfg, []any{
		map[string]any{
			"type":      "thinking",
			"thinking":  "l'utilisateur mentionne " + agentBlockSecret,
			"signature": "sig-abc123",
		},
	})

	if !out.Allowed {
		t.Fatalf("request refused: %s", out.RejectionReason)
	}
	part := toolResultPart(t, userMessageParts(t, out))
	if part["type"] != "thinking" {
		t.Errorf("part type changed: %#v", part["type"])
	}
	if part["thinking"] != "l'utilisateur mentionne "+agentBlockSecret {
		t.Errorf("thinking text was rewritten, signature is now invalid: %#v", part["thinking"])
	}
	if part["signature"] != "sig-abc123" {
		t.Errorf("signature was altered: %#v", part["signature"])
	}
}

// A thinking block is forwarded unmodified, but a leak inside it must not be
// invisible to the operator either: the text is scanned read-only (Detect,
// not Anonymize — no mapping, nothing rewritten) and folded into the same
// sensitive-data.detected event as everything else the request pseudonymized.
func TestPreRequest_ThinkingBlockLeakIsReportedNotSilent(t *testing.T) {
	cfg := attachmentConfig(t)
	out, host := preRequestPartsWithHost(t, cfg, []any{
		map[string]any{
			"type":      "thinking",
			"thinking":  "l'utilisateur mentionne " + agentBlockSecret,
			"signature": "sig-abc123",
		},
	})

	if !out.Allowed {
		t.Fatalf("request refused: %s", out.RejectionReason)
	}
	evt := host.waitForEvent(t)
	if evt.Type != "sensitive-data.detected" {
		t.Fatalf("event type = %q, want sensitive-data.detected (the leak went unreported)", evt.Type)
	}
	if evt.Attributes["entities"] != "0" {
		t.Errorf("entities (pseudonymized count) = %q, want 0: a thinking block is never rewritten", evt.Attributes["entities"])
	}
	if evt.Attributes["leak_entities"] == "0" || evt.Attributes["leak_entities"] == "" {
		t.Errorf("leak_entities = %q, want a non-zero count", evt.Attributes["leak_entities"])
	}
}

// When a request both pseudonymizes something and leaks something else (in a
// thinking block here), the two must stay in separate buckets: entities/types
// describe only what was actually rewritten, matching len(session.Mapping),
// and leak_entities/leak_types describe only what was left in clear. Mixing
// them would make "entities" lie about how much was actually protected.
func TestPreRequest_PseudonymizedAndLeakedEntitiesAreCountedSeparately(t *testing.T) {
	cfg := attachmentConfig(t)
	const otherSecret = "marc.durand@exemple.fr"
	out, host := preRequestPartsWithHost(t, cfg, []any{
		map[string]any{"type": "text", "text": "contact : " + otherSecret},
		map[string]any{
			"type":      "thinking",
			"thinking":  "l'utilisateur mentionne " + agentBlockSecret,
			"signature": "sig-abc123",
		},
	})

	if !out.Allowed {
		t.Fatalf("request refused: %s", out.RejectionReason)
	}
	if strings.Contains(out.ModifiedMessagesJson, otherSecret) {
		t.Errorf("the text part should have been pseudonymized: %s", out.ModifiedMessagesJson)
	}
	if !strings.Contains(out.ModifiedMessagesJson, agentBlockSecret) {
		t.Errorf("the thinking block should have survived byte for byte, secret included: %s", out.ModifiedMessagesJson)
	}
	evt := host.waitForEvent(t)
	if evt.Attributes["entities"] != "1" {
		t.Errorf("entities = %q, want 1 (only the pseudonymized text part)", evt.Attributes["entities"])
	}
	if evt.Attributes["leak_entities"] != "1" {
		t.Errorf("leak_entities = %q, want 1 (only the thinking block)", evt.Attributes["leak_entities"])
	}
	if !strings.Contains(evt.Message, "pseudonymisée") || !strings.Contains(evt.Message, "clair") {
		t.Errorf("message should mention both the pseudonymized and the leaked count, got %q", evt.Message)
	}
}

// An OpenAI Responses text part spells its type "input_text" and carries the
// text in the same `text` field. Matching only "text" left that traffic to the
// catch-all, forwarded in clear when it is plainly rewritable.
func TestPreRequest_InputTextPartIsPseudonymized(t *testing.T) {
	cfg := attachmentConfig(t)
	out := preRequestWithParts(t, cfg, []any{
		map[string]any{"type": "input_text", "text": "contact : " + agentBlockSecret},
	})

	if !out.Allowed {
		t.Fatalf("request refused: %s", out.RejectionReason)
	}
	if strings.Contains(out.ModifiedMessagesJson, agentBlockSecret) {
		t.Errorf("an input_text part was forwarded unpseudonymized: %s", out.ModifiedMessagesJson)
	}
	part := toolResultPart(t, userMessageParts(t, out))
	if part["type"] != "input_text" {
		t.Errorf("part type changed: %#v", part["type"])
	}
	if text, _ := part["text"].(string); !strings.Contains(text, "contact :") {
		t.Errorf("the part lost its text: %q", text)
	}
}

// redacted_thinking carries no plaintext at all: its `data` is an opaque
// encrypted blob the provider must receive unchanged.
func TestPreRequest_RedactedThinkingBlockDoesNotBlockTheRequest(t *testing.T) {
	cfg := attachmentConfig(t)
	out := preRequestWithParts(t, cfg, []any{
		map[string]any{
			"type": "redacted_thinking",
			"data": "opaque-encrypted-blob",
		},
	})

	if !out.Allowed {
		t.Fatalf("a redacted_thinking block must never refuse the request, got: %s", out.RejectionReason)
	}
	part := toolResultPart(t, userMessageParts(t, out))
	if part["data"] != "opaque-encrypted-blob" {
		t.Errorf("redacted_thinking data was altered: %#v", part["data"])
	}
}

// The whole `input` of a web_search server_tool_use leaves in clear, not just
// its query: `user_location` carries a city and a region. Scanning only the
// query left those uncounted, while the node documentation promises that
// everything forwarded unrewritten is scanned read-only.
func TestPreRequest_WebSearchServerToolUseIsScannedBeyondItsQuery(t *testing.T) {
	cfg := attachmentConfig(t)
	out, host := preRequestPartsWithHost(t, cfg, []any{
		map[string]any{
			"type": "server_tool_use",
			"id":   "srvtoolu_1",
			"name": "web_search",
			"input": map[string]any{
				"query":         "météo demain",
				"user_location": map[string]any{"city": "écrire à " + agentBlockSecret},
			},
		},
	})

	if !strings.Contains(out.ModifiedMessagesJson, agentBlockSecret) {
		t.Error("the block must still travel byte for byte")
	}

	evt := host.waitForEvent(t)
	if got := evt.Attributes["leak_entities"]; got == "" || got == "0" {
		t.Errorf("leak_entities = %q, want the user_location leak counted", got)
	}
}

// A leaked type count has to be in the same unit as the leaked total, or the
// same event says one value escaped and names five of them.
func TestPreRequest_LeakTypesAreDistinctValuesToo(t *testing.T) {
	cfg := attachmentConfig(t)
	_, host := preRequestPartsWithHost(t, cfg, []any{
		map[string]any{
			"type":      "thinking",
			"signature": "sig-abc123",
			"thinking":  strings.Repeat("note : marc.durand@exemple.fr. ", 5),
		},
	})

	evt := host.waitForEvent(t)
	if got := evt.Attributes["leak_types"]; got != "EMAIL:1" {
		t.Errorf("leak_types = %q, want EMAIL:1", got)
	}
	if got := evt.Attributes["leak_entities"]; got != "1" {
		t.Errorf("leak_entities = %q, want 1", got)
	}
}

// The same value written two ways is one leak, as it would be one mapping
// entry once pseudonymized.
func TestPreRequest_LeakDeduplicationMatchesTheSessionNormalization(t *testing.T) {
	cfg := attachmentConfig(t)
	_, host := preRequestPartsWithHost(t, cfg, []any{
		map[string]any{
			"type":      "thinking",
			"signature": "sig-abc123",
			"thinking":  "note : Marc.Durand@Exemple.fr puis marc.durand@exemple.fr",
		},
	})

	evt := host.waitForEvent(t)
	if got := evt.Attributes["leak_entities"]; got != "1" {
		t.Errorf("leak_entities = %q, want 1", got)
	}
}

// `entities` counts distinct pseudonymized values, so the leak count set beside
// it in the same sentence has to be distinct too. One address quoted five times
// is one leak; reporting five reads as five different things having escaped.
func TestPreRequest_LeakCountIsDistinctValuesNotOccurrences(t *testing.T) {
	cfg := attachmentConfig(t)
	_, host := preRequestPartsWithHost(t, cfg, []any{
		map[string]any{"type": "text", "text": "écris à " + agentBlockSecret},
		map[string]any{
			"type":      "thinking",
			"signature": "sig-abc123",
			"thinking":  strings.Repeat("note : marc.durand@exemple.fr. ", 5),
		},
	})

	evt := host.waitForEvent(t)
	if got := evt.Attributes["entities"]; got != "1" {
		t.Errorf("entities = %q, want 1", got)
	}
	if got := evt.Attributes["leak_entities"]; got != "1" {
		t.Errorf("leak_entities = %q, want 1: the same address five times is one leak", got)
	}
}

// An agentic conversation re-scans its whole history every turn, so the
// kilobytes of base64 in a search result's encrypted_content would be read
// again on every request to find nothing. Declared opaque, so never scanned.
func TestPreRequest_OpaqueFieldsAreNotScannedForLeaks(t *testing.T) {
	cfg := attachmentConfig(t)
	_, host := preRequestPartsWithHost(t, cfg, []any{
		map[string]any{
			"type":        "web_search_tool_result",
			"tool_use_id": "srvtoolu_2",
			"content": []any{
				map[string]any{
					"type":              "web_search_result",
					"title":             "un titre anodin",
					"url":               "https://exemple.fr/a",
					"encrypted_content": agentBlockSecret,
				},
			},
		},
	})

	host.assertNoEvent(t, 500*time.Millisecond)
}

// The assistant's own text in a Responses history. Left out of the text family,
// the model's restatement of a personal detail travels in clear on the next
// turn while the question that prompted it was pseudonymized.
func TestPreRequest_OutputTextPartIsPseudonymized(t *testing.T) {
	cfg := attachmentConfig(t)
	out, _ := preRequestPartsWithHost(t, cfg, []any{
		map[string]any{"type": "output_text", "text": "j'écris à " + agentBlockSecret},
	})

	if strings.Contains(out.ModifiedMessagesJson, agentBlockSecret) {
		t.Errorf("output_text was forwarded in clear: %s", out.ModifiedMessagesJson)
	}
}
