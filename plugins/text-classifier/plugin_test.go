package main

import (
	"context"
	"encoding/json"
	"testing"

	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

func TestPreRequest_ClassifiesAndLoadsModelOnce(t *testing.T) {
	p := &Plugin{}
	for i := 0; i < 2; i++ {
		out, err := p.PreRequest(context.Background(), &proto.PreRequestInput{
			MessagesJson: `[{"role":"user","content":"Write a Go function that reverses a slice"}]`,
		})
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(out.OutputsJson), &m); err != nil {
			t.Fatal(err)
		}
		if m["category"] == "unknown" || m["category"] == "" {
			t.Errorf("expected a category, got %v", m)
		}
		if c := m["confidence"].(float64); c <= 0 || c > 1 {
			t.Errorf("expected confidence in (0,1], got %v", c)
		}
	}
	if p.model == nil || p.loadErr != nil {
		t.Fatalf("model should be loaded once without error: %v", p.loadErr)
	}
}

func TestPreRequest_EmptyPromptIsUnknown(t *testing.T) {
	out, err := (&Plugin{}).PreRequest(context.Background(), &proto.PreRequestInput{MessagesJson: `[]`})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(out.OutputsJson), &m)
	if m["category"] != "unknown" || m["confidence"] != 0.0 {
		t.Errorf("expected unknown/0, got %v", m)
	}
}
