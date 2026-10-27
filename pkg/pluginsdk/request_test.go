// Copyright The Xolo Authors
// SPDX-License-Identifier: Apache-2.0

package pluginsdk_test

import (
	"testing"

	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

const body = `{"model":"m","messages":[{"role":"user","content":"hello"}]}`

func TestBodyMessages(t *testing.T) {
	cases := map[string]string{
		body:            `[{"role":"user","content":"hello"}]`,
		"":              "[]",
		"not json":      "[]",
		`{"model":"m"}`: "[]",
	}
	for in, want := range cases {
		if got := pluginsdk.BodyMessages(in); got != want {
			t.Errorf("BodyMessages(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPreRequestMessages(t *testing.T) {
	in := &proto.PreRequestInput{Model: body}
	if got, want := pluginsdk.PreRequestMessages(in), pluginsdk.BodyMessages(body); got != want {
		t.Errorf("left out: got %q, want the body's %q", got, want)
	}

	in.MessagesJson = `[{"role":"user","content":"bonjour"}]`
	if got := pluginsdk.PreRequestMessages(in); got != in.MessagesJson {
		t.Errorf("rewritten: got %q, want %q", got, in.MessagesJson)
	}
}

func TestResolveModelMessages(t *testing.T) {
	in := &proto.ResolveModelInput{BodyJson: body}
	if got, want := pluginsdk.ResolveModelMessages(in), pluginsdk.BodyMessages(body); got != want {
		t.Errorf("left out: got %q, want the body's %q", got, want)
	}
}

func TestPreRequestInputs(t *testing.T) {
	in := &proto.PreRequestInput{
		Model:              body,
		InputsJson:         `{"score":0.5}`,
		RequestInputPorts:  []string{"request"},
		MessagesInputPorts: []string{"messages_json"},
	}
	inputs, err := pluginsdk.PreRequestInputs(in)
	if err != nil {
		t.Fatal(err)
	}
	if inputs["score"] != 0.5 {
		t.Errorf("score = %v, want 0.5", inputs["score"])
	}
	if inputs["request"] != body {
		t.Errorf("request = %v, want the body", inputs["request"])
	}
	if inputs["messages_json"] != pluginsdk.BodyMessages(body) {
		t.Errorf("messages_json = %v, want the body's messages", inputs["messages_json"])
	}

	empty, err := pluginsdk.PreRequestInputs(&proto.PreRequestInput{})
	if err != nil || empty == nil || len(empty) != 0 {
		t.Errorf("no input: got %v, %v, want an empty map", empty, err)
	}

	if _, err := pluginsdk.PreRequestInputs(&proto.PreRequestInput{InputsJson: "{"}); err == nil {
		t.Error("invalid inputs_json: want an error")
	}
}
