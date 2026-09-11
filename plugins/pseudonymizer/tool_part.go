package main

const (
	partTypeToolUse    = "tool_use"
	partTypeToolResult = "tool_result"
)

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
// The second return value reports whether the block could be handled as text at
// all. It is false when the block carries a non-textual payload — an image
// returned by a screenshot tool, say. Such a block keeps the attachment policy:
// the plugin cannot vouch for what it contains, and this function does not
// pretend otherwise.
func anonymizeToolPart(part map[string]any, anonymize func(string) (string, error)) (map[string]any, bool, error) {
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
			return updated, true, nil
		}
		walked, err := anonymizeLeaves(input, anonymize)
		if err != nil {
			return nil, true, err
		}
		updated["input"] = walked
		return updated, true, nil

	case partTypeToolResult:
		switch c := part["content"].(type) {
		case nil:
			return updated, true, nil
		case string:
			text, err := anonymize(c)
			if err != nil {
				return nil, true, err
			}
			updated["content"] = text
			return updated, true, nil
		case []any:
			out := make([]any, 0, len(c))
			for _, sub := range c {
				subMap, ok := sub.(map[string]any)
				if !ok {
					out = append(out, sub)
					continue
				}
				if subType, _ := subMap["type"].(string); subType != "text" {
					return nil, false, nil
				}
				text, _ := subMap["text"].(string)
				anonText, err := anonymize(text)
				if err != nil {
					return nil, true, err
				}
				copied := make(map[string]any, len(subMap))
				for k, v := range subMap {
					copied[k] = v
				}
				copied["text"] = anonText
				out = append(out, copied)
			}
			updated["content"] = out
			return updated, true, nil
		default:
			return nil, false, nil
		}
	}

	return nil, false, nil
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
