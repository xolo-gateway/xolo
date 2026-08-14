package proxy

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bornholm/genai/llm"
	genaiProxy "github.com/bornholm/genai/proxy"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/pipeline"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
	"github.com/pkg/errors"
)

const metaPipelineExecution = "pipeline.execution"

// PipelineHookAdapter bridges the pipeline engine into the genai proxy hook chain.
// It implements PreRequestHook (runs the pipeline forward pass, handles rejection)
// and ModelListerHook (returns the pre-resolved client to the proxy chain).
type PipelineHookAdapter struct {
	engine            *pipeline.Engine
	virtualModelStore port.VirtualModelStore
	personalVMStore   port.PersonalVirtualModelStore
	orgStore          port.OrgStore
	providerStore     port.ProviderStore
	middlewareStore   port.MiddlewareStore
}

// NewPipelineHookAdapter creates a PipelineHookAdapter and wires the pipeline engine.
func NewPipelineHookAdapter(
	pluginProvider pipeline.PluginProvider,
	virtualModelStore port.VirtualModelStore,
	personalVMStore port.PersonalVirtualModelStore,
	providerStore port.ProviderStore,
	orgStore port.OrgStore,
	middlewareStore port.MiddlewareStore,
	orgModelRouter *OrgModelRouter,
) *PipelineHookAdapter {
	reg := pipeline.NewRegistry()
	eng := pipeline.NewEngine(reg)

	reg.Register(model.NodeTypeGenerator, pipeline.NewGeneratorExecutor())
	reg.Register(model.NodeTypeSink, pipeline.NewSinkExecutor())
	reg.Register(model.NodeTypeValue, pipeline.NewValueExecutor())
	reg.Register(model.NodeTypePlugin, pipeline.NewPluginExecutor(pluginProvider))
	// ModelExecutor needs the engine for recursive VirtualModel resolution.
	reg.Register(model.NodeTypeModel, pipeline.NewModelExecutor(orgModelRouter, virtualModelStore, eng))

	return &PipelineHookAdapter{
		engine:            eng,
		virtualModelStore: virtualModelStore,
		personalVMStore:   personalVMStore,
		orgStore:          orgStore,
		providerStore:     providerStore,
		middlewareStore:   middlewareStore,
	}
}

func (a *PipelineHookAdapter) Name() string  { return "pipeline" }
func (a *PipelineHookAdapter) Priority() int { return 3 }

// PreRequest runs the full pipeline forward pass for virtual models.
// Rejection results in a 403 response. A successful execution stores the
// ForwardExecution in req.Metadata for ResolveModel to pick up.
func (a *PipelineHookAdapter) PreRequest(ctx context.Context, req *genaiProxy.ProxyRequest) (*genaiProxy.HookResult, error) {
	PopulateMetaFromContext(ctx, req.Metadata)

	// Middlewares wrap the requested model (real or virtual) transparently. If
	// any apply, they take over resolution before the standard virtual-model and
	// OrgModelRouter paths.
	if handled, result := a.applyMiddlewares(ctx, req); handled {
		return result, nil
	}

	org, vm, lookupErr := a.lookupVirtualModel(ctx, req.Model)
	if lookupErr != nil || vm == nil {
		// Not a virtual model — pass through to OrgModelRouter.
		slog.DebugContext(ctx, "pipeline: not a virtual model, delegating to OrgModelRouter",
			slog.String("model", req.Model))
		return nil, nil
	}

	slog.DebugContext(ctx, "pipeline: virtual model found",
		slog.String("model", req.Model),
		slog.String("vmID", string(vm.ID())))

	// Enforce RBAC before running the pipeline.
	if forbidden := a.assertVirtualModelAccess(ctx, req, org, vm); forbidden != nil {
		return forbidden, nil
	}

	if vm.Graph() == nil {
		slog.WarnContext(ctx, "pipeline: virtual model has no pipeline configured — returning error",
			slog.String("model", req.Model))
		return &genaiProxy.HookResult{
			Response: &genaiProxy.ProxyResponse{
				StatusCode: http.StatusUnprocessableEntity,
				Body: map[string]any{"error": map[string]any{
					"type":    "invalid_request_error",
					"message": "Virtual model \"" + req.Model + "\" has no pipeline configured.",
					"code":    "pipeline_not_configured",
				}},
			},
		}, nil
	}

	ec := a.buildEC(ctx, req, org, vm)
	forwardExec, err := a.engine.RunForward(ctx, vm.Graph(), ec)
	return a.finishForwardExecution(ctx, req, ec, forwardExec, err), nil
}

// finishForwardExecution turns a pipeline forward-pass result (or error) into a
// HookResult. On success it stashes the execution in req.Metadata for
// ResolveModel and returns nil; on rejection/error it returns the corresponding
// API response. Shared by the virtual-model and middleware paths.
func (a *PipelineHookAdapter) finishForwardExecution(ctx context.Context, req *genaiProxy.ProxyRequest, ec pipeline.ExecutionContext, forwardExec *pipeline.ForwardExecution, runErr error) *genaiProxy.HookResult {
	if runErr != nil {
		var rejErr *pipeline.RejectionError
		if errors.As(runErr, &rejErr) {
			slog.InfoContext(ctx, "pipeline: request rejected by node",
				slog.String("model", req.Model),
				slog.String("reason", rejErr.Reason))
			return &genaiProxy.HookResult{
				Response: &genaiProxy.ProxyResponse{
					StatusCode: http.StatusForbidden,
					Body:       map[string]any{"error": rejErr.Error()},
				},
			}
		}
		// All pipeline errors are surfaced as API responses — never as Go errors,
		// because the genai proxy's RunOnError swallows errors and continues silently.
		slog.ErrorContext(ctx, "pipeline: forward pass failed",
			slog.String("model", req.Model),
			slog.Any("error", runErr))
		return &genaiProxy.HookResult{
			Response: &genaiProxy.ProxyResponse{
				StatusCode: http.StatusInternalServerError,
				Body: map[string]any{"error": map[string]any{
					"type":    "server_error",
					"message": "Pipeline execution failed for \"" + req.Model + "\": " + runErr.Error(),
					"code":    "pipeline_error",
				}},
			},
		}
	}

	if forwardExec.ResolvedClient == nil {
		slog.ErrorContext(ctx, "pipeline: forward pass completed but no LLM client resolved — pipeline has no terminal node",
			slog.String("model", req.Model))
		return &genaiProxy.HookResult{
			Response: &genaiProxy.ProxyResponse{
				StatusCode: http.StatusUnprocessableEntity,
				Body: map[string]any{"error": map[string]any{
					"type":    "invalid_request_error",
					"message": "Pipeline for model \"" + req.Model + "\" has no terminal node (LLM model or dummy-model).",
					"code":    "pipeline_no_terminal",
				}},
			},
		}
	}

	slog.DebugContext(ctx, "pipeline: forward pass succeeded",
		slog.String("model", req.Model),
		slog.String("resolvedModel", forwardExec.ResolvedModel))

	// Expose the resolved model ID early so quota/subscription hooks (priority 5+)
	// can look up the underlying provider without waiting for ResolveModel.
	if forwardExec.ResolvedModelID != "" {
		req.Metadata[MetaModelID] = string(forwardExec.ResolvedModelID)
	}

	// If a pipeline node (e.g. a PRE_REQUEST plugin) returned a modified
	// messages array, apply it to the actual LLM request by overriding
	// req.ChatOptions. ChatOptions is normally fixed before hooks run, but
	// genai/proxy re-reads it after RunPreRequest, so this is the last word.
	applyModifiedMessages(ctx, req, ec, forwardExec)

	// Store the execution result for ResolveModel.
	req.Metadata[metaPipelineExecution] = forwardExec
	return nil
}

// applyMiddlewares checks whether any enabled middleware applies to the
// requested model and, if so, runs the middleware chain (which wraps the target
// model). It returns handled=true when it has taken responsibility for the
// request — either by storing a pipeline execution (result=nil) or by producing
// a rejection/error/forbidden response.
func (a *PipelineHookAdapter) applyMiddlewares(ctx context.Context, req *genaiProxy.ProxyRequest) (bool, *genaiProxy.HookResult) {
	if a.middlewareStore == nil {
		return false, nil
	}

	orgSlug, localName, ok := splitQualifiedName(req.Model)
	if !ok || orgSlug == "~" {
		// Unqualified or personal models are out of scope for org middlewares.
		return false, nil
	}

	org, err := a.orgStore.GetOrgBySlug(ctx, httpCtx.TenantID(ctx), orgSlug)
	if err != nil {
		return false, nil //nolint:nilerr
	}

	mws, err := a.middlewareStore.ListEnabledMiddlewares(ctx, org.ID())
	if err != nil || len(mws) == 0 {
		return false, nil //nolint:nilerr
	}

	// Resolve the requested model's identity to match against middleware targets.
	var ref model.ModelRef
	var targetVM model.VirtualModel
	if vm, vmErr := a.virtualModelStore.GetVirtualModelByName(ctx, org.ID(), localName); vmErr == nil && vm != nil {
		ref = model.ModelRef{Kind: model.ModelRefKindVirtual, ID: string(vm.ID())}
		targetVM = vm
	} else if llmModel, llmErr := a.providerStore.GetLLMModelByProxyName(ctx, org.ID(), localName); llmErr == nil && llmModel != nil {
		ref = model.ModelRef{Kind: model.ModelRefKindLLM, ID: string(llmModel.ID())}
	} else {
		// Unknown model — let the standard path emit the appropriate error.
		return false, nil
	}

	applicable := applicableMiddlewares(mws, ref)
	if len(applicable) == 0 {
		return false, nil
	}

	// The middleware path bypasses OrgModelRouter's own RBAC, so enforce access
	// to the target model here.
	if forbidden := a.assertTargetAccess(ctx, req, org, ref, targetVM); forbidden != nil {
		return true, forbidden
	}

	ec := a.buildMiddlewareEC(ctx, req, org)
	ec.TargetModelName = req.Model
	ec.PendingMiddlewares = applicable[1:]

	forwardExec, err := a.engine.RunForward(ctx, applicable[0].Graph(), ec)
	return true, a.finishForwardExecution(ctx, req, ec, forwardExec, err)
}

// applicableMiddlewares filters the (priority-ordered) middlewares down to those
// that wrap the given model, skipping any without a configured pipeline.
func applicableMiddlewares(mws []model.Middleware, ref model.ModelRef) []model.Middleware {
	var out []model.Middleware
	for _, mw := range mws {
		if mw.Graph() == nil {
			continue
		}
		if mw.AppliesToAll() || modelRefMatches(mw.Targets(), ref) {
			out = append(out, mw)
		}
	}
	return out
}

func modelRefMatches(targets []model.ModelRef, ref model.ModelRef) bool {
	for _, t := range targets {
		if t.Kind == ref.Kind && t.ID == ref.ID {
			return true
		}
	}
	return false
}

// assertTargetAccess enforces RBAC for the model wrapped by a middleware,
// mirroring the checks OrgModelRouter/pipeline would otherwise perform.
func (a *PipelineHookAdapter) assertTargetAccess(ctx context.Context, req *genaiProxy.ProxyRequest, org model.Organization, ref model.ModelRef, targetVM model.VirtualModel) *genaiProxy.HookResult {
	if targetVM != nil {
		return a.assertVirtualModelAccess(ctx, req, org, targetVM)
	}

	perms, err := httpCtx.ResolvePermissions(ctx, org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "pipeline: could not resolve permissions", slog.Any("error", err))
		return &genaiProxy.HookResult{
			Response: &genaiProxy.ProxyResponse{
				StatusCode: http.StatusInternalServerError,
				Body: map[string]any{"error": map[string]any{
					"type":    "server_error",
					"message": "Could not resolve permissions.",
					"code":    "permission_error",
				}},
			},
		}
	}

	if perms.IsOwner() || perms.Has(rbac.PermModelUseOrg) || perms.HasModelAccess(ref.ID, rbac.ModelKindLLM) {
		return nil
	}
	return virtualModelForbidden(req.Model)
}

// buildMiddlewareEC constructs the ExecutionContext for a middleware chain run.
func (a *PipelineHookAdapter) buildMiddlewareEC(ctx context.Context, req *genaiProxy.ProxyRequest, org model.Organization) pipeline.ExecutionContext {
	userID := ""
	displayName := ""
	if u := httpCtx.User(ctx); u != nil {
		userID = string(u.ID())
		displayName = u.DisplayName()
	}

	orgID := org.ID()
	return pipeline.ExecutionContext{
		OrgID:           string(orgID),
		UserID:          userID,
		DisplayName:     displayName,
		TokenID:         AuthTokenIDFromMeta(req.Metadata),
		MessagesJSON:    extractMessagesJSON(req.Body),
		BodyJSON:        string(req.Body),
		ProtoModels:     buildProtoModels(ctx, a.providerStore, orgID),
		ProtoVMs:        buildProtoVMs(ctx, a.virtualModelStore, orgID),
		VisitedVMs:      map[model.VirtualModelID]struct{}{},
		PersonalVMStore: a.personalVMStore,
		// Every model node in a middleware chain is a passthrough: it wraps the
		// requested model, never a fixed one.
		ForcePassthrough: true,
	}
}

// ResolveModel returns the pre-resolved llm.Client from the pipeline execution.
// If no pipeline execution was stored (non-virtual model), it returns ErrModelNotFound
// so the OrgModelRouter can handle it.
func (a *PipelineHookAdapter) ResolveModel(ctx context.Context, req *genaiProxy.ProxyRequest) (llm.Client, string, error) {
	execAny, ok := req.Metadata[metaPipelineExecution]
	if !ok {
		return nil, "", genaiProxy.ErrModelNotFound
	}

	forwardExec, ok := execAny.(*pipeline.ForwardExecution)
	if !ok || forwardExec == nil || forwardExec.ResolvedClient == nil {
		return nil, "", genaiProxy.ErrModelNotFound
	}

	// Retrieve ec from metadata if possible (stored by PreRequest via closue capture).
	ec, _ := req.Metadata[metaPipelineExecution+".ec"].(pipeline.ExecutionContext)

	// Populate metadata for UsageTracker and QuotaEnforcer.
	if forwardExec.ResolvedModelID != "" {
		req.Metadata[MetaModelID] = string(forwardExec.ResolvedModelID)
	}
	req.Metadata[MetaOriginalModel] = req.Model
	req.Metadata[MetaResolvedModel] = forwardExec.ResolvedModel

	client := NewPipelineWrappedClient(forwardExec.ResolvedClient, a.engine, forwardExec, ec)
	return client, forwardExec.ResolvedModel, nil
}

// ListModels lists available virtual models for the org and the current user's personal VMs.
func (a *PipelineHookAdapter) ListModels(ctx context.Context) ([]genaiProxy.ModelInfo, error) {
	var infos []genaiProxy.ModelInfo

	// Org virtual models
	orgID := OrgIDFromContext(ctx)
	if orgID != "" {
		org, err := a.orgStore.GetOrgByID(ctx, model.OrgID(orgID))
		if err == nil {
			vms, err := a.virtualModelStore.ListVirtualModels(ctx, model.OrgID(orgID))
			if err != nil {
				return nil, errors.WithStack(err)
			}
			perms, err := httpCtx.ResolvePermissions(ctx, model.OrgID(orgID))
			if err != nil {
				return nil, errors.WithStack(err)
			}
			for _, vm := range vms {
				if !perms.IsOwner() && !perms.Has(rbac.PermModelUseVirtual) && !perms.HasModelAccess(string(vm.ID()), rbac.ModelKindVirtual) {
					continue
				}
				infos = append(infos, genaiProxy.ModelInfo{
					ID:      org.Slug() + "/" + vm.Name(),
					OwnedBy: "xolo",
				})
			}
		}
	}

	// Personal virtual models
	if a.personalVMStore != nil {
		if u := httpCtx.User(ctx); u != nil {
			pvms, err := a.personalVMStore.ListPersonalVirtualModels(ctx, u.ID())
			if err != nil {
				slog.WarnContext(ctx, "pipeline: could not list personal virtual models", slog.Any("error", err))
			} else {
				canUsePersonal := true
				if orgID != "" {
					if perms, err := httpCtx.ResolvePermissions(ctx, model.OrgID(orgID)); err == nil {
						canUsePersonal = perms.IsOwner() || perms.Has(rbac.PermPersonalVMCreate)
					}
				}
				if canUsePersonal {
					for _, pvm := range pvms {
						infos = append(infos, genaiProxy.ModelInfo{
							ID:      "~/" + pvm.Name(),
							OwnedBy: "xolo",
						})
					}
				}
			}
		}
	}

	return infos, nil
}

// assertVirtualModelAccess enforces RBAC for virtual model usage. It returns a
// 403 HookResult when access is denied, or nil when allowed. org is nil for
// personal virtual models, in which case the token's org scope is used.
func (a *PipelineHookAdapter) assertVirtualModelAccess(ctx context.Context, req *genaiProxy.ProxyRequest, org model.Organization, vm model.VirtualModel) *genaiProxy.HookResult {
	isPersonal := org == nil

	var permOrgID model.OrgID
	if isPersonal {
		permOrgID = OrgIDFromMeta(req.Metadata)
		if permOrgID == "" {
			permOrgID = model.OrgID(OrgIDFromContext(ctx))
		}
	} else {
		permOrgID = org.ID()
	}

	if permOrgID == "" {
		return virtualModelForbidden(req.Model)
	}

	perms, err := httpCtx.ResolvePermissions(ctx, permOrgID)
	if err != nil {
		slog.ErrorContext(ctx, "pipeline: could not resolve permissions", slog.Any("error", err))
		return &genaiProxy.HookResult{
			Response: &genaiProxy.ProxyResponse{
				StatusCode: http.StatusInternalServerError,
				Body: map[string]any{"error": map[string]any{
					"type":    "server_error",
					"message": "Could not resolve permissions.",
					"code":    "permission_error",
				}},
			},
		}
	}

	if perms.IsOwner() {
		return nil
	}
	if isPersonal {
		if perms.Has(rbac.PermPersonalVMCreate) {
			return nil
		}
	} else if perms.Has(rbac.PermModelUseVirtual) || perms.HasModelAccess(string(vm.ID()), rbac.ModelKindVirtual) {
		return nil
	}

	return virtualModelForbidden(req.Model)
}

// virtualModelForbidden builds a 403 response that does not leak model existence.
func virtualModelForbidden(modelName string) *genaiProxy.HookResult {
	return &genaiProxy.HookResult{
		Response: &genaiProxy.ProxyResponse{
			StatusCode: http.StatusForbidden,
			Body: map[string]any{"error": map[string]any{
				"type":    "permission_error",
				"message": "model '" + modelName + "' not available in your organization",
				"code":    "model_forbidden",
			}},
		},
	}
}

// lookupVirtualModel resolves the org and virtual model from a qualified model name.
// For personal virtual models ("~/name"), org is nil and the VM is user-scoped.
func (a *PipelineHookAdapter) lookupVirtualModel(ctx context.Context, modelName string) (model.Organization, model.VirtualModel, error) {
	orgSlug, localName, ok := splitQualifiedName(modelName)
	if !ok {
		return nil, nil, nil
	}

	// Personal virtual model: "~/model-name"
	if orgSlug == "~" {
		if a.personalVMStore == nil {
			return nil, nil, nil
		}
		u := httpCtx.User(ctx)
		if u == nil {
			return nil, nil, nil
		}
		pvm, err := a.personalVMStore.GetPersonalVirtualModelByName(ctx, u.ID(), localName)
		if err != nil {
			return nil, nil, nil //nolint:nilerr
		}
		return nil, &personalVMAdapter{pvm: pvm}, nil
	}

	// Org virtual model
	org, err := a.orgStore.GetOrgBySlug(ctx, httpCtx.TenantID(ctx), orgSlug)
	if err != nil {
		return nil, nil, nil //nolint:nilerr
	}

	vm, err := a.virtualModelStore.GetVirtualModelByName(ctx, org.ID(), localName)
	if err != nil {
		return nil, nil, nil //nolint:nilerr
	}

	return org, vm, nil
}

// personalVMAdapter wraps PersonalVirtualModel as VirtualModel so the pipeline engine
// can handle it without knowing about the personal VM type.
type personalVMAdapter struct {
	pvm model.PersonalVirtualModel
}

func (a *personalVMAdapter) ID() model.VirtualModelID    { return model.VirtualModelID(a.pvm.ID()) }
func (a *personalVMAdapter) OrgID() model.OrgID          { return "" }
func (a *personalVMAdapter) Name() string                { return a.pvm.Name() }
func (a *personalVMAdapter) Description() string         { return a.pvm.Description() }
func (a *personalVMAdapter) Graph() *model.PipelineGraph { return a.pvm.Graph() }
func (a *personalVMAdapter) CreatedAt() time.Time        { return a.pvm.CreatedAt() }
func (a *personalVMAdapter) UpdatedAt() time.Time        { return a.pvm.UpdatedAt() }

var _ model.VirtualModel = &personalVMAdapter{}

// buildEC constructs the ExecutionContext for a pipeline run.
// org may be nil for personal virtual models; in that case the org context
// is derived from the token's OrgID.
func (a *PipelineHookAdapter) buildEC(ctx context.Context, req *genaiProxy.ProxyRequest, org model.Organization, vm model.VirtualModel) pipeline.ExecutionContext {
	userID := ""
	displayName := ""
	if u := httpCtx.User(ctx); u != nil {
		userID = string(u.ID())
		displayName = u.DisplayName()
	}

	// For personal VMs, org is nil — fall back to the token's org for model resolution.
	orgID := model.OrgID(OrgIDFromContext(ctx))
	var protoModels []*proto.ModelInfo
	var protoVMs []*proto.VirtualModelInfo
	if org != nil {
		orgID = org.ID()
		protoModels = buildProtoModels(ctx, a.providerStore, orgID)
		protoVMs = buildProtoVMs(ctx, a.virtualModelStore, orgID)
	} else if orgID != "" {
		protoModels = buildProtoModels(ctx, a.providerStore, orgID)
		protoVMs = buildProtoVMs(ctx, a.virtualModelStore, orgID)
	}

	return pipeline.ExecutionContext{
		OrgID:           string(orgID),
		UserID:          userID,
		DisplayName:     displayName,
		TokenID:         AuthTokenIDFromMeta(req.Metadata),
		MessagesJSON:    extractMessagesJSON(req.Body),
		BodyJSON:        string(req.Body),
		ProtoModels:     protoModels,
		ProtoVMs:        protoVMs,
		ProtoQuota:      nil,
		VisitedVMs:      map[model.VirtualModelID]struct{}{vm.ID(): {}},
		PersonalVMStore: a.personalVMStore,
	}
}

// applyModifiedMessages overrides req.ChatOptions with the messages produced
// by the pipeline's forward pass, if a node modified them. It converts the
// final messages JSON back into []llm.Message according to the wire format
// of the incoming request (Anthropic Messages vs. OpenAI chat completions),
// and appends llm.WithMessages to req.ChatOptions, which fully replaces
// opts.Messages and is applied last by genai/proxy.
func applyModifiedMessages(ctx context.Context, req *genaiProxy.ProxyRequest, ec pipeline.ExecutionContext, forwardExec *pipeline.ForwardExecution) {
	if forwardExec.FinalMessagesJSON == "" || forwardExec.FinalMessagesJSON == ec.MessagesJSON {
		return
	}

	var convertedMsgs []llm.Message
	var convErr error
	if req.Type == genaiProxy.RequestTypeMessage {
		convertedMsgs, convErr = genaiProxy.ConvertAnthropicMessagesJSON(json.RawMessage(forwardExec.FinalMessagesJSON))
	} else {
		convertedMsgs, convErr = genaiProxy.ConvertOpenAIMessagesJSON(json.RawMessage(forwardExec.FinalMessagesJSON))
	}
	if convErr != nil {
		slog.WarnContext(ctx, "pipeline: failed to apply modified messages, ignoring",
			slog.String("model", req.Model),
			slog.Any("error", convErr))
		return
	}

	req.ChatOptions = append(req.ChatOptions, llm.WithMessages(convertedMsgs...))
}

// extractMessagesJSON extracts the "messages" JSON array from a chat completions request body.
// Returns "[]" on failure so plugins receive a valid (empty) messages JSON.
func extractMessagesJSON(body []byte) string {
	if len(body) == 0 {
		return "[]"
	}
	var envelope struct {
		Messages json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Messages == nil {
		return "[]"
	}
	return string(envelope.Messages)
}

// splitQualifiedName splits "org-slug/model-name" → (orgSlug, modelName, true).
func splitQualifiedName(name string) (string, string, bool) {
	idx := strings.IndexByte(name, '/')
	if idx <= 0 || idx == len(name)-1 {
		return "", "", false
	}
	return name[:idx], name[idx+1:], true
}

// buildProtoModels builds the list of available LLM models for the execution context.
func buildProtoModels(ctx context.Context, ps port.ProviderStore, orgID model.OrgID) []*proto.ModelInfo {
	if ps == nil {
		return nil
	}
	models, err := ps.ListEnabledLLMModels(ctx, orgID)
	if err != nil {
		slog.WarnContext(ctx, "could not list models for pipeline context", slog.Any("error", err))
		return nil
	}
	out := make([]*proto.ModelInfo, 0, len(models))
	for _, m := range models {
		caps := m.Capabilities()
		out = append(out, &proto.ModelInfo{
			ProxyName:            m.ProxyName(),
			RealModel:            m.RealModel(),
			ProviderId:           string(m.ProviderID()),
			IsVirtual:            false,
			ContextLength:        m.ContextWindow(),
			SupportsVision:       caps.Vision,
			SupportsReasoning:    caps.Reasoning,
			SupportsEmbeddings:   caps.Embeddings,
			ActiveParamsBillions: float32(m.ActiveParams()) / 1e9,
		})
	}
	return out
}

// buildProtoVMs builds the list of virtual models for the execution context.
func buildProtoVMs(ctx context.Context, vs port.VirtualModelStore, orgID model.OrgID) []*proto.VirtualModelInfo {
	if vs == nil {
		return nil
	}
	vms, err := vs.ListVirtualModels(ctx, orgID)
	if err != nil {
		return nil
	}
	out := make([]*proto.VirtualModelInfo, 0, len(vms))
	for _, vm := range vms {
		out = append(out, &proto.VirtualModelInfo{
			Id:          string(vm.ID()),
			Name:        vm.Name(),
			OrgId:       string(vm.OrgID()),
			Description: vm.Description(),
		})
	}
	return out
}

var (
	_ genaiProxy.PreRequestHook  = (*PipelineHookAdapter)(nil)
	_ genaiProxy.ModelListerHook = (*PipelineHookAdapter)(nil)
)
