// Copyright The Xolo Authors
// SPDX-License-Identifier: Apache-2.0

package pluginsdk

import (
	"encoding/json"

	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// The host sends the request body once per call: the messages and the input
// ports that only repeat it are left out of the message. The helpers below
// restore them, so a plugin never reads PreRequestInput.MessagesJson,
// PreRequestInput.InputsJson or ResolveModelInput.MessagesJson directly.

// PreRequestMessages returns the JSON array of the messages entering the
// node: MessagesJson when an upstream node rewrote them, the messages of the
// request body otherwise.
func PreRequestMessages(in *proto.PreRequestInput) string {
	if messages := in.GetMessagesJson(); messages != "" {
		return messages
	}
	return BodyMessages(in.GetModel())
}

// ResolveModelMessages returns the JSON array of the messages entering the
// node: MessagesJson when an upstream node rewrote them, the messages of the
// request body otherwise.
func ResolveModelMessages(in *proto.ResolveModelInput) string {
	if messages := in.GetMessagesJson(); messages != "" {
		return messages
	}
	return BodyMessages(in.GetBodyJson())
}

// PreRequestInputs returns the values of the node's connected input ports,
// keyed by port name, the ports left out of InputsJson included. It returns
// an empty map when no port is connected.
func PreRequestInputs(in *proto.PreRequestInput) (map[string]any, error) {
	inputs := map[string]any{}
	if raw := in.GetInputsJson(); raw != "" {
		if err := json.Unmarshal([]byte(raw), &inputs); err != nil {
			return nil, err
		}
		if inputs == nil {
			inputs = map[string]any{}
		}
	}
	for _, port := range in.GetRequestInputPorts() {
		inputs[port] = in.GetModel()
	}
	if ports := in.GetMessagesInputPorts(); len(ports) > 0 {
		messages := PreRequestMessages(in)
		for _, port := range ports {
			inputs[port] = messages
		}
	}
	return inputs, nil
}

// BodyMessages returns the "messages" array of a raw LLM request body,
// verbatim, or "[]" when the body is empty, invalid or holds no messages.
func BodyMessages(body string) string {
	if body == "" {
		return "[]"
	}
	var envelope struct {
		Messages json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil || envelope.Messages == nil {
		return "[]"
	}
	return string(envelope.Messages)
}
