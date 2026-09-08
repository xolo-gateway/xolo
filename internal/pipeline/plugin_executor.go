package pipeline

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/bornholm/genai/llm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
	"github.com/pkg/errors"
)

// PluginProvider resolves plugin gRPC clients dynamically, restarting dead processes as needed.
type PluginProvider interface {
	GetOrRestart(ctx context.Context, name string) (proto.XoloPluginClient, *proto.PluginDescriptor, bool)
}

// PluginExecutor handles NodeTypePlugin nodes by delegating to the gRPC plugin.
type PluginExecutor struct {
	provider PluginProvider
}

// NewPluginExecutor creates a PluginExecutor backed by a dynamic plugin provider.
func NewPluginExecutor(provider PluginProvider) *PluginExecutor {
	return &PluginExecutor{provider: provider}
}

func (e *PluginExecutor) Forward(ctx context.Context, node model.PipelineNode, inputs map[string]interface{}, ec ExecutionContext) (*ForwardResult, error) {
	data, err := parsePluginNodeData(node)
	if err != nil {
		return nil, errors.Wrap(err, "invalid plugin node data")
	}

	client, desc, ok := e.provider.GetOrRestart(ctx, data.PluginName)
	if !ok {
		return nil, errors.Errorf("plugin %q is not loaded", data.PluginName)
	}

	configJSON := "{}"
	if data.Config != nil {
		configJSON = string(data.Config)
	}

	reqCtx := &proto.RequestContext{
		OrgId:       ec.OrgID,
		UserId:      ec.UserID,
		TokenId:     ec.TokenID,
		DisplayName: ec.DisplayName,
		ConfigJson:  configJSON,
		NodeId:      node.ID,
	}

	inputsJSON := InputsJSON(inputs)

	// Dispatch based on capability.
	if hasCapability(desc, proto.PluginDescriptor_PRE_REQUEST) {
		return e.forwardPreRequest(ctx, client, reqCtx, node, inputs, inputsJSON, ec)
	}
	if hasCapability(desc, proto.PluginDescriptor_RESOLVE_MODEL) {
		return e.forwardResolveModel(ctx, client, reqCtx, node, inputs, inputsJSON, ec)
	}
	if hasCapability(desc, proto.PluginDescriptor_TOOL_PROVIDER) {
		return e.forwardToolProvider(ctx, client, reqCtx, node, inputs)
	}

	// No relevant capability: pass through.
	return &ForwardResult{OutputValues: inputs}, nil
}

// forwardToolProvider handles nodes whose plugin declares the TOOL_PROVIDER
// capability: it lists the tools the plugin exposes, wraps each as an
// llm.Tool backed by a CallTool gRPC call, and returns a ClientDecorator
// that drives the multi-turn tool-resolution loop once the model node has
// resolved a real llm.Client. The plugin controls maxConsecutiveToolCalls
// via its own ListTools response, since that setting is exposed through the
// plugin's own configuration UI rather than a generic node-level field.
func (e *PluginExecutor) forwardToolProvider(
	ctx context.Context,
	client proto.XoloPluginClient,
	reqCtx *proto.RequestContext,
	node model.PipelineNode,
	inputs map[string]interface{},
) (*ForwardResult, error) {
	out, err := client.ListTools(ctx, &proto.ListToolsInput{Ctx: reqCtx})
	if err != nil {
		return nil, errors.Wrap(err, "plugin ListTools failed")
	}

	tools := make([]llm.Tool, 0, len(out.Tools))
	for _, td := range out.Tools {
		tools = append(tools, newGRPCTool(client, reqCtx, td))
	}

	maxConsecutiveToolCalls := int(out.MaxConsecutiveToolCalls)

	return &ForwardResult{
		OutputValues: inputs,
		Tools:        tools,
		ClientDecorator: func(inner llm.Client) llm.Client {
			return NewToolLoopClient(inner, tools, 0, maxConsecutiveToolCalls)
		},
	}, nil
}

// newGRPCTool wraps a single plugin-advertised tool as an llm.Tool whose
// Execute calls back into the plugin via gRPC CallTool.
func newGRPCTool(client proto.XoloPluginClient, reqCtx *proto.RequestContext, td *proto.ToolDescriptor) llm.Tool {
	var schema map[string]any
	if td.InputSchemaJson != "" {
		_ = json.Unmarshal([]byte(td.InputSchemaJson), &schema)
	}
	return llm.NewFuncTool(td.Name, td.Description, schema, func(ctx context.Context, params map[string]any) (llm.ToolResult, error) {
		argsJSON, err := json.Marshal(params)
		if err != nil {
			return nil, errors.Wrap(err, "marshal tool call arguments")
		}
		out, err := client.CallTool(ctx, &proto.CallToolInput{
			Ctx:           reqCtx,
			Name:          td.Name,
			ArgumentsJson: string(argsJSON),
		})
		if err != nil {
			return nil, errors.Wrap(err, "plugin CallTool failed")
		}
		if out.IsError {
			return nil, errors.New(out.ResultText)
		}
		return llm.NewToolResult(out.ResultText), nil
	})
}

func (e *PluginExecutor) forwardPreRequest(
	ctx context.Context,
	client proto.XoloPluginClient,
	reqCtx *proto.RequestContext,
	node model.PipelineNode,
	inputs map[string]interface{},
	inputsJSON string,
	ec ExecutionContext,
) (*ForwardResult, error) {
	messagesJSON, _ := inputs["messages_json"].(string)
	if messagesJSON == "" {
		messagesJSON = ec.MessagesJSON
	}

	out, err := client.PreRequest(ctx, &proto.PreRequestInput{
		Ctx:          reqCtx,
		Model:        ec.RequestJSON,
		MessagesJson: messagesJSON,
		InputsJson:   inputsJSON,
		Quota:        ec.quotaInfo(ctx),
	})
	if err != nil {
		return nil, errors.Wrap(err, "plugin PreRequest failed")
	}

	if !out.Allowed {
		return &ForwardResult{Rejected: true, RejectionReason: out.RejectionReason}, nil
	}

	result := &ForwardResult{
		NodeState:         out.NodeState,
		OutputValues:      ParseOutputsJSON(out.OutputsJson),
		NoResponseRewrite: out.NoResponseRewrite,
	}
	if result.OutputValues == nil {
		result.OutputValues = make(map[string]interface{})
	}

	// Pass the (possibly modified) request through.
	if out.ModifiedMessagesJson != "" {
		result.OutputValues["messages_json"] = out.ModifiedMessagesJson
	}

	return result, nil
}

func (e *PluginExecutor) forwardResolveModel(
	ctx context.Context,
	client proto.XoloPluginClient,
	reqCtx *proto.RequestContext,
	node model.PipelineNode,
	inputs map[string]interface{},
	inputsJSON string,
	ec ExecutionContext,
) (*ForwardResult, error) {
	messagesJSON, _ := inputs["messages_json"].(string)
	if messagesJSON == "" {
		messagesJSON = ec.MessagesJSON
	}

	out, err := client.ResolveModel(ctx, &proto.ResolveModelInput{
		Ctx:             reqCtx,
		RequestedModel:  modelFromInputs(inputs),
		AvailableModels: ec.ProtoModels,
		MessagesJson:    messagesJSON,
		VirtualModels:   ec.ProtoVMs,
		Quota:           ec.quotaInfo(ctx),
		BodyJson:        ec.BodyJSON,
	})
	if err != nil {
		return nil, errors.Wrap(err, "plugin ResolveModel failed")
	}

	// Short-circuit: plugin returns a forged response (e.g. dummy-model).
	if out.ResponseContent != "" {
		return &ForwardResult{
			ResolvedClient: newDummyClient(out.ResponseContent),
			ResolvedModel:  "dummy",
			OutputValues:   map[string]interface{}{"response": out.ResponseContent},
		}, nil
	}

	result := &ForwardResult{
		OutputValues: make(map[string]interface{}),
	}
	if out.ResolvedProxyName != "" {
		result.OutputValues["model_name"] = out.ResolvedProxyName
	}
	return result, nil
}

// ModifiesResponse reports whether the plugin backing this node declares the
// POST_RESPONSE capability, which is the only way it can rewrite the response.
// Plugins without it (or that cannot be resolved) leave the response alone, so
// a streaming call can be forwarded live instead of buffered.
func (e *PluginExecutor) ModifiesResponse(ctx context.Context, node model.PipelineNode) bool {
	data, err := parsePluginNodeData(node)
	if err != nil {
		return false
	}
	_, desc, ok := e.provider.GetOrRestart(ctx, data.PluginName)
	if !ok {
		return false
	}
	return hasCapability(desc, proto.PluginDescriptor_POST_RESPONSE)
}

func (e *PluginExecutor) Backward(ctx context.Context, node model.PipelineNode, state []byte, responseContent string, tokens *TokensUsed, hadError bool) (*BackwardResult, error) {
	data, err := parsePluginNodeData(node)
	if err != nil {
		return &BackwardResult{}, nil
	}

	client, desc, ok := e.provider.GetOrRestart(ctx, data.PluginName)
	if !ok {
		return &BackwardResult{}, nil
	}

	if !hasCapability(desc, proto.PluginDescriptor_POST_RESPONSE) {
		return &BackwardResult{}, nil
	}

	var prompt, completion int64
	if tokens != nil {
		prompt = tokens.Prompt
		completion = tokens.Completion
	}

	out, err := client.PostResponse(ctx, &proto.PostResponseInput{
		Model:           "",
		PromptTokens:    prompt,
		CompletionTokens: completion,
		HadError:        hadError,
		ResponseContent: responseContent,
		NodeState:       state,
	})
	if err != nil {
		slog.WarnContext(ctx, "plugin PostResponse failed",
			slog.String("plugin", data.PluginName),
			slog.Any("error", err))
		return &BackwardResult{}, nil
	}

	return &BackwardResult{ModifiedResponseContent: out.ModifiedResponseContent}, nil
}

func parsePluginNodeData(node model.PipelineNode) (*model.PluginNodeData, error) {
	if node.Data == nil {
		return nil, errors.New("plugin node has no data")
	}
	var d model.PluginNodeData
	if err := json.Unmarshal(node.Data, &d); err != nil {
		return nil, errors.Wrap(err, "unmarshal plugin node data")
	}
	if d.PluginName == "" {
		return nil, errors.New("plugin node data has no pluginName")
	}
	return &d, nil
}

func hasCapability(desc *proto.PluginDescriptor, cap proto.PluginDescriptor_Capability) bool {
	if desc == nil {
		return false
	}
	for _, c := range desc.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

func modelFromInputs(inputs map[string]interface{}) string {
	if v, ok := inputs["model_name"]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
