package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/bornholm/go-anon/pkg/ner"
)

const (
	// partTypeText is how the Messages and Chat Completions routes name a text
	// part; partTypeInputText and partTypeOutputText are the OpenAI Responses
	// spellings, for the user turn and the assistant turn of the same
	// thing, carrying its text in the same `text` field.
	partTypeText      = "text"
	partTypeInputText = "input_text"
	// partTypeOutputText is the assistant's own text replayed in a Responses
	// history. Left out, the model's restatement of a personal detail travels
	// in clear on the next turn while the user's question that prompted it was
	// pseudonymized.
	partTypeOutputText = "output_text"

	partTypeToolUse    = "tool_use"
	partTypeToolResult = "tool_result"

	// Server-side and MCP tools follow the same call/result shapes as a
	// client-side tool_use/tool_result — only the type name differs.
	partTypeServerToolUse       = "server_tool_use"
	partTypeMCPToolUse          = "mcp_tool_use"
	partTypeWebSearchToolResult = "web_search_tool_result"
	partTypeMCPToolResult       = "mcp_tool_result"

	// partTypeCodeExecutionToolResult is the legacy code execution tool's
	// result type (code_execution_20250522 and earlier). Current tool
	// versions (code_execution_20250825 and later) split it in two, neither
	// of which is named "code_execution_tool_result":
	// partTypeBashCodeExecutionToolResult covers running a command;
	// text_editor_code_execution_tool_result covers file operations (view,
	// create, str_replace) and is not handled here — its content shape
	// differs by command and none of its fields are consistently free text,
	// so isAttachmentPart returns false for it and it falls through to
	// PreRequest's catch-all default:, which forwards it untouched (scanned
	// read-only for visibility) rather than guessed at.
	partTypeCodeExecutionToolResult     = "code_execution_tool_result"
	partTypeBashCodeExecutionToolResult = "bash_code_execution_tool_result"

	partTypeThinking         = "thinking"
	partTypeRedactedThinking = "redacted_thinking"
)

// callShapedToolParts carries its payload in "input", correlated to its
// result by "id".
var callShapedToolParts = map[string]bool{
	partTypeToolUse:       true,
	partTypeServerToolUse: true,
	partTypeMCPToolUse:    true,
}

// resultShapedToolParts carries its payload in "content", correlated to its
// call by "tool_use_id", and actually gets that content rewritten.
var resultShapedToolParts = map[string]bool{
	partTypeToolResult:                  true,
	partTypeCodeExecutionToolResult:     true,
	partTypeBashCodeExecutionToolResult: true,
	partTypeMCPToolResult:               true,
}

// passthroughToolParts are agent tool blocks recognized as such — so
// isToolPart is true and they never reach the attachment path — but forwarded
// byte for byte instead of rewritten.
//
// web_search_tool_result's content is a list of `web_search_result` blocks —
// url, title, page_age, and an encrypted_content the API requires back
// unchanged on the next turn, the same provider-signature contract as
// encrypted `thinking` — or, on failure, a
// {"type":"web_search_tool_result_error",...} object. Neither shape is a
// `text` block, and the documentation asks for the assistant's content blocks
// to round-trip as a whole rather than field by field, so even `title`, which
// is free text, is not singled out for rewriting.
//
// PreRequest owns these: it intercepts them before anonymizeToolPart, which
// has no way to scan what it forwards. They are listed apart from
// resultShapedToolParts so that map's members are exactly the ones that take
// the "content" rewrite path.
var passthroughToolParts = map[string]bool{
	partTypeWebSearchToolResult: true,
}

// codeExecutionResultTypes carries {type, stdout, stderr, return_code,
// content} — or, on failure, {type, error_code} — as an object, not a list,
// under both the legacy and the current bash tool. return_code must stay a
// number and type/error_code are protocol enums, not user text; stdout and
// stderr are the only free-text fields.
var codeExecutionResultTypes = map[string]bool{
	partTypeCodeExecutionToolResult:     true,
	partTypeBashCodeExecutionToolResult: true,
}

// isTextPart reports whether a content part is a plain text block, under any
// of the spellings the routes use for it.
func isTextPart(partType string) bool {
	return partType == partTypeText || partType == partTypeInputText || partType == partTypeOutputText
}

// nonTextToolPayloadNotice replaces a tool payload the plugin cannot read.
//
// A REPLACEMENT AND NOT A REMOVAL, because a `tool_result` is half of a pair.
// Dropping it leaves the matching `tool_use` alone in the previous assistant
// message, and the Messages API answers 400 on the unpaired id — so an image
// returned by one tool would break the whole session rather than that one call.
// The notice keeps the pairing and tells the agent what happened, which is more
// useful to it than a block that silently vanished.
//
// In English because its reader is the model, not the operator.
const nonTextToolPayloadNotice = "[non-textual content removed by the pseudonymizer]"

// isToolPart reports whether a message part is an agent tool block rather than
// a document attached by a human — a client tool call/result, a server-side
// tool (web search, code execution), or an MCP tool.
//
// The distinction matters because these look alike to the attachment path —
// none carries inline file bytes — but they are not the same thing. A
// `tool_result` is text the model itself asked for, which the pseudonymizer can
// rewrite like any other message content. Treating it as an unreadable
// attachment removes it, and an agent whose read tools return nothing keeps
// working blind.
func isToolPart(partType string) bool {
	return callShapedToolParts[partType] || resultShapedToolParts[partType] || passthroughToolParts[partType]
}

// isUnrewritableThinkingPart reports whether a message part is a `thinking` or
// `redacted_thinking` block.
//
// Both are provider-signed: the model must receive the exact bytes it
// produced on a later turn, or it rejects them as tampered. `redacted_thinking`
// carries no plaintext at all — its `data` is an opaque encrypted blob. A
// `thinking` block does carry readable text, but rewriting it invalidates the
// `signature` field alongside it just as surely as touching the signature
// itself would; there is no way to pseudonymize the visible half of a
// signed pair. Such a block is passed through unmodified rather than routed to
// the attachment path, which would refuse or strip it and break the
// conversation for any client using extended thinking.
func isUnrewritableThinkingPart(partType string) bool {
	return partType == partTypeThinking || partType == partTypeRedactedThinking
}

// anonymizeToolPart returns a copy of an agent tool block with its textual
// leaves rewritten by anonymize.
//
// Nothing is ever dropped here: what cannot be read is replaced by
// nonTextToolPayloadNotice, so the block keeps its shape and its place in the
// call/result pairing.
func anonymizeToolPart(part map[string]any, anonymize func(string) (string, error)) (map[string]any, error) {
	updated := make(map[string]any, len(part))
	for k, v := range part {
		updated[k] = v
	}

	partType, _ := part["type"].(string)
	switch {
	case callShapedToolParts[partType]:
		// Only the arguments are rewritten. `id` and `name` correlate the call
		// with its result: renaming them would break the pairing the model
		// relies on to read its own history.
		input, ok := part["input"]
		if !ok {
			return updated, nil
		}
		walked, err := rewriteLeaves(input, anonymize)
		if err != nil {
			return nil, err
		}
		updated["input"] = walked
		return updated, nil

	case resultShapedToolParts[partType]:
		switch c := part["content"].(type) {
		case nil:
			return updated, nil
		case string:
			text, err := anonymize(c)
			if err != nil {
				return nil, err
			}
			updated["content"] = text
			return updated, nil
		case []any:
			out := make([]any, 0, len(c))
			for _, sub := range c {
				replaced, err := anonymizeToolResultBlock(sub, anonymize)
				if err != nil {
					return nil, err
				}
				out = append(out, replaced)
			}
			updated["content"] = out
			return updated, nil
		case map[string]any:
			if !codeExecutionResultTypes[partType] {
				// An object where the spec describes a list (a plain
				// tool_result, say). Not a shape this switch knows how to
				// read, so — same answer as the default case below — it is
				// not forwarded either.
				updated["content"] = nonTextToolPayloadNotice
				return updated, nil
			}
			// Only stdout/stderr are free text, so only those are rewritten.
			// Replacing the whole object with a string, as the default case
			// below does, would itself violate the schema.
			rewritten := make(map[string]any, len(c))
			for k, v := range c {
				rewritten[k] = v
			}
			for _, field := range []string{"stdout", "stderr"} {
				text, ok := c[field].(string)
				if !ok {
					continue
				}
				anonText, err := anonymize(text)
				if err != nil {
					return nil, err
				}
				rewritten[field] = anonText
			}
			updated["content"] = rewritten
			return updated, nil
		default:
			// A bare number, bool — not a shape the spec describes. It is not
			// read, so it is not forwarded either.
			updated["content"] = nonTextToolPayloadNotice
			return updated, nil
		}
	}

	return updated, nil
}

// anonymizeToolResultBlock handles one element of a `tool_result` content list.
//
// A bare string is not valid per the spec but costs nothing to handle, and it
// sits next to the case below: both are elements of the same list, and leaving
// one untouched while replacing the other would be two opposite answers to the
// same question.
func anonymizeToolResultBlock(sub any, anonymize func(string) (string, error)) (any, error) {
	switch block := sub.(type) {
	case string:
		return anonymize(block)
	case map[string]any:
		if subType, _ := block["type"].(string); subType != "text" {
			return textBlock(nonTextToolPayloadNotice), nil
		}
		text, _ := block["text"].(string)
		anonText, err := anonymize(text)
		if err != nil {
			return nil, err
		}
		copied := make(map[string]any, len(block))
		for k, v := range block {
			copied[k] = v
		}
		copied["text"] = anonText
		return copied, nil
	default:
		return textBlock(nonTextToolPayloadNotice), nil
	}
}

func textBlock(text string) map[string]any {
	return map[string]any{"type": "text", "text": text}
}

// rewriteLeaves walks a decoded JSON value and rewrites every string it
// contains, leaving the shape untouched.
//
// Map KEYS are left alone on purpose: they are the tool's parameter names, part
// of a schema the model and the client agreed on. Rewriting them would produce
// a call the client cannot execute.
//
// Keys are walked in sorted order. The session numbers entities in the order it
// meets them, and Go's map order changes from one run to the next: two new
// names in two fields of the same tool input could swap numbers between turns,
// which rewrites the history and breaks the upstream prompt cache (#85).
func rewriteLeaves(v any, rewrite func(string) (string, error)) (any, error) {
	switch value := v.(type) {
	case string:
		return rewrite(value)
	case map[string]any:
		out := make(map[string]any, len(value))
		for _, k := range slices.Sorted(maps.Keys(value)) {
			sub := value[k]
			walked, err := rewriteLeaves(sub, rewrite)
			if err != nil {
				return nil, err
			}
			out[k] = walked
		}
		return out, nil
	case []any:
		out := make([]any, 0, len(value))
		for _, sub := range value {
			walked, err := rewriteLeaves(sub, rewrite)
			if err != nil {
				return nil, err
			}
			out = append(out, walked)
		}
		return out, nil
	default:
		return v, nil
	}
}

// keepDetectedTypes drops from a Detect result the entity types the operator
// disabled through `skip_types`.
//
// Detect does not do this itself: it returns the recognizer's raw output,
// where Anonymize first filters on the EntityTypes the node fills in from
// `skip_types`. Without the same filter on the read-only path, a disabled type
// would be absent from `types` and present in `leak_types`, and the two
// counters — which the event presents side by side — would be measuring
// different sets.
func keepDetectedTypes(entities []ner.Entity, skipTypes []string) []ner.Entity {
	if len(skipTypes) == 0 || len(entities) == 0 {
		return entities
	}
	skipped := make(map[string]bool, len(skipTypes))
	for _, t := range skipTypes {
		skipped[t] = true
	}
	kept := make([]ner.Entity, 0, len(entities))
	for _, e := range entities {
		if !skipped[string(e.Type)] {
			kept = append(kept, e)
		}
	}
	return kept
}

// opaqueLeafKeys are the field names that never hold free text: an encrypted
// blob, a signature, a protocol enum, an identifier. Running the recognizer
// over them finds nothing, and an agentic conversation re-scans its whole
// history every turn, so a web search result's several kilobytes of
// `encrypted_content` would be read again on every request for no answer.
//
// `url` and `title` are deliberately absent: a URL path can carry a name and a
// title routinely does. The skip applies to string values only, so an object
// that happens to sit under one of these names is still walked.
var opaqueLeafKeys = map[string]bool{
	"encrypted_content": true,
	"data":              true,
	"signature":         true,
	"type":              true,
	"id":                true,
	"tool_use_id":       true,
	"file_id":           true,
}

// detectLeaves walks a decoded JSON value read-only, running detect over
// every string leaf and folding what it finds into counts, without rewriting
// anything. Counting happens in detect, once per distinct value, so there is
// nothing to total here.
//
// The counterpart to anonymizeLeaves for content this plugin forwards
// unpseudonymized on purpose (a thinking block, an unrecognized part type):
// there is no mapping to build and nothing to reuse a placeholder for, only
// a leak an operator should not have to infer from a debug log.
func detectLeaves(v any, detect func(string) ([]ner.Entity, error)) error {
	switch value := v.(type) {
	case string:
		if value == "" {
			return nil
		}
		_, err := detect(value)
		return err
	case map[string]any:
		for key, sub := range value {
			// Only a string leaf is skipped. `data` and `id` are generic
			// enough that an object can sit under them, and skipping the whole
			// subtree on the name alone would blind the scan exactly where the
			// catch-all needs it: an unrecognized block type is where free
			// text hides under a name nobody has classified yet.
			if _, isString := sub.(string); isString && opaqueLeafKeys[key] {
				continue
			}
			if err := detectLeaves(sub, detect); err != nil {
				return err
			}
		}
		return nil
	case []any:
		for _, sub := range value {
			if err := detectLeaves(sub, detect); err != nil {
				return err
			}
		}
		return nil
	default:
		return nil
	}
}

// fieldToolCalls is where an OpenAI-compatible client puts the assistant's tool
// calls: a sibling of `content`, not one of its parts.
const fieldToolCalls = "tool_calls"

// anonymizeToolCalls rewrites the arguments of OpenAI-shaped tool calls,
// returning the rewritten list and whether anything was there to rewrite.
//
// The Anthropic equivalent (`tool_use.input`) is a JSON object inside the
// message content, which the part loop reaches. `tool_calls` sits outside
// `content` entirely and would otherwise travel in clear — the same data, one
// shape protected and the other not.
//
// `id`, `type` and the function `name` are left alone for the same reason as in
// a `tool_use` block: they pair the call with its result.
func anonymizeToolCalls(raw any, anonymize func(string) (string, error)) ([]any, bool, error) {
	calls, ok := raw.([]any)
	if !ok || len(calls) == 0 {
		return nil, false, nil
	}

	out := make([]any, 0, len(calls))
	for _, call := range calls {
		callMap, ok := call.(map[string]any)
		if !ok {
			out = append(out, call)
			continue
		}
		fn, ok := callMap["function"].(map[string]any)
		if !ok {
			out = append(out, call)
			continue
		}
		rewritten, err := anonymizeArgumentsValue(fn["arguments"], anonymize)
		if err != nil {
			// The calls already rewritten are handed back with the error: their
			// values are in the session mapping either way, so dropping them
			// here would send them in clear under a placeholder the model is
			// told to reuse.
			out = append(out, calls[len(out):]...)
			return out, true, err
		}
		if rewritten == nil {
			out = append(out, call)
			continue
		}

		copiedFn := make(map[string]any, len(fn))
		for k, v := range fn {
			copiedFn[k] = v
		}
		copiedFn["arguments"] = rewritten

		copiedCall := make(map[string]any, len(callMap))
		for k, v := range callMap {
			copiedCall[k] = v
		}
		copiedCall["function"] = copiedFn
		out = append(out, copiedCall)
	}

	return out, true, nil
}

// rewriteToolArguments rewrites the string leaves of a tool call's `arguments`,
// which is a JSON document carried as a string.
//
// Both directions go through here. Pseudonymizing on the way to the provider
// and restoring on the way back are the same walk with a different rewrite, and
// they were written twice before, which is how one of them ended up keeping a
// precision fix the other had not received yet.
//
// A payload that does not parse is handed to rewrite as the plain text it then
// is, rather than forwarded untouched.
func rewriteToolArguments(args string, rewrite func(string) (string, error)) (string, error) {
	var decoded any
	dec := json.NewDecoder(strings.NewReader(args))
	// Without this, every number becomes a float64 and is re-encoded from it.
	// A 19-digit identifier or a nanosecond timestamp does not survive that
	// trip: 9223372036854775807 comes back as 9223372036854776000, and the tool
	// runs against something the model never asked for. json.Number keeps the
	// literal as it was written, and being a named type it falls through the
	// `case string` of rewriteLeaves untouched.
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		return rewrite(args)
	}

	walked, err := rewriteLeaves(decoded, rewrite)
	if err != nil {
		return "", err
	}

	return encodeToolJSON(walked)
}

// encodeToolJSON marshals a value without escaping HTML characters: the result
// is read by a tool, not by a browser, and turning `<`, `>` or `&` into an
// escape would alter a payload the client has to execute verbatim.
func encodeToolJSON(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

// anonymizeArgumentsValue rewrites the `arguments` of one call, whatever shape
// it arrived in, and returns nil when there is nothing to rewrite.
//
// The spec says a string, and that is what the field holds in practice. An SDK
// that pre-parses it into an object is the other shape seen in the wild, and
// forwarding that one untouched would be the very leak this file exists to
// close, so it is walked in place instead.
func anonymizeArgumentsValue(raw any, anonymize func(string) (string, error)) (any, error) {
	switch args := raw.(type) {
	case nil:
		return nil, nil
	case string:
		if args == "" {
			return nil, nil
		}
		return rewriteToolArguments(args, anonymize)
	default:
		return rewriteLeaves(args, anonymize)
	}
}

// leakKey identifies a detected entity for deduplication, without keeping the
// value itself.
//
// The surface form is normalized the way go-anon normalizes it before reusing
// a placeholder — trimmed and lowercased — so that a leak and a pseudonymized
// entity count as one value on both sides of the event: "Marc Durand" and
// "marc durand" are the same leak, as they would be the same mapping entry.
//
// The form is then hashed. Deduplicating needs equality, not the text, and a
// map of detected personal data held for the life of the request is a thing to
// avoid when nothing requires it.
func leakKey(e ner.Entity) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(e.Text))))
	return string(e.Type) + "\x00" + hex.EncodeToString(sum[:8])
}

// leakTypesOf counts the distinct leaked values per entity type.
//
// Derived from the same set the total is derived from, so the two can never
// disagree: reporting one leaked value next to a type count of five was the
// contradiction this replaces.
func leakTypesOf(values map[string]bool) map[string]int {
	counts := make(map[string]int, len(values))
	for key := range values {
		entityType, _, found := strings.Cut(key, "\x00")
		if !found {
			continue
		}
		counts[entityType]++
	}
	return counts
}
