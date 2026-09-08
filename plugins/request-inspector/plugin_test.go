package main

import (
	"context"
	"encoding/json"
	"testing"

	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

func run(t *testing.T, body, messages string) map[string]any {
	t.Helper()
	out, err := (&Plugin{}).PreRequest(context.Background(), &proto.PreRequestInput{Model: body, MessagesJson: messages})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out.OutputsJson), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPreRequest_PlainText(t *testing.T) {
	m := run(t, `{"model":"x","messages":[]}`, `[{"role":"user","content":"Bonjour, ça va ?"}]`)
	if m["has_vision"] != false || m["has_reasoning"] != false || m["has_tools"] != false || m["is_streaming"] != false {
		t.Errorf("unexpected flags: %v", m)
	}
	if m["message_count"] != 1.0 {
		t.Errorf("expected 1 message, got %v", m["message_count"])
	}
	if m["input_tokens"].(float64) <= 0 {
		t.Errorf("expected a positive token estimate, got %v", m["input_tokens"])
	}
	if m["max_tokens"] != 0.0 {
		t.Errorf("expected max_tokens 0, got %v", m["max_tokens"])
	}
}

func TestPreRequest_FeaturesDetected(t *testing.T) {
	body := `{"stream":true,"reasoning_effort":"high","max_completion_tokens":2048,"tools":[{"type":"function"}]}`
	messages := `[{"role":"user","content":[{"type":"text","text":"Regarde"},{"type":"image_url","image_url":{"url":"http://x"}}]}]`
	m := run(t, body, messages)
	if m["has_vision"] != true || m["has_reasoning"] != true || m["has_tools"] != true || m["is_streaming"] != true {
		t.Errorf("unexpected flags: %v", m)
	}
	if m["max_tokens"] != 2048.0 {
		t.Errorf("expected max_tokens 2048, got %v", m["max_tokens"])
	}
}

func TestHasReasoning_Conventions(t *testing.T) {
	cases := map[string]bool{
		`{"thinking":{"type":"enabled"}}`:  true,
		`{"thinking":{"type":"disabled"}}`: false,
		`{"enable_thinking":true}`:         true,
		`{"reasoning_effort":"none"}`:      false,
		`{"reasoning_effort":"low"}`:       true,
		`{}`:                               false,
	}
	for body, want := range cases {
		if got := parseBody(body).hasReasoning(); got != want {
			t.Errorf("%s: expected %v, got %v", body, want, got)
		}
	}
}
