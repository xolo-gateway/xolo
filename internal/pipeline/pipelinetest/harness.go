package pipelinetest

import (
	"context"
	"errors"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/pipeline"
)

type harnessConfig struct {
	plugins  pipeline.PluginProvider
	resolver pipeline.ModelResolver
	vmStore  port.VirtualModelStore
}

// Option configures a Harness.
type Option func(*harnessConfig)

// WithPlugins sets the PluginProvider used to resolve plugin nodes.
func WithPlugins(provider pipeline.PluginProvider) Option {
	return func(c *harnessConfig) { c.plugins = provider }
}

// WithModelResolver sets the ModelResolver used to resolve model nodes.
func WithModelResolver(resolver pipeline.ModelResolver) Option {
	return func(c *harnessConfig) { c.resolver = resolver }
}

// WithVirtualModelStore sets the store used to resolve chained VirtualModels.
func WithVirtualModelStore(store port.VirtualModelStore) Option {
	return func(c *harnessConfig) { c.vmStore = store }
}

// Harness wires a pipeline.Engine with the standard set of node executors,
// allowing tests to run a full request -> pipeline -> response cycle.
type Harness struct {
	engine *pipeline.Engine
}

// New creates a Harness. By default it uses empty fakes (no plugins, no
// virtual models, and a ModelResolver that falls back to a "default"
// response for any unregistered proxy model name).
func New(opts ...Option) *Harness {
	cfg := &harnessConfig{
		plugins:  NewPluginProvider(),
		resolver: NewModelResolver(),
		vmStore:  NewVirtualModelStore(),
	}
	for _, opt := range opts {
		opt(cfg)
	}

	registry := pipeline.NewRegistry()
	engine := pipeline.NewEngine(registry)
	registry.Register(model.NodeTypeGenerator, pipeline.NewGeneratorExecutor())
	registry.Register(model.NodeTypeSink, pipeline.NewSinkExecutor())
	registry.Register(model.NodeTypeValue, pipeline.NewValueExecutor())
	registry.Register(model.NodeTypePlugin, pipeline.NewPluginExecutor(cfg.plugins))
	registry.Register(model.NodeTypeBlock, pipeline.NewBlockExecutor(nil))
	registry.Register(model.NodeTypeModel, pipeline.NewModelExecutor(cfg.resolver, cfg.vmStore, engine))

	return &Harness{engine: engine}
}

// Engine exposes the underlying pipeline.Engine, for tests that need to call
// RunForward/RunBackward directly (e.g. graph validation tests expecting an
// error before any LLM call would happen).
func (h *Harness) Engine() *pipeline.Engine {
	return h.engine
}

// Result is the outcome of a full Harness.Run.
type Result struct {
	// Forward is the result of the forward pass.
	Forward *pipeline.ForwardExecution
	// Rejected is true if the pipeline rejected the request.
	Rejected bool
	// RejectionReason explains why the request was rejected, when Rejected is true.
	RejectionReason string
	// ResponseContent is the raw content returned by the resolved LLM client.
	ResponseContent string
	// FinalContent is ResponseContent after the backward pass.
	FinalContent string
}

// Run executes the full request -> pipeline (forward) -> LLM -> pipeline
// (backward) -> response cycle for graph.
func (h *Harness) Run(ctx context.Context, graph *model.PipelineGraph, ec pipeline.ExecutionContext) (*Result, error) {
	if ec.VisitedVMs == nil {
		ec.VisitedVMs = map[model.VirtualModelID]struct{}{}
	}

	fwd, err := h.engine.RunForward(ctx, graph, ec)
	if err != nil {
		var rejErr *pipeline.RejectionError
		if errors.As(err, &rejErr) {
			return &Result{Rejected: true, RejectionReason: rejErr.Reason}, nil
		}
		return nil, err
	}

	if fwd.ResolvedClient == nil {
		return nil, errors.New("pipelinetest: pipeline did not resolve a terminal model node")
	}

	res, err := fwd.ResolvedClient.ChatCompletion(ctx)
	if err != nil {
		return nil, err
	}
	content := res.Message().Content()

	final, err := h.engine.RunBackward(ctx, fwd, content, nil, false)
	if err != nil {
		return nil, err
	}

	return &Result{
		Forward:         fwd,
		ResponseContent: content,
		FinalContent:    final,
	}, nil
}

// ECOption configures a pipeline.ExecutionContext built by NewExecutionContext.
type ECOption func(*pipeline.ExecutionContext)

// WithOrgID sets the execution context's organization ID.
func WithOrgID(orgID string) ECOption {
	return func(ec *pipeline.ExecutionContext) { ec.OrgID = orgID }
}

// WithUserID sets the execution context's user ID.
func WithUserID(userID string) ECOption {
	return func(ec *pipeline.ExecutionContext) { ec.UserID = userID }
}

// WithMessagesJSON sets the execution context's messages JSON payload.
func WithMessagesJSON(messagesJSON string) ECOption {
	return func(ec *pipeline.ExecutionContext) { ec.MessagesJSON = messagesJSON }
}

// WithRequestJSON sets the execution context's raw request JSON payload.
func WithRequestJSON(requestJSON string) ECOption {
	return func(ec *pipeline.ExecutionContext) { ec.RequestJSON = requestJSON }
}

// WithTargetModel sets the model requested by the caller, resolved by
// passthrough model nodes in Middleware pipelines.
func WithTargetModel(name string) ECOption {
	return func(ec *pipeline.ExecutionContext) { ec.TargetModelName = name }
}

// WithPendingMiddlewares sets the ordered list of middlewares still to apply,
// consumed by passthrough model nodes.
func WithPendingMiddlewares(mws ...model.Middleware) ECOption {
	return func(ec *pipeline.ExecutionContext) { ec.PendingMiddlewares = mws }
}

// NewExecutionContext builds a pipeline.ExecutionContext with sensible
// defaults (OrgID "test-org", empty VisitedVMs), applying opts on top.
func NewExecutionContext(opts ...ECOption) pipeline.ExecutionContext {
	ec := pipeline.ExecutionContext{
		OrgID:      "test-org",
		VisitedVMs: map[model.VirtualModelID]struct{}{},
	}
	for _, opt := range opts {
		opt(&ec)
	}
	return ec
}
