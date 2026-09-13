package main

import (
	"bytes"
	"encoding/json"
	"strings"
)

const (
	partTypeToolUse    = "tool_use"
	partTypeToolResult = "tool_result"
)

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
// a document attached by a human.
//
// The distinction matters because the two look alike to the attachment path —
// neither carries inline file bytes — but they are not the same thing. A
// `tool_result` is text the model itself asked for, which the pseudonymizer can
// rewrite like any other message content. Treating it as an unreadable
// attachment removes it, and an agent whose read tools return nothing keeps
// working blind.
func isToolPart(partType string) bool {
	return partType == partTypeToolUse || partType == partTypeToolResult
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
	switch partType {
	case partTypeToolUse:
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

	case partTypeToolResult:
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
		default:
			// An object, a number — not a shape the spec describes. It is not
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
func rewriteLeaves(v any, rewrite func(string) (string, error)) (any, error) {
	switch value := v.(type) {
	case string:
		return rewrite(value)
	case map[string]any:
		out := make(map[string]any, len(value))
		for k, sub := range value {
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
