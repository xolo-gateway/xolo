package pipelinetest

import (
	"context"
	"encoding/json"

	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// PreRequestDescriptor returns a PluginDescriptor with the PRE_REQUEST capability only.
func PreRequestDescriptor(name string) *proto.PluginDescriptor {
	return &proto.PluginDescriptor{
		Name:         name,
		Capabilities: []proto.PluginDescriptor_Capability{proto.PluginDescriptor_PRE_REQUEST},
	}
}

// ToolProviderDescriptor returns a PluginDescriptor with the TOOL_PROVIDER capability only.
func ToolProviderDescriptor(name string) *proto.PluginDescriptor {
	return &proto.PluginDescriptor{
		Name:         name,
		Capabilities: []proto.PluginDescriptor_Capability{proto.PluginDescriptor_TOOL_PROVIDER},
	}
}

// PostOnlyDescriptor returns a PluginDescriptor with the POST_RESPONSE capability only.
// Useful for tests that exercise the post-response pass in isolation, without a
// matching PRE_REQUEST plugin in the same graph.
func PostOnlyDescriptor(name string) *proto.PluginDescriptor {
	return &proto.PluginDescriptor{
		Name:         name,
		Capabilities: []proto.PluginDescriptor_Capability{proto.PluginDescriptor_POST_RESPONSE},
	}
}

// PrePostDescriptor returns a PluginDescriptor with PRE_REQUEST and POST_RESPONSE capabilities.
func PrePostDescriptor(name string) *proto.PluginDescriptor {
	return &proto.PluginDescriptor{
		Name: name,
		Capabilities: []proto.PluginDescriptor_Capability{
			proto.PluginDescriptor_PRE_REQUEST,
			proto.PluginDescriptor_POST_RESPONSE,
		},
	}
}

// JSONPreRequest builds a PluginClient whose PreRequest restores its inputs
// (pluginsdk.PreRequestInputs) into a map, passes it to fn, and marshals the returned map as OutputsJson.
func JSONPreRequest(fn func(inputs map[string]any) map[string]any) *PluginClient {
	return &PluginClient{
		PreRequestFunc: func(_ context.Context, in *proto.PreRequestInput) (*proto.PreRequestOutput, error) {
			inputs, _ := pluginsdk.PreRequestInputs(in)

			outputs := fn(inputs)

			outputsJSON, err := json.Marshal(outputs)
			if err != nil {
				return nil, err
			}

			return &proto.PreRequestOutput{Allowed: true, OutputsJson: string(outputsJSON)}, nil
		},
	}
}

// JSONPreRequestWithState is like JSONPreRequest but also lets fn return an
// opaque NodeState forwarded to the backward pass.
func JSONPreRequestWithState(fn func(inputs map[string]any) (outputs map[string]any, state []byte)) *PluginClient {
	return &PluginClient{
		PreRequestFunc: func(_ context.Context, in *proto.PreRequestInput) (*proto.PreRequestOutput, error) {
			inputs, _ := pluginsdk.PreRequestInputs(in)

			outputs, state := fn(inputs)

			var outputsJSON string
			if outputs != nil {
				b, err := json.Marshal(outputs)
				if err != nil {
					return nil, err
				}
				outputsJSON = string(b)
			}

			return &proto.PreRequestOutput{Allowed: true, OutputsJson: outputsJSON, NodeState: state}, nil
		},
	}
}

// ModifiedMessages builds a PluginClient whose PreRequest replaces the
// request messages with messagesJSON.
func ModifiedMessages(messagesJSON string) *PluginClient {
	return &PluginClient{
		PreRequestFunc: func(_ context.Context, _ *proto.PreRequestInput) (*proto.PreRequestOutput, error) {
			return &proto.PreRequestOutput{Allowed: true, ModifiedMessagesJson: messagesJSON}, nil
		},
	}
}

// RejectPreRequest builds a PluginClient whose PreRequest blocks the request
// with the given reason (mimicking e.g. the time-restriction plugin).
func RejectPreRequest(reason string) *PluginClient {
	return &PluginClient{
		PreRequestFunc: func(_ context.Context, _ *proto.PreRequestInput) (*proto.PreRequestOutput, error) {
			return &proto.PreRequestOutput{Allowed: false, RejectionReason: reason}, nil
		},
	}
}

// PostResponseRewrite builds a PostResponseFunc that rewrites the response
// content based on the node's opaque state.
func PostResponseRewrite(fn func(state []byte, content string) string) func(ctx context.Context, in *proto.PostResponseInput) (*proto.PostResponseOutput, error) {
	return func(_ context.Context, in *proto.PostResponseInput) (*proto.PostResponseOutput, error) {
		return &proto.PostResponseOutput{ModifiedResponseContent: fn(in.GetNodeState(), in.GetResponseContent())}, nil
	}
}
