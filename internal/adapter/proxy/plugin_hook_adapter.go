package proxy

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/bornholm/genai/llm"
	genaiProxy "github.com/bornholm/genai/proxy"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/core/service"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/pipeline"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

const metaPipelineExecution = "pipeline.execution"

// PipelineHookAdapter bridges the pipeline engine into the genai proxy hook chain.
// It implements PreRequestHook (runs the pipeline forward pass, handles rejection)
// and ModelListerHook (returns the pre-resolved client to the proxy chain).
type PipelineHookAdapter struct {
	engine            *pipeline.Engine
	registry          *pipeline.Registry
	modelExecutor     *pipeline.ModelExecutor
	virtualModelStore port.VirtualModelStore
	personalVMStore   port.PersonalVirtualModelStore
	orgStore          port.OrgStore
	providerStore     port.ProviderStore
	middlewareStore   port.MiddlewareStore
	teamStore         TeamReader
	quotaInfo         *QuotaInfoResolver
}

// TeamReader reads the teams of an organization, to know which middlewares
// belong to a team.
type TeamReader interface {
	ListOrgTeams(ctx context.Context, orgID model.OrgID) ([]model.Team, error)
}

// WithTeams enables team middlewares: a middleware attached to teams runs on
// their requests only, after the organization middlewares. Without it, every
// middleware runs as an organization one.
func (a *PipelineHookAdapter) WithTeams(teams TeamReader) *PipelineHookAdapter {
	a.teamStore = teams
	return a
}

// WithEventEmitter lets trace and block nodes record events; without it they only log.
func (a *PipelineHookAdapter) WithEventEmitter(emitter port.EventEmitter) *PipelineHookAdapter {
	a.registry.Register(model.NodeTypeTrace, pipeline.NewTraceExecutor(emitter))
	a.registry.Register(model.NodeTypeBlock, pipeline.NewBlockExecutor(emitter))
	return a
}

// PostResponse attributes the usage to the model that actually answered when
// the terminal node may switch models (fallback chain). It runs before the
// usage tracker (priority 100), which reads the metadata set here.
func (a *PipelineHookAdapter) PostResponse(ctx context.Context, req *genaiProxy.ProxyRequest, _ *genaiProxy.ProxyResponse) (*genaiProxy.HookResult, error) {
	forwardExec, ok := req.Metadata[metaPipelineExecution].(*pipeline.ForwardExecution)
	if !ok || forwardExec == nil || forwardExec.ModelOutcome == nil {
		return nil, nil
	}
	realModel, modelID, used := forwardExec.ModelOutcome.UsedModel()
	if !used {
		return nil, nil
	}
	if modelID != "" && modelID != ModelIDFromMeta(req.Metadata) {
		slog.InfoContext(ctx, "pipeline: request answered by a fallback model",
			slog.String("primary", forwardExec.ResolvedModel), slog.String("used", realModel))
	}
	if modelID != "" {
		req.Metadata[MetaModelID] = string(modelID)
	}
	if realModel != "" {
		req.Metadata[MetaResolvedModel] = realModel
	}
	return nil, nil
}

// WithQuotaInfo enables the resolution of the requesting user's remaining
// budget, exposed to plugins through PreRequestInput.quota and
// ResolveModelInput.quota. Without it plugins receive no quota.
func (a *PipelineHookAdapter) WithQuotaInfo(r *QuotaInfoResolver) *PipelineHookAdapter {
	a.quotaInfo = r
	return a
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
	reg.Register(model.NodeTypeModelRef, pipeline.NewModelRefExecutor())
	reg.Register(model.NodeTypeCompare, pipeline.NewCompareExecutor())
	reg.Register(model.NodeTypeSelect, pipeline.NewSelectExecutor())
	reg.Register(model.NodeTypeMath, pipeline.NewMathExecutor())
	reg.Register(model.NodeTypeSample, pipeline.NewSampleExecutor())
	reg.Register(model.NodeTypeContext, pipeline.NewContextExecutor())
	reg.Register(model.NodeTypeNote, pipeline.NewNoteExecutor())
	reg.Register(model.NodeTypeTrace, pipeline.NewTraceExecutor(nil))
	reg.Register(model.NodeTypeBlock, pipeline.NewBlockExecutor(nil))
	reg.Register(model.NodeTypeModelFallback, pipeline.NewModelFallbackExecutor(orgModelRouter))
	reg.Register(model.NodeTypePlugin, pipeline.NewPluginExecutor(pluginProvider))
	// ModelExecutor needs the engine for recursive VirtualModel resolution.
	modelExecutor := pipeline.NewModelExecutor(orgModelRouter, virtualModelStore, eng)
	reg.Register(model.NodeTypeModel, modelExecutor)

	return &PipelineHookAdapter{
		engine:            eng,
		registry:          reg,
		modelExecutor:     modelExecutor,
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

	ec, err := a.buildEC(ctx, req, org, vm)
	if err != nil {
		return nil, err
	}
	forwardExec, err := a.engine.RunForward(ctx, vm.Graph(), ec)
	return a.finishForwardExecution(ctx, req, ec, forwardExec, err), nil
}

// finishForwardExecution turns a pipeline forward-pass result (or error) into a
// HookResult. On success it stashes the execution in req.Metadata for
// ResolveModel and returns nil; on rejection/error it returns the corresponding
// API response. Shared by the virtual-model and middleware paths.
func (a *PipelineHookAdapter) finishForwardExecution(ctx context.Context, req *genaiProxy.ProxyRequest, ec pipeline.ExecutionContext, forwardExec *pipeline.ForwardExecution, runErr error) *genaiProxy.HookResult {
	if runErr != nil {
		var accessErr *pipeline.ModelAccessError
		if errors.As(runErr, &accessErr) {
			slog.InfoContext(ctx, "pipeline: a node reached a model the request cannot use",
				slog.String("model", req.Model),
				slog.String("reached", accessErr.Name))
			return &genaiProxy.HookResult{
				Response: &genaiProxy.ProxyResponse{
					StatusCode: http.StatusForbidden,
					Body: map[string]any{"error": map[string]any{
						"type":    "permission_error",
						"message": "Pipeline for \"" + req.Model + "\" reached " + accessErr.Error() + ".",
						"code":    "model_forbidden",
					}},
				},
			}
		}
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
	if err := applyModifiedMessages(ctx, req, ec, forwardExec); err != nil {
		// The body would go upstream without the pipeline's rewrite — for the
		// pseudonymizer, with the very data it exists to hide.
		slog.ErrorContext(ctx, "pipeline: could not carry the modified messages over the request body",
			slog.String("model", req.Model),
			slog.Any("error", err))
		return &genaiProxy.HookResult{
			Response: &genaiProxy.ProxyResponse{
				StatusCode: http.StatusInternalServerError,
				Body: map[string]any{"error": map[string]any{
					"type":    "server_error",
					"message": "Pipeline for model \"" + req.Model + "\" produced messages that cannot be sent: " + err.Error(),
					"code":    "pipeline_error",
				}},
			},
		}
	}

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

	// The team scope already stripped the team segment.
	name, ok := model.ParseModelName(req.Model, nil)
	if !ok || !name.Qualified() || name.Personal() {
		// Unqualified or personal models are out of scope for org middlewares.
		return false, nil
	}
	localName := name.Local

	org, err := a.orgStore.GetOrgBySlug(ctx, httpCtx.TenantID(ctx), name.Org)
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

	applicable, err := a.orderedMiddlewares(ctx, org.ID(), TeamIDFromMeta(req.Metadata), applicableMiddlewares(mws, ref))
	if err != nil {
		slog.ErrorContext(ctx, "pipeline: could not order middlewares", slog.Any("error", err))
		return true, &genaiProxy.HookResult{
			Response: &genaiProxy.ProxyResponse{
				StatusCode: http.StatusInternalServerError,
				Body: map[string]any{"error": map[string]any{
					"type":    "server_error",
					"message": "Could not resolve the middlewares of the request.",
					"code":    "pipeline_error",
				}},
			},
		}
	}
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
	// A middleware delegates its static references, like an organization
	// virtual model.
	ec = a.withProtoLists(ctx, ec.Delegating(applicable[0].Graph()), org.ID())

	forwardExec, err := a.engine.RunForward(ctx, applicable[0].Graph(), ec)
	return true, a.finishForwardExecution(ctx, req, ec, forwardExec, err)
}

// orderedMiddlewares orders the applicable middlewares by level, then by
// priority: the organization middlewares first, which hold compliance rules
// that must not depend on the team a member picks, then those of the team the
// request is attributed to. A middleware attached to teams is a team
// middleware, and never runs for another team or for the organization alone.
func (a *PipelineHookAdapter) orderedMiddlewares(ctx context.Context, orgID model.OrgID, teamID model.TeamID, applicable []model.Middleware) ([]model.Middleware, error) {
	if a.teamStore == nil || len(applicable) == 0 {
		return applicable, nil
	}
	teams, err := a.teamStore.ListOrgTeams(ctx, orgID)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	attached := map[model.MiddlewareID]bool{}
	ofTeam := map[model.MiddlewareID]bool{}
	for _, t := range teams {
		for _, id := range t.MiddlewareIDs() {
			attached[id] = true
			if teamID != "" && t.ID() == teamID {
				ofTeam[id] = true
			}
		}
	}
	var orgLevel, teamLevel []model.Middleware
	for _, mw := range applicable {
		switch {
		case !attached[mw.ID()]:
			orgLevel = append(orgLevel, mw)
		case ofTeam[mw.ID()]:
			teamLevel = append(teamLevel, mw)
		}
	}
	return append(orgLevel, teamLevel...), nil
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

	if perms.CanUseModel(string(TeamIDFromMeta(req.Metadata)), ref.ID, rbac.ModelKindLLM) {
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
	teamID, teamSlug := TeamIDFromMeta(req.Metadata), TeamSlugFromMeta(req.Metadata)
	return pipeline.ExecutionContext{
		OrgID:           string(orgID),
		OrgSlug:         org.Slug(),
		SecretScopeID:   string(orgID),
		UserID:          userID,
		DisplayName:     displayName,
		TokenID:         AuthTokenIDFromMeta(req.Metadata),
		TeamID:          string(teamID),
		TeamSlug:        teamSlug,
		RequestedModel:  RequestedModelFromMeta(req.Metadata),
		Access:          newRequestModelAccess(org, string(teamID), teamSlug, a.orgStore, a.providerStore, a.virtualModelStore),
		MessagesJSON:    extractMessagesJSON(req.Body),
		BodyJSON:        string(req.Body),
		QuotaInfo:       a.quotaInfoFunc(model.UserID(userID), ApplicationIDFromMeta(req.Metadata), orgID, teamID),
		VisitedVMs:      map[model.VirtualModelID]struct{}{},
		PersonalVMStore: a.personalVMStore,
		ToolInspectors:  pipeline.NewToolInspectorSet(),
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
			bound := BoundTeamIDFromContext(ctx)
			for _, vm := range vms {
				for _, name := range service.ListedModelNames(perms, bound, org.Slug(), vm.Name(), string(vm.ID()), rbac.ModelKindVirtual) {
					infos = append(infos, genaiProxy.ModelInfo{ID: name, OwnedBy: "xolo"})
				}
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

	if isPersonal {
		// The models a personal virtual model reaches are checked one by
		// one as the pipeline runs: it delegates nothing.
		if perms.Has(rbac.PermPersonalVMCreate) {
			return nil
		}
	} else if perms.CanUseModel(string(TeamIDFromMeta(req.Metadata)), string(vm.ID()), rbac.ModelKindVirtual) {
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
	// The team scope already stripped the team segment.
	name, ok := model.ParseModelName(modelName, nil)
	if !ok || !name.Qualified() {
		return nil, nil, nil
	}
	orgSlug, localName := name.Org, name.Local

	// Personal virtual model: "~/model-name"
	if name.Personal() {
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
func (a *personalVMAdapter) CatalogOverrides() *model.CatalogOverrides {
	return a.pvm.CatalogOverrides()
}
func (a *personalVMAdapter) CreatedAt() time.Time { return a.pvm.CreatedAt() }
func (a *personalVMAdapter) UpdatedAt() time.Time { return a.pvm.UpdatedAt() }

var _ model.VirtualModel = &personalVMAdapter{}

// buildEC constructs the ExecutionContext for a pipeline run.
// org may be nil for personal virtual models; in that case the org context
// is derived from the token's OrgID.
func (a *PipelineHookAdapter) buildEC(ctx context.Context, req *genaiProxy.ProxyRequest, org model.Organization, vm model.VirtualModel) (pipeline.ExecutionContext, error) {
	userID := ""
	displayName := ""
	if u := httpCtx.User(ctx); u != nil {
		userID = string(u.ID())
		displayName = u.DisplayName()
	}

	// For personal VMs, org is nil — fall back to the token's org for model resolution.
	var orgID model.OrgID
	if org != nil {
		orgID = org.ID()
	} else {
		orgID = OrgIDFromMeta(req.Metadata)
		if orgID == "" {
			orgID = model.OrgID(OrgIDFromContext(ctx))
		}
		if orgID != "" && a.orgStore != nil {
			o, err := a.orgStore.GetOrgByID(ctx, orgID)
			if err != nil {
				return pipeline.ExecutionContext{}, errors.WithStack(err)
			}
			org = o
		}
	}

	scopeID := string(vm.OrgID())
	personal, isPersonal := vm.(*personalVMAdapter)
	if isPersonal {
		scopeID = "~:" + string(personal.pvm.UserID())
	}
	teamID, teamSlug := TeamIDFromMeta(req.Metadata), TeamSlugFromMeta(req.Metadata)
	ec := pipeline.ExecutionContext{
		SecretScopeID:   scopeID,
		OrgID:           string(orgID),
		UserID:          userID,
		DisplayName:     displayName,
		TokenID:         AuthTokenIDFromMeta(req.Metadata),
		TeamID:          string(teamID),
		TeamSlug:        teamSlug,
		MessagesJSON:    extractMessagesJSON(req.Body),
		BodyJSON:        string(req.Body),
		VisitedVMs:      map[model.VirtualModelID]struct{}{vm.ID(): {}},
		PersonalVMStore: a.personalVMStore,
		ToolInspectors:  pipeline.NewToolInspectorSet(),
	}
	if org == nil {
		return ec, nil
	}
	ec.OrgSlug = org.Slug()
	ec.QuotaInfo = a.quotaInfoFunc(model.UserID(userID), ApplicationIDFromMeta(req.Metadata), org.ID(), teamID)
	ec.Access = newRequestModelAccess(org, string(teamID), teamSlug, a.orgStore, a.providerStore, a.virtualModelStore)
	if !isPersonal {
		// An organization virtual model delegates its static references: a
		// team granted the model can use what its author configured.
		ec = ec.Delegating(vm.Graph())
	}
	return a.withProtoLists(ctx, ec, org.ID()), nil
}

// withProtoLists sets the models and virtual models handed to plugins: those
// the request may reach, its own access plus the static references of a
// delegating graph. Access is read from the listed models themselves, with no
// lookup per model.
func (a *PipelineHookAdapter) withProtoLists(ctx context.Context, ec pipeline.ExecutionContext, orgID model.OrgID) pipeline.ExecutionContext {
	perms, err := httpCtx.ResolvePermissions(ctx, orgID)
	if err != nil {
		slog.WarnContext(ctx, "pipeline: could not resolve permissions for the model lists", slog.Any("error", err))
		return ec
	}
	reachable := func(local, id, kind string) bool {
		return ec.Access == nil || ec.Delegates(local) || perms.CanUseModel(ec.TeamID, id, kind)
	}
	ec.ProtoModels = buildProtoModels(ctx, a.providerStore, orgID, reachable)
	ec.ProtoVMs = buildProtoVMs(ctx, a.virtualModelStore, orgID, reachable)
	return ec
}

// applyModifiedMessages overrides req.ChatOptions with the messages produced
// by the pipeline's forward pass, if a node modified them. It converts the
// final messages JSON back into []llm.Message according to the wire format
// of the incoming request (Anthropic Messages vs. OpenAI chat completions),
// and appends llm.WithMessages to req.ChatOptions, which fully replaces
// opts.Messages and is applied last by genai/proxy. On the Messages route it
// also rewrites req.Body, and fails when it cannot: a relayed request is sent
// from the body.
func applyModifiedMessages(ctx context.Context, req *genaiProxy.ProxyRequest, ec pipeline.ExecutionContext, forwardExec *pipeline.ForwardExecution) error {
	if forwardExec.FinalMessagesJSON == "" || forwardExec.FinalMessagesJSON == ec.MessagesJSON {
		return nil
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
		return nil
	}

	// On the Messages route the system prompt is a top-level field, outside the
	// messages array the pipeline rewrites. ConvertAnthropicMessagesJSON does not
	// handle it and llm.WithMessages replaces opts.Messages wholesale, so without
	// this the client's system prompt is dropped as soon as a node rewrites the
	// conversation.
	if req.Type == genaiProxy.RequestTypeMessage {
		systemMsgs, sysErr := requestSystemMessages(req.Body)
		if sysErr != nil {
			// Not reachable on this route: the Messages handler runs
			// ParseMessagesRequest on the same body before any hook, and answers
			// 400 on a malformed body or a system prompt that is neither a string
			// nor an array. Kept so a future caller that skips that parsing loses
			// the prompt loudly rather than silently.
			slog.WarnContext(ctx, "pipeline: could not carry the top-level system prompt over the rewritten messages",
				slog.String("model", req.Model),
				slog.Any("error", sysErr))
		} else if len(systemMsgs) > 0 {
			convertedMsgs = append(systemMsgs, convertedMsgs...)
		}
	}

	req.ChatOptions = append(req.ChatOptions, llm.WithMessages(convertedMsgs...))

	// A request relayed to an Anthropic upstream is sent from the body, not
	// rebuilt from ChatOptions (see genaiProxy.ProxyRequest.Body): the body has
	// to carry the rewritten conversation too. Last, since the client's own
	// system prompt was read from the body above.
	if req.Type == genaiProxy.RequestTypeMessage {
		body, err := withPipelineMessages(req.Body, ec.MessagesJSON, forwardExec.FinalMessagesJSON)
		if err != nil {
			return err
		}
		req.Body = body
	}
	return nil
}

// requestSystemMessages converts the top-level "system" field of an Anthropic
// Messages request body into system messages.
//
// The conversion itself belongs to genai, which owns the wire format: going
// through ConvertAnthropicSystemJSON is what keeps a rewritten request and an
// untouched one producing the same system prompt.
func requestSystemMessages(body []byte) ([]llm.Message, error) {
	if len(body) == 0 {
		return nil, nil
	}

	var envelope struct {
		System json.RawMessage `json:"system"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, errors.Wrap(err, "could not parse request body")
	}

	return genaiProxy.ConvertAnthropicSystemJSON(envelope.System)
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

// buildProtoModels builds the list of available LLM models for the execution
// context, those reachable reports true for.
func buildProtoModels(ctx context.Context, ps port.ProviderStore, orgID model.OrgID, reachable func(local, id, kind string) bool) []*proto.ModelInfo {
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
		if reachable != nil && !reachable(m.ProxyName(), string(m.ID()), rbac.ModelKindLLM) {
			continue
		}
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

// buildProtoVMs builds the list of virtual models for the execution context,
// those reachable reports true for.
func buildProtoVMs(ctx context.Context, vs port.VirtualModelStore, orgID model.OrgID, reachable func(local, id, kind string) bool) []*proto.VirtualModelInfo {
	if vs == nil {
		return nil
	}
	vms, err := vs.ListVirtualModels(ctx, orgID)
	if err != nil {
		return nil
	}
	out := make([]*proto.VirtualModelInfo, 0, len(vms))
	for _, vm := range vms {
		if reachable != nil && !reachable(vm.Name(), string(vm.ID()), rbac.ModelKindVirtual) {
			continue
		}
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

// quotaInfoFunc returns a memoised resolver of the user's remaining quota, or
// nil when quota resolution is not configured. The memo guarantees that the
// underlying store queries run at most once per pipeline execution, however
// many plugin nodes ask for the quota.
func (a *PipelineHookAdapter) quotaInfoFunc(userID model.UserID, appID model.ApplicationID, orgID model.OrgID, teamID model.TeamID) func(context.Context) *proto.QuotaInfo {
	if a.quotaInfo == nil || userID == "" || orgID == "" {
		return nil
	}
	var (
		once sync.Once
		info *proto.QuotaInfo
	)
	return func(ctx context.Context) *proto.QuotaInfo {
		once.Do(func() { info = a.quotaInfo.Resolve(ctx, userID, appID, orgID, teamID) })
		return info
	}
}

var _ genaiProxy.PostResponseHook = &PipelineHookAdapter{}
