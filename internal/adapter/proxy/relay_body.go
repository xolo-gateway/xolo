package proxy

import (
	"bytes"
	"encoding/json"

	"github.com/pkg/errors"
)

// withPipelineMessages returns a Messages request body carrying the
// conversation a pipeline rewrote, for a request relayed as is.
//
// The system messages a node put in front of the conversation — the
// pseudonymizer's placeholder instruction, the system-prompt plugin — are
// moved to the top-level system field, after the client's own blocks: the
// Messages API takes system instructions there only, and the translated path
// hoists them the same way. Leading system messages the client sent itself
// stay where they are, as it sent them.
func withPipelineMessages(body []byte, originalMessagesJSON, finalMessagesJSON string) ([]byte, error) {
	var final []json.RawMessage
	if err := json.Unmarshal([]byte(finalMessagesJSON), &final); err != nil {
		return nil, errors.Wrap(err, "pipeline messages are not a JSON array")
	}
	var original []json.RawMessage
	_ = json.Unmarshal([]byte(originalMessagesJSON), &original)

	injected := max(leadingSystemMessages(final)-leadingSystemMessages(original), 0)
	fields := map[string]any{"messages": final[injected:]}

	if injected > 0 {
		var decoded struct {
			System json.RawMessage `json:"system"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil {
			return nil, errors.Wrap(err, "request body is not a JSON object")
		}
		system, err := systemBlocks(decoded.System)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		for _, raw := range final[:injected] {
			var msg struct {
				Content json.RawMessage `json:"content"`
			}
			if err := json.Unmarshal(raw, &msg); err != nil {
				return nil, errors.WithStack(err)
			}
			blocks, err := systemBlocks(msg.Content)
			if err != nil {
				return nil, errors.WithStack(err)
			}
			system = append(system, blocks...)
		}
		fields["system"] = system
	}

	return setBodyFields(body, fields)
}

// leadingSystemMessages counts the system messages a conversation starts with.
func leadingSystemMessages(messages []json.RawMessage) int {
	for i, raw := range messages {
		var msg struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(raw, &msg) != nil || msg.Role != "system" {
			return i
		}
	}
	return len(messages)
}

// systemBlocks reads a system prompt or a message content, a string or an
// array of blocks, as an array of blocks.
func systemBlocks(raw json.RawMessage) ([]json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if text == "" {
			return nil, nil
		}
		block, err := encodeJSON(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{"text", text})
		return []json.RawMessage{block}, errors.WithStack(err)
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, errors.Wrap(err, "system content is neither a string nor an array of blocks")
	}
	return blocks, nil
}

// setBodyFields sets top-level fields of a JSON request body and leaves the
// others as they were. HTML escaping is off so that the conversation keeps its bytes: a relayed
// request is meant to reach the upstream as sent.
func setBodyFields(body []byte, fields map[string]any) ([]byte, error) {
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, errors.Wrap(err, "request body is not a JSON object")
	}
	for key, value := range fields {
		encoded, err := encodeJSON(value)
		if err != nil {
			return nil, errors.Wrapf(err, "could not encode field %q", key)
		}
		decoded[key] = encoded
	}
	return encodeJSON(decoded)
}

// encodeJSON is json.Marshal without HTML escaping, which json.Marshal also
// applies inside json.RawMessage values.
func encodeJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		return nil, errors.WithStack(err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
