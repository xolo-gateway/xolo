package pipeline

import (
	"context"

	"github.com/bornholm/genai/llm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// ExecutionContext carries per-request metadata through the pipeline engine.
type ExecutionContext struct {
	OrgID       string
	UserID      string
	TokenID     string
	DisplayName string
	// RequestJSON is the raw JSON of the LLM request body.
	RequestJSON string
	// MessagesJSON is the JSON array of messages extracted from RequestJSON.
	MessagesJSON string
	// BodyJSON is the full request body JSON.
	BodyJSON string
	// ProtoModels is the list of available real models for the org.
	ProtoModels []*proto.ModelInfo
	// ProtoVMs is the list of virtual models visible to the org.
	ProtoVMs []*proto.VirtualModelInfo
	// QuotaInfo lazily resolves the remaining budget of the requesting
	// user/org. It is handed to plugins (PreRequest and ResolveModel) and is
	// only evaluated when a plugin node actually runs. May be nil.
	QuotaInfo func(ctx context.Context) *proto.QuotaInfo
	// VisitedVMs tracks VirtualModelIDs already resolved to detect cycles.
	VisitedVMs map[model.VirtualModelID]struct{}
	// PersonalVMStore is used by ModelExecutor to resolve personal virtual models (~/name).
	PersonalVMStore port.PersonalVirtualModelStore

	// ToolInspectors collects the tool-result inspectors contributed by the
	// nodes of this execution. It is shared by pointer so a node running
	// early can register an inspector that the tool loop, built later on the
	// resolved client, will consult. May be nil (no inspection).
	ToolInspectors *ToolInspectorSet

	// TargetModelName is the model name originally requested by the caller. It
	// is resolved by "passthrough" model nodes (used in Middleware pipelines),
	// once the middleware chain has been fully applied.
	TargetModelName string
	// ForcePassthrough makes every model node of the current graph behave as a
	// passthrough node. It is set while executing a Middleware chain so the
	// terminal always wraps the requested model (guaranteeing transparency and
	// chaining), and is turned off again when descending into a virtual model.
	ForcePassthrough bool
	// PendingMiddlewares is the ordered (ascending priority) list of middlewares
	// that still have to wrap the target model. A passthrough model node pops the
	// next one and runs its graph; when empty, it resolves TargetModelName.
	PendingMiddlewares []model.Middleware
}

// quotaInfo returns the resolved quota, or nil when no resolver is configured.
func (ec ExecutionContext) quotaInfo(ctx context.Context) *proto.QuotaInfo {
	if ec.QuotaInfo == nil {
		return nil
	}
	return ec.QuotaInfo(ctx)
}

// UsedModelReporter is implemented by clients that may answer with a model
// other than the one resolved up front (fallback chains). The hook adapter
// reads it after the call to attribute usage to the model that actually
// answered.
type UsedModelReporter interface {
	UsedModel() (realModel string, modelID model.LLMModelID, ok bool)
}

// ForwardResult is the output of a node's Forward execution.
type ForwardResult struct {
	// ModelOutcome, when set by a terminal node, reports after the call which
	// model actually answered.
	ModelOutcome UsedModelReporter
	// OutputValues are the typed values produced on output ports.
	OutputValues map[string]interface{}
	// NodeState is an opaque byte blob the pipeline engine stores and passes
	// back to the same node in the backward (post-response) pass.
	NodeState []byte
	// ResolvedClient is set by terminal (model) nodes to the llm.Client that
	// should handle the actual LLM call.
	ResolvedClient llm.Client
	// ResolvedModel is the real model name forwarded to the provider.
	ResolvedModel string
	// ResolvedModelID is the internal database ID of the resolved LLM model.
	ResolvedModelID model.LLMModelID
	// Rejected is true when a plugin node blocks the request.
	Rejected        bool
	RejectionReason string
	// Tools are additional llm.Tool definitions contributed by this node
	// (e.g. a TOOL_PROVIDER plugin), to be made available to the LLM call.
	Tools []llm.Tool
	// ClientDecorator, when set, wraps the eventually-resolved llm.Client
	// (e.g. to intercept and resolve tool calls server-side before they
	// reach the API client). Applied by the engine once the terminal model
	// node has resolved a client.
	ClientDecorator func(llm.Client) llm.Client
	// NoResponseRewrite is set by a node that could rewrite the response in
	// general, but knows it will not for this particular execution. It lets a
	// streaming response be forwarded live instead of buffered.
	NoResponseRewrite bool
	// NestedExecutedNodes carries the ExecutedNodes of a nested pipeline run
	// (virtual-model recursion or middleware chaining). The engine splices them
	// into the parent execution so their backward (post-response) pass runs.
	NestedExecutedNodes []ExecutedNode
}

// BackwardResult is the output of a node's Backward execution.
type BackwardResult struct {
	// ModifiedResponseContent, when non-empty, replaces the response sent to the client.
	ModifiedResponseContent string
}

// TokensUsed holds token counts reported by the LLM.
type TokensUsed struct {
	Prompt     int64
	Completion int64
}

// NodeExecutor is implemented by each node type.
type NodeExecutor interface {
	// Forward is called during the request phase (before the LLM call).
	Forward(ctx context.Context, node model.PipelineNode, inputs map[string]interface{}, ec ExecutionContext) (*ForwardResult, error)
	// Backward is called during the response phase (after the LLM call) in
	// reverse order. state is the NodeState returned by Forward for the same
	// node in the same execution.
	Backward(ctx context.Context, node model.PipelineNode, state []byte, responseContent string, tokens *TokensUsed, hadError bool) (*BackwardResult, error)
}

// ResponseModifier is implemented by executors whose Backward pass may replace
// the response content. Executors that do not implement it are assumed to leave
// the response untouched.
//
// This is what lets a streaming response be forwarded chunk by chunk instead of
// being buffered until the LLM is done: buffering is only required when some
// node might rewrite the text after the fact.
type ResponseModifier interface {
	// ModifiesResponse reports whether this node's Backward pass may return a
	// ModifiedResponseContent. It must be conservative: return true when unsure.
	//
	// It answers for the node type as a whole. A node that can rewrite in
	// general but knows it will not for the execution at hand should instead
	// set ForwardResult.NoResponseRewrite, which the engine checks first.
	ModifiesResponse(ctx context.Context, node model.PipelineNode) bool
}

// noopBackward is a helper that returns an empty BackwardResult without error.
func noopBackward(_ context.Context, _ model.PipelineNode, _ []byte, _ string, _ *TokensUsed, _ bool) (*BackwardResult, error) {
	return &BackwardResult{}, nil
}
