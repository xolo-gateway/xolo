package main

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
		walked, err := anonymizeLeaves(input, anonymize)
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

// anonymizeLeaves walks a decoded JSON value and rewrites every string it
// contains, leaving the shape untouched.
//
// Map KEYS are left alone on purpose: they are the tool's parameter names, part
// of a schema the model and the client agreed on. Rewriting them would produce
// a call the client cannot execute.
func anonymizeLeaves(v any, anonymize func(string) (string, error)) (any, error) {
	switch value := v.(type) {
	case string:
		return anonymize(value)
	case map[string]any:
		out := make(map[string]any, len(value))
		for k, sub := range value {
			walked, err := anonymizeLeaves(sub, anonymize)
			if err != nil {
				return nil, err
			}
			out[k] = walked
		}
		return out, nil
	case []any:
		out := make([]any, 0, len(value))
		for _, sub := range value {
			walked, err := anonymizeLeaves(sub, anonymize)
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
