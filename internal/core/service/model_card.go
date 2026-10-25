package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// ModelCard is what the catalogue says about one model: the figures a client
// sizes its requests with. A zero field means unknown, not zero.
type ModelCard struct {
	ContextLength int64
	// OutputWindow is the most tokens the model can produce in one answer,
	// as a client sizes max_tokens with.
	OutputWindow    int64
	PromptLimit     int64
	CompletionLimit int64
	Capabilities    model.ModelCapabilities
	// Kind is the model's kind. A virtual model's card is always a
	// completion card, whatever its candidates are: combineTraits sets it.
	Kind model.ModelKind
	// Costs in microcents per 1K tokens of currency. An empty currency means
	// the price is unknown, whatever the costs.
	PromptCost     int64
	CompletionCost int64
	Currency       string
	// Declared is false when nothing tells the capabilities: Capabilities
	// then only holds zero values, not absent capabilities.
	Declared bool
}

// ModelCardOf describes a real model, priced in the currency of its
// provider. An unknown currency leaves the price unknown: a figure without its
// unit would be read in the client's own currency.
func ModelCardOf(m model.LLMModel, currency string) ModelCard {
	t := ModelCard{
		ContextLength:  m.ContextWindow(),
		OutputWindow:   m.OutputWindow(),
		Capabilities:   m.Capabilities(),
		Kind:           m.Kind(),
		PromptCost:     m.PromptCostPer1KTokens(),
		CompletionCost: m.CompletionCostPer1KTokens(),
		Currency:       currency,
		Declared:       true,
	}
	if currency == "" {
		t.PromptCost, t.CompletionCost = 0, 0
	}
	if tlc := m.TokenLimitConfig(); tlc != nil && tlc.MaxTokens > 0 {
		t.PromptLimit = int64(tlc.MaxTokens)
		t.CompletionLimit = int64(tlc.MaxTokens)
	}
	return t
}

// combineTraits merges the candidates a pipeline may call into one card that
// holds whichever of them answers: the smallest window and limits, only the
// capabilities every candidate has, and the highest price. A candidate with an
// unknown context window makes the window unknown, since no figure would be
// safe. A candidate without a request limit has none to lower the others.
// Prices in different currencies, or an unknown one, leave the price unknown;
// one undeclared candidate leaves the capabilities undeclared. The card is a
// completion card: a virtual model serves completions only.
func combineTraits(candidates []ModelCard) ModelCard {
	var out ModelCard
	for i, c := range candidates {
		if i == 0 {
			out = c
			continue
		}
		if out.ContextLength <= 0 || c.ContextLength <= 0 {
			out.ContextLength = 0
		} else {
			out.ContextLength = min(out.ContextLength, c.ContextLength)
		}
		if out.OutputWindow <= 0 || c.OutputWindow <= 0 {
			out.OutputWindow = 0
		} else {
			out.OutputWindow = min(out.OutputWindow, c.OutputWindow)
		}
		out.PromptLimit = minKnown(out.PromptLimit, c.PromptLimit)
		out.CompletionLimit = minKnown(out.CompletionLimit, c.CompletionLimit)
		out.Capabilities = model.ModelCapabilities{
			Tools:     out.Capabilities.Tools && c.Capabilities.Tools,
			Vision:    out.Capabilities.Vision && c.Capabilities.Vision,
			Reasoning: out.Capabilities.Reasoning && c.Capabilities.Reasoning,
			Audio:     out.Capabilities.Audio && c.Capabilities.Audio,
		}
		out.PromptCost = max(out.PromptCost, c.PromptCost)
		out.CompletionCost = max(out.CompletionCost, c.CompletionCost)
		if out.Currency != c.Currency {
			out.Currency = ""
		}
		out.Declared = out.Declared && c.Declared
	}
	if out.Currency == "" {
		out.PromptCost, out.CompletionCost = 0, 0
	}
	out.Kind = model.ModelKindCompletion
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
func applyCatalogOverrides(t ModelCard, o *model.CatalogOverrides) ModelCard {
	if o.IsZero() {
		return t
	}
	if o.ContextWindow > 0 {
		t.ContextLength = o.ContextWindow
	}
	if o.MaxOutputTokens > 0 {
		t.CompletionLimit = o.MaxOutputTokens
		t.OutputWindow = o.MaxOutputTokens
	}
	if o.Capabilities != nil {
		t.Capabilities = *o.Capabilities
		t.Declared = true
	}
	return t
}

// ModelCardService derives the catalogue card of virtual models from their
// pipeline, for the API catalogue and the web UI alike.
type ModelCardService struct {
	orgStore          port.OrgStore
	providerStore     port.ProviderStore
	virtualModelStore port.VirtualModelStore
	personalVMStore   port.PersonalVirtualModelStore
	// modelResolvers names the plugins that pick the model at runtime. Nil
	// means none.
	modelResolvers func() map[string]bool
}

// NewModelCardService returns a ModelCardService. modelResolvers may be nil,
// as may the virtual model stores: names they would resolve then do not.
func NewModelCardService(orgStore port.OrgStore, providerStore port.ProviderStore, virtualModelStore port.VirtualModelStore, personalVMStore port.PersonalVirtualModelStore, modelResolvers func() map[string]bool) *ModelCardService {
	return &ModelCardService{
		orgStore:          orgStore,
		providerStore:     providerStore,
		virtualModelStore: virtualModelStore,
		personalVMStore:   personalVMStore,
		modelResolvers:    modelResolvers,
	}
}

// ModelCardResolver derives cards for one caller. It resolves the model names
// a graph calls with the rules of the pipeline's model node: a name is a
// virtual model of the owning organization first, then a real model, local or
// qualified as "org/name". Only organizations already in the caller's scope
// are consulted, so a card never reveals a model the caller could not call.
// It memoizes what it reads and is meant for a single request.
type ModelCardResolver struct {
	s      *ModelCardService
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
	// currencies memoizes ProviderCurrencies per organization, shared with
	// the listing of the real models.
	currencies map[model.OrgID]map[model.ProviderID]string
	// virtualModels memoizes virtualModel by owner and local name, nil for
	// a name that is no virtual model.
	virtualModels map[string]model.VirtualModel
}

type scopedOrg struct {
	slug   string
	models map[string]model.LLMModel
	// currencies are the currencies of the organization's providers.
	currencies map[model.ProviderID]string
}

// NewResolver returns a resolver for a caller whose scope is orgIDs.
func (s *ModelCardService) NewResolver(ctx context.Context, orgIDs []model.OrgID, defaultOrg model.OrgID, userID model.UserID) *ModelCardResolver {
	return &ModelCardResolver{
		s:          s,
		ctx:        ctx,
		orgIDs:     orgIDs,
		defaultOrg: defaultOrg,
		userID:     userID,
		orgs:       map[model.OrgID]*scopedOrg{},
		currencies: map[model.OrgID]map[model.ProviderID]string{},

		virtualModels: map[string]model.VirtualModel{},
	}
}

// virtualModel looks up a virtual model of owner by its local name, once per
// resolver: every card of a page may name the same candidates. A store error
// other than not found is not memoized.
func (r *ModelCardResolver) virtualModel(owner model.OrgID, name string) (model.VirtualModel, error) {
	key := string(owner) + "/" + name
	if vm, ok := r.virtualModels[key]; ok {
		return vm, nil
	}
	vm, err := r.s.virtualModelStore.GetVirtualModelByName(r.ctx, owner, name)
	switch {
	case errors.Is(err, port.ErrNotFound):
		vm, err = nil, nil
	case err != nil:
		return nil, err
	}
	r.virtualModels[key] = vm
	return vm, nil
}

// ProviderCurrencies returns the currency of each provider of an
// organization, read once per resolver, a failure included.
func (r *ModelCardResolver) ProviderCurrencies(orgID model.OrgID) map[model.ProviderID]string {
	if currencies, ok := r.currencies[orgID]; ok {
		return currencies
	}
	var currencies map[model.ProviderID]string
	providers, err := r.s.providerStore.ListProviders(r.ctx, orgID)
	if err != nil {
		slog.WarnContext(r.ctx, "could not list providers for model pricing", slog.Any("error", err), slog.String("orgID", string(orgID)))
	} else {
		currencies = make(map[model.ProviderID]string, len(providers))
		for _, p := range providers {
			currencies[p.ID()] = p.Currency()
		}
	}
	r.currencies[orgID] = currencies
	return currencies
}

// scope loads the organizations of the caller once, on the first name that
// needs them.
func (r *ModelCardResolver) scope() map[model.OrgID]*scopedOrg {
	if r.loaded {
		return r.orgs
	}
	r.loaded = true
	for _, id := range r.orgIDs {
		org, err := r.s.orgStore.GetOrgByID(r.ctx, id)
		if err != nil {
			slog.WarnContext(r.ctx, "could not load org to derive virtual model cards", slog.String("orgID", string(id)), slog.Any("error", err))
			continue
		}
		models, err := r.s.providerStore.ListEnabledLLMModels(r.ctx, id)
		if err != nil {
			slog.WarnContext(r.ctx, "could not list models to derive virtual model cards", slog.String("orgID", string(id)), slog.Any("error", err))
			continue
		}
		so := &scopedOrg{slug: org.Slug(), models: make(map[string]model.LLMModel, len(models)), currencies: r.ProviderCurrencies(id)}
		for _, m := range models {
			so.models[m.ProxyName()] = m
		}
		r.orgs[id] = so
	}
	return r.orgs
}

// OrgModelCard returns the card an organization virtual model advertises: the
// figures derived from its pipeline, then the operator's overrides on top.
// Nothing derivable leaves the zero value, which the catalogue renders as the
// plain text-only card, marked undeclared.
func (r *ModelCardResolver) OrgModelCard(vm model.VirtualModel) ModelCard {
	return r.cardOf(vm.Graph(), vm.CatalogOverrides(), vm.OrgID(), map[string]struct{}{"vm:" + string(vm.ID()): {}})
}

// PersonalModelCard is OrgModelCard for a personal virtual model. Its local
// model names resolve against the organization of the token at call time. When
// the caller's scope holds several, each is tried and the card combines those
// where the pipeline resolves; none resolving leaves it underived.
func (r *ModelCardResolver) PersonalModelCard(pvm model.PersonalVirtualModel) ModelCard {
	key := "pvm:" + string(pvm.ID())
	owners := r.orgIDs
	if r.defaultOrg != "" {
		owners = []model.OrgID{r.defaultOrg}
	}

	var derived []ModelCard
	for _, owner := range owners {
		if t, ok := r.deriveGraph(pvm.Graph(), owner, map[string]struct{}{key: {}}); ok {
			derived = append(derived, t)
		}
	}
	var t ModelCard
	if len(derived) > 0 {
		t = combineTraits(derived)
	}
	// The proxy bills one organization, so a price that differs between the
	// ones tried would show a figure that may not be the one charged.
	for _, d := range derived[min(1, len(derived)):] {
		if d.PromptCost != derived[0].PromptCost || d.CompletionCost != derived[0].CompletionCost {
			t.PromptCost, t.CompletionCost, t.Currency = 0, 0, ""
			break
		}
	}
	return applyCatalogOverrides(t, pvm.CatalogOverrides())
}

func (r *ModelCardResolver) cardOf(g *model.PipelineGraph, ov *model.CatalogOverrides, owner model.OrgID, visited map[string]struct{}) ModelCard {
	t, _ := r.deriveGraph(g, owner, visited)
	return applyCatalogOverrides(t, ov)
}

// deriveGraph combines the models a graph can call. It gives up, returning
// false, as soon as one of them is not known statically: a passthrough or
// runtime-chosen model node, a plugin that resolves the model, a name that
// does not resolve, or a cycle.
func (r *ModelCardResolver) deriveGraph(g *model.PipelineGraph, owner model.OrgID, visited map[string]struct{}) (ModelCard, bool) {
	if g == nil {
		return ModelCard{}, false
	}

	var names []string
	for _, node := range g.Nodes {
		switch node.Type {
		case model.NodeTypeModel:
			var d model.ModelNodeData
			if len(node.Data) > 0 {
				if err := json.Unmarshal(node.Data, &d); err != nil {
					return ModelCard{}, false
				}
			}
			if d.Passthrough {
				return ModelCard{}, false
			}
			if edges := incomingEdges(g, node.ID, "model_name"); len(edges) > 0 {
				found, ok := staticModelNames(g, edges, map[string]struct{}{})
				if !ok {
					return ModelCard{}, false
				}
				names = append(names, found...)
			} else if d.ProxyName != "" {
				names = append(names, d.ProxyName)
			} else {
				return ModelCard{}, false
			}
		case model.NodeTypeModelFallback:
			var d model.ModelFallbackNodeData
			if len(node.Data) > 0 {
				if err := json.Unmarshal(node.Data, &d); err != nil {
					return ModelCard{}, false
				}
			}
			if edges := incomingEdges(g, node.ID, "model_name"); len(edges) > 0 {
				found, ok := staticModelNames(g, edges, map[string]struct{}{})
				if !ok {
					return ModelCard{}, false
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
				return ModelCard{}, false
			}
		}
	}
	if len(names) == 0 {
		return ModelCard{}, false
	}

	candidates := make([]ModelCard, 0, len(names))
	for _, name := range names {
		t, ok := r.resolveName(name, owner, visited)
		if !ok {
			return ModelCard{}, false
		}
		candidates = append(candidates, t)
	}
	return combineTraits(candidates), true
}

// resolveName looks a model name up the way the pipeline's model node does.
func (r *ModelCardResolver) resolveName(name string, owner model.OrgID, visited map[string]struct{}) (ModelCard, bool) {
	// Personal virtual model of the caller.
	if strings.HasPrefix(name, "~/") {
		if r.s.personalVMStore == nil || r.userID == "" {
			return ModelCard{}, false
		}
		pvm, err := r.s.personalVMStore.GetPersonalVirtualModelByName(r.ctx, r.userID, name[2:])
		if err != nil || pvm == nil {
			return ModelCard{}, false
		}
		key := "pvm:" + string(pvm.ID())
		if _, seen := visited[key]; seen {
			return ModelCard{}, false
		}
		return r.cardOfNested(pvm.Graph(), pvm.CatalogOverrides(), owner, visited, key)
	}

	// Organization virtual model, looked up by its local name in the owning
	// organization.
	if r.s.virtualModelStore != nil && owner != "" {
		vm, err := r.virtualModel(owner, localName(name))
		switch {
		case err == nil && vm != nil:
			key := "vm:" + string(vm.ID())
			if _, seen := visited[key]; seen {
				return ModelCard{}, false
			}
			return r.cardOfNested(vm.Graph(), vm.CatalogOverrides(), vm.OrgID(), visited, key)
		case err != nil && !errors.Is(err, port.ErrNotFound):
			slog.WarnContext(r.ctx, "could not look up virtual model to derive a card", slog.String("name", name), slog.Any("error", err))
			return ModelCard{}, false
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
		return ModelCard{}, false
	}
	m, ok := so.models[local]
	if !ok {
		return ModelCard{}, false
	}
	return ModelCardOf(m, so.currencies[m.ProviderID()]), true
}

// cardOfNested derives the card of a virtual model called from another one,
// marking it visited for the cycle guard on a copy so sibling branches stay
// independent.
func (r *ModelCardResolver) cardOfNested(g *model.PipelineGraph, ov *model.CatalogOverrides, owner model.OrgID, visited map[string]struct{}, key string) (ModelCard, bool) {
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
			return ModelCard{}, false
		}
		return applyCatalogOverrides(ModelCard{}, ov), true
	}
	return applyCatalogOverrides(t, ov), true
}

// pluginResolvesModel reports whether a plugin node picks the model at runtime.
func (r *ModelCardResolver) pluginResolvesModel(node model.PipelineNode) bool {
	if r.s.modelResolvers == nil {
		return false
	}
	if r.resolver == nil {
		r.resolver = r.s.modelResolvers()
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
			// Like the value node at runtime, any type but number and boolean
			// emits a string, an absent one included.
			var d model.ValueNodeData
			if json.Unmarshal(src.Data, &d) != nil || d.PortType == "number" || d.PortType == "boolean" || d.Value == "" {
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
