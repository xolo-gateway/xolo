package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// modelTraits is what the catalogue says about one model: the figures a client
// sizes its requests with. A zero field means unknown, not zero.
type modelTraits struct {
	contextLength   int64
	promptLimit     int64
	completionLimit int64
	caps            model.ModelCapabilities
	// Costs in microcents per 1K tokens.
	promptCost     int64
	completionCost int64
}

func traitsOfLLMModel(m model.LLMModel) modelTraits {
	t := modelTraits{
		contextLength:  m.ContextWindow(),
		caps:           m.Capabilities(),
		promptCost:     m.PromptCostPer1KTokens(),
		completionCost: m.CompletionCostPer1KTokens(),
	}
	if tlc := m.TokenLimitConfig(); tlc != nil && tlc.MaxTokens > 0 {
		t.promptLimit = int64(tlc.MaxTokens)
		t.completionLimit = int64(tlc.MaxTokens)
	}
	return t
}

// combineTraits merges the candidates a pipeline may call into one card that
// holds whichever of them answers: the smallest window and limits, only the
// capabilities every candidate has, and the highest price. A candidate with an
// unknown context window makes the window unknown, since no figure would be
// safe. A candidate without a request limit has none to lower the others.
func combineTraits(candidates []modelTraits) modelTraits {
	var out modelTraits
	for i, c := range candidates {
		if i == 0 {
			out = c
			continue
		}
		if out.contextLength <= 0 || c.contextLength <= 0 {
			out.contextLength = 0
		} else {
			out.contextLength = min(out.contextLength, c.contextLength)
		}
		out.promptLimit = minKnown(out.promptLimit, c.promptLimit)
		out.completionLimit = minKnown(out.completionLimit, c.completionLimit)
		out.caps = model.ModelCapabilities{
			Tools:      out.caps.Tools && c.caps.Tools,
			Vision:     out.caps.Vision && c.caps.Vision,
			Reasoning:  out.caps.Reasoning && c.caps.Reasoning,
			Audio:      out.caps.Audio && c.caps.Audio,
			Embeddings: out.caps.Embeddings && c.caps.Embeddings,
		}
		out.promptCost = max(out.promptCost, c.promptCost)
		out.completionCost = max(out.completionCost, c.completionCost)
	}
	return out
}

// minKnown returns the smaller of two limits, where zero means no limit.
func minKnown(a, b int64) int64 {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	default:
		return min(a, b)
	}
}

// applyCatalogOverrides lets the operator's declaration win, field by field,
// over what derivation found.
func applyCatalogOverrides(t modelTraits, o *model.CatalogOverrides) modelTraits {
	if o.IsZero() {
		return t
	}
	if o.ContextWindow > 0 {
		t.contextLength = o.ContextWindow
	}
	if o.MaxOutputTokens > 0 {
		t.completionLimit = o.MaxOutputTokens
	}
	if o.Capabilities != nil {
		t.caps = *o.Capabilities
	}
	return t
}

// cardResolver derives the catalogue card of virtual models from their
// pipeline. It resolves the model names a graph calls with the rules of the
// pipeline's model node: a name is a virtual model of the owning organization
// first, then a real model, local or qualified as "org/name". Only
// organizations already in the caller's scope are consulted, so a card never
// reveals a model the caller could not call.
type cardResolver struct {
	h      *Handler
	ctx    context.Context
	orgIDs []model.OrgID
	// defaultOrg is the organization of the token, against which the local
	// names of a personal virtual model resolve at call time. Empty when the
	// scope holds several: each is then tried.
	defaultOrg model.OrgID
	userID     model.UserID

	orgs     map[model.OrgID]*scopedOrg
	loaded   bool
	resolver map[string]bool // plugin name -> picks the model at runtime
}

type scopedOrg struct {
	slug   string
	models map[string]model.LLMModel
}

func (h *Handler) newCardResolver(ctx context.Context, orgIDs []model.OrgID, defaultOrg model.OrgID, userID model.UserID) *cardResolver {
	return &cardResolver{
		h:          h,
		ctx:        ctx,
		orgIDs:     orgIDs,
		defaultOrg: defaultOrg,
		userID:     userID,
		orgs:       map[model.OrgID]*scopedOrg{},
	}
}

// scope loads the organizations of the caller once, on the first name that
// needs them.
func (r *cardResolver) scope() map[model.OrgID]*scopedOrg {
	if r.loaded {
		return r.orgs
	}
	r.loaded = true
	for _, id := range r.orgIDs {
		org, err := r.h.orgStore.GetOrgByID(r.ctx, id)
		if err != nil {
			slog.WarnContext(r.ctx, "could not load org to derive virtual model cards", slog.String("orgID", string(id)), slog.Any("error", err))
			continue
		}
		models, err := r.h.providerStore.ListEnabledLLMModels(r.ctx, id)
		if err != nil {
			slog.WarnContext(r.ctx, "could not list models to derive virtual model cards", slog.String("orgID", string(id)), slog.Any("error", err))
			continue
		}
		so := &scopedOrg{slug: org.Slug(), models: make(map[string]model.LLMModel, len(models))}
		for _, m := range models {
			so.models[m.ProxyName()] = m
		}
		r.orgs[id] = so
	}
	return r.orgs
}

// orgModelCard returns the card an organization virtual model advertises: the
// figures derived from its pipeline, then the operator's overrides on top.
// Nothing derivable leaves the zero value, which the catalogue renders as the
// plain text-only card.
func (r *cardResolver) orgModelCard(vm model.VirtualModel) modelTraits {
	return r.cardOf(vm.Graph(), vm.CatalogOverrides(), vm.OrgID(), map[string]struct{}{"vm:" + string(vm.ID()): {}})
}

// personalModelCard is orgModelCard for a personal virtual model. Its local
// model names resolve against the organization of the token at call time. When
// the caller's scope holds several, each is tried and the card combines those
// where the pipeline resolves; none resolving leaves it underived.
func (r *cardResolver) personalModelCard(pvm model.PersonalVirtualModel) modelTraits {
	key := "pvm:" + string(pvm.ID())
	owners := r.orgIDs
	if r.defaultOrg != "" {
		owners = []model.OrgID{r.defaultOrg}
	}

	var derived []modelTraits
	for _, owner := range owners {
		if t, ok := r.deriveGraph(pvm.Graph(), owner, map[string]struct{}{key: {}}); ok {
			derived = append(derived, t)
		}
	}
	var t modelTraits
	if len(derived) > 0 {
		t = combineTraits(derived)
	}
	// The proxy bills one organization, so a price that differs between the
	// ones tried would show a figure that may not be the one charged.
	for _, d := range derived[min(1, len(derived)):] {
		if d.promptCost != derived[0].promptCost || d.completionCost != derived[0].completionCost {
			t.promptCost, t.completionCost = 0, 0
			break
		}
	}
	return applyCatalogOverrides(t, pvm.CatalogOverrides())
}

func (r *cardResolver) cardOf(g *model.PipelineGraph, ov *model.CatalogOverrides, owner model.OrgID, visited map[string]struct{}) modelTraits {
	t, _ := r.deriveGraph(g, owner, visited)
	return applyCatalogOverrides(t, ov)
}

// deriveGraph combines the models a graph can call. It gives up, returning
// false, as soon as one of them is not known statically: a passthrough or
// runtime-chosen model node, a plugin that resolves the model, a name that
// does not resolve, or a cycle.
func (r *cardResolver) deriveGraph(g *model.PipelineGraph, owner model.OrgID, visited map[string]struct{}) (modelTraits, bool) {
	if g == nil {
		return modelTraits{}, false
	}

	var names []string
	for _, node := range g.Nodes {
		switch node.Type {
		case model.NodeTypeModel:
			var d model.ModelNodeData
			if len(node.Data) > 0 {
				if err := json.Unmarshal(node.Data, &d); err != nil {
					return modelTraits{}, false
				}
			}
			if d.Passthrough {
				return modelTraits{}, false
			}
			if edges := incomingEdges(g, node.ID, "model_name"); len(edges) > 0 {
				found, ok := staticModelNames(g, edges, map[string]struct{}{})
				if !ok {
					return modelTraits{}, false
				}
				names = append(names, found...)
			} else if d.ProxyName != "" {
				names = append(names, d.ProxyName)
			} else {
				return modelTraits{}, false
			}
		case model.NodeTypeModelFallback:
			var d model.ModelFallbackNodeData
			if len(node.Data) > 0 {
				if err := json.Unmarshal(node.Data, &d); err != nil {
					return modelTraits{}, false
				}
			}
			if edges := incomingEdges(g, node.ID, "model_name"); len(edges) > 0 {
				found, ok := staticModelNames(g, edges, map[string]struct{}{})
				if !ok {
					return modelTraits{}, false
				}
				names = append(names, found...)
			}
			for _, m := range d.Models {
				if m != "" {
					names = append(names, m)
				}
			}
		case model.NodeTypePlugin:
			if r.pluginResolvesModel(node) {
				return modelTraits{}, false
			}
		}
	}
	if len(names) == 0 {
		return modelTraits{}, false
	}

	candidates := make([]modelTraits, 0, len(names))
	for _, name := range names {
		t, ok := r.resolveName(name, owner, visited)
		if !ok {
			return modelTraits{}, false
		}
		candidates = append(candidates, t)
	}
	return combineTraits(candidates), true
}

// resolveName looks a model name up the way the pipeline's model node does.
func (r *cardResolver) resolveName(name string, owner model.OrgID, visited map[string]struct{}) (modelTraits, bool) {
	// Personal virtual model of the caller.
	if strings.HasPrefix(name, "~/") {
		if r.h.personalVMStore == nil || r.userID == "" {
			return modelTraits{}, false
		}
		pvm, err := r.h.personalVMStore.GetPersonalVirtualModelByName(r.ctx, r.userID, name[2:])
		if err != nil || pvm == nil {
			return modelTraits{}, false
		}
		key := "pvm:" + string(pvm.ID())
		if _, seen := visited[key]; seen {
			return modelTraits{}, false
		}
		return r.cardOfNested(pvm.Graph(), pvm.CatalogOverrides(), owner, visited, key)
	}

	// Organization virtual model, looked up by its local name in the owning
	// organization.
	if r.h.virtualModelStore != nil && owner != "" {
		vm, err := r.h.virtualModelStore.GetVirtualModelByName(r.ctx, owner, localName(name))
		switch {
		case err == nil && vm != nil:
			key := "vm:" + string(vm.ID())
			if _, seen := visited[key]; seen {
				return modelTraits{}, false
			}
			return r.cardOfNested(vm.Graph(), vm.CatalogOverrides(), vm.OrgID(), visited, key)
		case err != nil && !errors.Is(err, port.ErrNotFound):
			slog.WarnContext(r.ctx, "could not look up virtual model to derive a card", slog.String("name", name), slog.Any("error", err))
			return modelTraits{}, false
		}
	}

	// Real model, local or "org-slug/name". A slug naming no organization in
	// scope falls back to the owner with the prefix stripped, like the router.
	local := name
	target := owner
	if idx := strings.IndexByte(name, '/'); idx > 0 {
		slug := name[:idx]
		local = name[idx+1:]
		for id, so := range r.scope() {
			if so.slug == slug {
				target = id
				break
			}
		}
	}
	so, ok := r.scope()[target]
	if !ok {
		return modelTraits{}, false
	}
	m, ok := so.models[local]
	if !ok {
		return modelTraits{}, false
	}
	return traitsOfLLMModel(m), true
}

// cardOfNested derives the card of a virtual model called from another one,
// marking it visited for the cycle guard on a copy so sibling branches stay
// independent.
func (r *cardResolver) cardOfNested(g *model.PipelineGraph, ov *model.CatalogOverrides, owner model.OrgID, visited map[string]struct{}, key string) (modelTraits, bool) {
	next := make(map[string]struct{}, len(visited)+1)
	for k := range visited {
		next[k] = struct{}{}
	}
	next[key] = struct{}{}
	t, ok := r.deriveGraph(g, owner, next)
	if !ok {
		// Nothing derived: the nested model advertises its overrides alone,
		// as it does when listed on its own.
		if ov.IsZero() {
			return modelTraits{}, false
		}
		return applyCatalogOverrides(modelTraits{}, ov), true
	}
	return applyCatalogOverrides(t, ov), true
}

// pluginResolvesModel reports whether a plugin node picks the model at runtime.
func (r *cardResolver) pluginResolvesModel(node model.PipelineNode) bool {
	if r.h.pluginManager == nil {
		return false
	}
	if r.resolver == nil {
		r.resolver = map[string]bool{}
		for _, d := range r.h.pluginManager.List() {
			for _, c := range d.GetCapabilities() {
				if c == proto.PluginDescriptor_RESOLVE_MODEL {
					r.resolver[d.GetName()] = true
				}
			}
		}
	}
	var d model.PluginNodeData
	if len(node.Data) == 0 || json.Unmarshal(node.Data, &d) != nil {
		return false
	}
	return r.resolver[d.PluginName]
}

// incomingEdges returns the edges feeding a node's input port.
func incomingEdges(g *model.PipelineGraph, nodeID, port string) []model.PipelineEdge {
	var out []model.PipelineEdge
	for _, e := range g.Edges {
		if e.Target == nodeID && e.TargetPort == port {
			out = append(out, e)
		}
	}
	return out
}

// staticModelNames follows model_name edges back to the nodes that emit a
// constant name: a model_ref, a string value, or a select between such
// sources. Any other source decides at runtime, so the trace fails.
func staticModelNames(g *model.PipelineGraph, edges []model.PipelineEdge, onPath map[string]struct{}) ([]string, bool) {
	var out []string
	for _, e := range edges {
		var src *model.PipelineNode
		for i := range g.Nodes {
			if g.Nodes[i].ID == e.Source {
				src = &g.Nodes[i]
				break
			}
		}
		if src == nil {
			return nil, false
		}

		switch src.Type {
		case model.NodeTypeModelRef:
			var d model.ModelRefNodeData
			if json.Unmarshal(src.Data, &d) != nil || d.ProxyName == "" {
				return nil, false
			}
			out = append(out, d.ProxyName)
		case model.NodeTypeValue:
			var d model.ValueNodeData
			if json.Unmarshal(src.Data, &d) != nil || d.PortType != "string" || d.Value == "" {
				return nil, false
			}
			out = append(out, d.Value)
		case model.NodeTypeSelect:
			// Only a select met again on the path being followed is a loop;
			// two branches may share the same source.
			if _, loop := onPath[src.ID]; loop {
				return nil, false
			}
			branches := append(incomingEdges(g, src.ID, "when_true"), incomingEdges(g, src.ID, "when_false")...)
			if len(branches) == 0 {
				return nil, false
			}
			onPath[src.ID] = struct{}{}
			found, ok := staticModelNames(g, branches, onPath)
			delete(onPath, src.ID)
			if !ok {
				return nil, false
			}
			out = append(out, found...)
		default:
			return nil, false
		}
	}
	return out, true
}

func localName(name string) string {
	if idx := strings.LastIndexByte(name, '/'); idx >= 0 {
		return name[idx+1:]
	}
	return name
}
