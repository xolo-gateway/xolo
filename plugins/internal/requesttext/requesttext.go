// Package requesttext extracts the textual content of an OpenAI-compatible
// chat request so that evaluation plugins can score, classify or measure it
// without re-implementing the messages schema.
package requesttext

import "encoding/json"

type message struct {
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	ToolCalls []struct {
		Function struct {
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func parse(messagesJSON string) ([]message, bool) {
	if messagesJSON == "" {
		return nil, false
	}
	var messages []message
	if err := json.Unmarshal([]byte(messagesJSON), &messages); err != nil {
		return nil, false
	}
	return messages, true
}

// textOf returns the textual parts of a message content, whether the content
// is a plain string or an array of typed parts.
func textOf(content json.RawMessage) string {
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return s
	}
	var parts []contentPart
	if err := json.Unmarshal(content, &parts); err != nil {
		return ""
	}
	var out []byte
	for _, p := range parts {
		if p.Type == "text" {
			out = append(out, p.Text...)
			out = append(out, ' ')
		}
	}
	return string(out)
}

// Prompt returns the text written by the requester: system and user messages
// only. Assistant turns and tool results are excluded so that the request is
// evaluated on what the user asked, not on what the model already produced.
func Prompt(messagesJSON string) string {
	messages, ok := parse(messagesJSON)
	if !ok {
		return messagesJSON
	}
	var out []byte
	for _, m := range messages {
		if m.Role != "system" && m.Role != "user" {
			continue
		}
		out = append(out, textOf(m.Content)...)
		out = append(out, ' ')
	}
	return string(out)
}

// LastUserTurn returns the text of the most recent user message, which is
// what the requester is asking right now. On a conversation without user
// message it falls back to Prompt.
func LastUserTurn(messagesJSON string) string {
	messages, ok := parse(messagesJSON)
	if !ok {
		return messagesJSON
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return textOf(messages[i].Content)
		}
	}
	return Prompt(messagesJSON)
}

// Context returns everything the model will have to read: system, user and
// tool messages plus the arguments of assistant tool calls. It approximates
// the prompt size seen by the provider.
func Context(messagesJSON string) string {
	messages, ok := parse(messagesJSON)
	if !ok {
		return messagesJSON
	}
	var out []byte
	for _, m := range messages {
		switch m.Role {
		case "system", "user", "tool":
			out = append(out, textOf(m.Content)...)
			out = append(out, ' ')
		case "assistant":
			for _, tc := range m.ToolCalls {
				if tc.Function.Arguments != "" {
					out = append(out, tc.Function.Arguments...)
					out = append(out, ' ')
				}
			}
		}
	}
	return string(out)
}

// HasImage reports whether any message carries an image part.
func HasImage(messagesJSON string) bool {
	messages, ok := parse(messagesJSON)
	if !ok {
		return false
	}
	for _, m := range messages {
		var parts []contentPart
		if err := json.Unmarshal(m.Content, &parts); err != nil {
			continue
		}
		for _, p := range parts {
			if p.Type == "image_url" || p.Type == "image" {
				return true
			}
		}
	}
	return false
}

// EstimateTokens approximates the number of tokens of a text. It is a
// tokenizer-free heuristic (roughly four characters per token for Latin
// scripts) meant for routing decisions, not for billing.
func EstimateTokens(text string) int {
	n := len(text)
	if n == 0 {
		return 0
	}
	return (n + 3) / 4
}
