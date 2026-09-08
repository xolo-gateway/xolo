package pipeline

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/bornholm/genai/llm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/pkg/errors"
)

// ModelResolver resolves a qualified proxy name (e.g. "org/gpt-4o") to an
// llm.Client. This interface is implemented by OrgModelRouter.
type ModelResolver interface {
	ResolveRealModel(ctx context.Context, orgID model.OrgID, proxyName string) (llm.Client, string, model.LLMModelID, error)
}

// ModelExecutor handles NodeTypeModel.
// It reads the model proxy name either from the "model_name" input port
// (connected at runtime) or from the static ProxyName in its config, then
// resolves it via the ModelResolver.
//
// If the resolved name corresponds to a VirtualModel or personal virtual model,
// it recurses into that model's pipeline (with cycle detection).
type ModelExecutor struct {
	resolver          ModelResolver
	virtualModelStore port.VirtualModelStore
	engine            *Engine // for recursive VM resolution
}

// NewModelExecutor creates a ModelExecutor.
func NewModelExecutor(resolver ModelResolver, virtualModelStore port.VirtualModelStore, engine *Engine) *ModelExecutor {
	return &ModelExecutor{
		resolver:          resolver,
		virtualModelStore: virtualModelStore,
		engine:            engine,
	}
}

func (e *ModelExecutor) Forward(ctx context.Context, node model.PipelineNode, inputs map[string]interface{}, ec ExecutionContext) (*ForwardResult, error) {
	// Passthrough model node (used by Middleware pipelines): instead of a fixed
	// model, it wraps the next pending middleware in the chain, or — when the
	// chain is exhausted — resolves the model the caller actually requested.
	// In a middleware chain (ForcePassthrough), every model node is passthrough
	// regardless of its stored flag.
	if isPassthroughNode(node) || ec.ForcePassthrough {
		if len(ec.PendingMiddlewares) > 0 {
			next := ec.PendingMiddlewares[0]
			if next.Graph() == nil {
				return nil, errors.Errorf("middleware %q has no pipeline configured", next.Name())
			}
			childEC := ec
			childEC.PendingMiddlewares = ec.PendingMiddlewares[1:]
			sub, err := e.engine.RunForward(ctx, next.Graph(), childEC)
			if err != nil {
				return nil, errors.Wrapf(err, "middleware %q pipeline failed", next.Name())
			}
			return nestedForwardResult(sub), nil
		}
		if ec.TargetModelName == "" {
			return nil, errors.New("passthrough model node: no target model in execution context")
		}
		return e.resolveByName(ctx, ec.TargetModelName, ec)
	}

	proxyName, err := resolveModelName(node, inputs)
	if err != nil {
		return nil, errors.Wrap(err, "model node: could not determine model name")
	}

	return e.resolveByName(ctx, proxyName, ec)
}

// ResolveClient resolves a proxy model name to the llm.Client that would serve
// it for the given execution context, recursing into virtual models exactly
// like a model node does. It is the entry point for host-side callers that
// need a model outside of a proxied request, such as plugins asking the host
// for a completion. It also returns the real model name.
func (e *ModelExecutor) ResolveClient(ctx context.Context, proxyName string, ec ExecutionContext) (llm.Client, string, error) {
	res, err := e.resolveByName(ctx, proxyName, ec)
	if err != nil {
		return nil, "", err
	}
	if res.ResolvedClient == nil {
		return nil, "", errors.Errorf("model %q did not resolve to a client", proxyName)
	}
	return res.ResolvedClient, res.ResolvedModel, nil
}

// resolveByName resolves a proxy model name to an llm.Client, recursing into
// personal or org virtual models when the name matches one (with cycle detection).
func (e *ModelExecutor) resolveByName(ctx context.Context, proxyName string, ec ExecutionContext) (*ForwardResult, error) {
	orgID := model.OrgID(ec.OrgID)

	// Personal virtual model (~/name) → recurse using the user's personal store.
	if strings.HasPrefix(proxyName, "~/") && ec.PersonalVMStore != nil && ec.UserID != "" {
		localName := proxyName[2:]
		pvm, pvmErr := ec.PersonalVMStore.GetPersonalVirtualModelByName(ctx, model.UserID(ec.UserID), localName)
		if pvmErr == nil && pvm != nil {
			// Use a namespaced key to avoid collision with org VM IDs.
			pvmKey := model.VirtualModelID("~:" + string(pvm.ID()))
			if _, alreadyVisited := ec.VisitedVMs[pvmKey]; alreadyVisited {
				return nil, errors.Errorf("personal virtual model cycle detected: %s", proxyName)
			}
			if pvm.Graph() == nil {
				return nil, errors.Errorf("personal virtual model %q has no pipeline configured", proxyName)
			}
			childEC := ec
			childEC.VisitedVMs = copyVisitedVMs(ec.VisitedVMs)
			childEC.VisitedVMs[pvmKey] = struct{}{}
			// Leaving the middleware chain: the VM resolves its own models.
			childEC.ForcePassthrough = false

			sub, err := e.engine.RunForward(ctx, pvm.Graph(), childEC)
			if err != nil {
				return nil, errors.Wrapf(err, "personal virtual model %q pipeline failed", proxyName)
			}
			return nestedForwardResult(sub), nil
		}
	}

	// Org virtual model → recurse.
	if e.virtualModelStore != nil {
		vm, vmErr := e.virtualModelStore.GetVirtualModelByName(ctx, orgID, localModelName(proxyName))
		if vmErr == nil && vm != nil {
			vmID := vm.ID()
			if _, alreadyVisited := ec.VisitedVMs[vmID]; alreadyVisited {
				return nil, errors.Errorf("virtual model cycle detected: %s", proxyName)
			}
			if vm.Graph() == nil {
				return nil, errors.Errorf("virtual model %q has no pipeline configured", proxyName)
			}
			// Clone visited set to avoid mutations affecting sibling branches.
			childEC := ec
			childEC.VisitedVMs = copyVisitedVMs(ec.VisitedVMs)
			childEC.VisitedVMs[vmID] = struct{}{}
			// Leaving the middleware chain: the VM resolves its own models.
			childEC.ForcePassthrough = false

			sub, err := e.engine.RunForward(ctx, vm.Graph(), childEC)
			if err != nil {
				return nil, errors.Wrapf(err, "virtual model %q pipeline failed", proxyName)
			}
			return nestedForwardResult(sub), nil
		}
	}

	// Real model: delegate to the ModelResolver.
	client, realModel, modelID, err := e.resolver.ResolveRealModel(ctx, orgID, proxyName)
	if err != nil {
		return nil, errors.Wrapf(err, "model executor: could not resolve %q", proxyName)
	}

	return &ForwardResult{
		ResolvedClient:  client,
		ResolvedModel:   realModel,
		ResolvedModelID: modelID,
		OutputValues:    map[string]interface{}{"response": ""},
	}, nil
}

// ModifiesResponse is always false: a model node never rewrites the response.
func (e *ModelExecutor) ModifiesResponse(context.Context, model.PipelineNode) bool { return false }

func (e *ModelExecutor) Backward(ctx context.Context, node model.PipelineNode, state []byte, responseContent string, tokens *TokensUsed, hadError bool) (*BackwardResult, error) {
	return noopBackward(ctx, node, state, responseContent, tokens, hadError)
}

// nestedForwardResult builds the ForwardResult a model node returns when it
// resolved its client by recursing into a nested pipeline (virtual model or
// middleware chain). Beyond the resolved client, it propagates the nested run's
// message modifications (via the "messages_json" output, so the parent engine
// picks them up as the running messages) and its executed nodes (so their
// backward/post-response pass runs as part of the parent execution). Without
// this, message-modifying plugins (e.g. pseudonymization) buried in a wrapped
// virtual model would be silently dropped.
func nestedForwardResult(sub *ForwardExecution) *ForwardResult {
	out := map[string]interface{}{"response": ""}
	if sub.FinalMessagesJSON != "" {
		out["messages_json"] = sub.FinalMessagesJSON
	}
	return &ForwardResult{
		ResolvedClient:      sub.ResolvedClient,
		ResolvedModel:       sub.ResolvedModel,
		ResolvedModelID:     sub.ResolvedModelID,
		OutputValues:        out,
		NestedExecutedNodes: sub.ExecutedNodes,
	}
}

// isPassthroughNode reports whether a model node resolves the caller's
// requested model (ExecutionContext.TargetModelName) rather than a fixed one.
func isPassthroughNode(node model.PipelineNode) bool {
	if node.Data == nil {
		return false
	}
	var d model.ModelNodeData
	if err := json.Unmarshal(node.Data, &d); err != nil {
		return false
	}
	return d.Passthrough
}

// resolveModelName returns the proxy model name from the "model_name" input
// port (when connected) or the static ProxyName in the node config.
func resolveModelName(node model.PipelineNode, inputs map[string]interface{}) (string, error) {
	if v, ok := inputs["model_name"]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s, nil
		}
	}
	if node.Data != nil {
		var d model.ModelNodeData
		if err := json.Unmarshal(node.Data, &d); err == nil && d.ProxyName != "" {
			return d.ProxyName, nil
		}
	}
	return "", errors.New("no model_name input connected and no static proxyName configured")
}

// localModelName strips the "org-slug/" prefix if present.
func localModelName(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '/' {
			return name[i+1:]
		}
	}
	return name
}

func copyVisitedVMs(m map[model.VirtualModelID]struct{}) map[model.VirtualModelID]struct{} {
	out := make(map[model.VirtualModelID]struct{}, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
