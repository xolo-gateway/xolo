package pipeline

import (
	"context"

	"github.com/bornholm/genai/llm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// ExecutionContext carries per-request metadata through the pipeline engine.
type ExecutionContext struct {
	// SecretScopeID is the graph owner, independent of the org used for models and quotas.
	SecretScopeID string
	OrgID         string
	// OrgSlug is the slug of OrgID, which model names are qualified with.
	OrgSlug     string
	UserID      string
	TokenID     string
	DisplayName string
	// TeamID and TeamSlug designate the team the request is attributed to,
	// empty when it is charged to the organization alone.
	TeamID   string
	TeamSlug string
	// RequestedModel is the model name as the client sent it, team segment
	// included.
	RequestedModel string
	// Access checks every model a node reaches by name against the access of
	// the request. Nil checks nothing.
	Access ModelAccess
	// Delegated holds the static model references of the graph being run when
	// that graph delegates access: an organization virtual model or a
	// middleware, whose author configured them. A request may reach them
	// without access of its own. Nil in a personal virtual model, which
	// delegates nothing: every model it reaches is checked.
	Delegated map[string]struct{}
	// BodyJSON is the raw JSON of the LLM request body. It is the single source
	// of truth for "the incoming request" inside the pipeline engine: the
	// generator node's "request" output is seeded from it, and it is exposed
	// to PreRequest plugins as PreRequestInput.Model unless an upstream node
	// feeds the plugin node a rewritten body (see nodeBody).
	BodyJSON string
	// MessagesJSON is the JSON array of the messages entering the node: those
	// of BodyJSON, as rewritten by the upstream nodes.
	MessagesJSON string
	// BodyMessagesJSON is the JSON array of messages extracted from BodyJSON,
	// as pluginsdk.BodyMessages returns it. A plugin call fed BodyJSON leaves
	// MessagesJSON out when it still equals this value, since the plugin
	// restores it from that body anyway. Empty means unknown: the messages are
	// sent.
	BodyMessagesJSON string
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
	// PluginContext retains the graph owner and node metadata for the backward pass.
	PluginContext *proto.RequestContext
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

// BackwardInput is what a node's Backward execution is given.
type BackwardInput struct {
	PluginContext *proto.RequestContext
	Node          model.PipelineNode
	// NodeState is the blob Forward returned for the same node in the same
	// execution.
	NodeState []byte
	// Model is the identifier of the model that answered the request, surfaced
	// to plugins through PostResponseInput.Model ("Model that was called").
	//
	// For a chain with a ModelFallback terminal node, this is the candidate
	// that actually answered (exec.ModelOutcome.UsedModel()), not the primary
	// resolved first. The forward pass otherwise resolves to a single client
	// (ModelExecutor or a RESOLVE_MODEL short-circuit), in which case it
	// matches ForwardExecution.ResolvedModel.
	//
	// A RESOLVE_MODEL plugin that short-circuits with a forged response
	// (e.g. the bundled dummy-model) hardcodes the literal "dummy" into
	// ResolvedModel, so this field carries that string rather than the user's
	// requested model.
	Model string
	// ResponseContent is the response text as the previous backward nodes left
	// it.
	ResponseContent string
	// ToolCallsJSON carries the tool calls the model emitted, as a JSON array
	// of {"id","name","arguments"} objects. It is empty when the response
	// carries none. A node that rewrote the request must be able to undo its
	// rewriting here too: a placeholder left inside a tool call reaches the
	// client verbatim, which then runs the call against it.
	ToolCallsJSON string
	Tokens        *TokensUsed
	HadError      bool
}

// BackwardResult is the output of a node's Backward execution.
type BackwardResult struct {
	// ModifiedResponseContent, when non-empty, replaces the response sent to the client.
	ModifiedResponseContent string
	// ModifiedToolCallsJSON, when non-empty, replaces the response tool calls.
	// Same shape as BackwardInput.ToolCallsJSON, in the same order: calls are
	// matched back by position, an id being optional on the wire.
	ModifiedToolCallsJSON string
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
	// reverse order.
	Backward(ctx context.Context, in BackwardInput) (*BackwardResult, error)
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
func noopBackward(_ context.Context, _ BackwardInput) (*BackwardResult, error) {
	return &BackwardResult{}, nil
}

// nodeBody returns the request body entering a plugin node and the messages
// it holds: the "request" input when an upstream node feeds one, rewritten by
// a pseudonymizer for instance, the incoming request body otherwise. A node
// downstream of a rewrite thus never sees the original body, and its call
// carries the body once.
func (ec ExecutionContext) nodeBody(inputs map[string]interface{}) (body, bodyMessages string) {
	if b, ok := inputs["request"].(string); ok && b != "" && b != ec.BodyJSON {
		return b, pluginsdk.BodyMessages(b)
	}
	return ec.BodyJSON, ec.BodyMessagesJSON
}

// sentMessagesJSON returns the messages a plugin call carries: empty when they
// are those of the body the call carries already (bodyMessages), from which
// the plugin restores them (pluginsdk.PreRequestMessages). An empty
// bodyMessages is unknown: the messages are sent.
func sentMessagesJSON(messagesJSON, bodyMessages string) string {
	if bodyMessages != "" && messagesJSON == bodyMessages {
		return ""
	}
	return messagesJSON
}
