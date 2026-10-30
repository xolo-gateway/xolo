package webui

import (
	"cmp"
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/slogx"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/core/service"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/profile/component"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authz"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

// getModelsPage lists the models the user can call, as a catalog. It reads no
// usage: the usage of each model is on the usage page.
func (h *Handler) getModelsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	memberships := httpCtx.Memberships(ctx)

	filter := parseModelsFilter(r.URL.Query())
	models := h.loadCatalogModels(ctx, user.ID(), memberships)

	vmodel := component.ModelsPageVModel{
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "models",
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: i18n.T(ctx, "nav.context.personal"), Href: "/usage"},
				{Label: i18n.T(ctx, "nav.models"), Href: ""},
			},
			Context: common.ContextPersonal,
		},
		Models:    filterCatalogModels(models, filter),
		AllModels: models,
		Filter:    filter,
		Facets:    catalogFacets(models),
	}

	templ.Handler(component.ModelsPage(vmodel)).ServeHTTP(w, r)
}

// parseModelsFilter reads the filter of the models page from its query
// string. Unknown values are dropped, so the controls never show a choice the
// list does not apply.
func parseModelsFilter(query url.Values) component.ModelsFilter {
	filter := component.ModelsFilter{
		Query:    strings.TrimSpace(query.Get("q")),
		Provider: query.Get("provider"),
		Kind:     query.Get("kind"),
		Type:     query.Get("type"),
		Sort:     query.Get("sort"),
	}
	switch model.ModelKind(filter.Kind) {
	case model.ModelKindCompletion, model.ModelKindEmbeddings, model.ModelKindDecision:
	default:
		filter.Kind = ""
	}
	if filter.Type != component.ModelsTypeDirect && filter.Type != component.ModelsTypeVirtual {
		filter.Type = ""
	}
	if !slices.Contains(component.ModelsSorts, filter.Sort) {
		filter.Sort = component.ModelsSortName
	}
	for _, capability := range query["cap"] {
		if slices.Contains(component.ModelCapabilityKeys, capability) && !slices.Contains(filter.Capabilities, capability) {
			filter.Capabilities = append(filter.Capabilities, capability)
		}
	}
	return filter
}

// filterCatalogModels returns the models matching the filter, in its order.
func filterCatalogModels(models []component.CatalogModel, filter component.ModelsFilter) []component.CatalogModel {
	terms := strings.Fields(strings.ToLower(filter.Query))
	matching := make([]component.CatalogModel, 0, len(models))
	for _, m := range models {
		if filter.Provider != "" && component.ProviderName(m) != filter.Provider {
			continue
		}
		if filter.Kind != "" && string(m.Model.Kind()) != filter.Kind {
			continue
		}
		if filter.Type == component.ModelsTypeDirect && m.Model.IsVirtual() ||
			filter.Type == component.ModelsTypeVirtual && !m.Model.IsVirtual() {
			continue
		}
		if !hasCapabilities(m.Model.Capabilities(), filter.Capabilities) {
			continue
		}
		if !matchesTerms(m, terms) {
			continue
		}
		matching = append(matching, m)
	}
	slices.SortStableFunc(matching, func(a, b component.CatalogModel) int {
		return compareCatalogModels(a, b, filter.Sort)
	})
	return matching
}

func hasCapabilities(caps model.ModelCapabilities, wanted []string) bool {
	for _, capability := range wanted {
		if !component.HasCapability(caps, capability) {
			return false
		}
	}
	return true
}

// matchesTerms is true when every term appears in one of the model's names,
// its description or its provider's name.
func matchesTerms(m component.CatalogModel, terms []string) bool {
	if len(terms) == 0 {
		return true
	}
	haystack := strings.ToLower(strings.Join(append(slices.Clone(component.ModelNames(m)),
		m.Model.ProxyName(), m.Model.Description(), component.ProviderName(m)), "\n"))
	for _, term := range terms {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	return true
}

// compareCatalogModels orders two models by the sort criterion, then by name.
// A virtual model has no price and goes after the priced ones; a model of
// unknown context window goes after the known ones.
func compareCatalogModels(a, b component.CatalogModel, sort string) int {
	var c int
	switch sort {
	case component.ModelsSortPrice:
		if c = cmpBool(a.Model.IsVirtual(), b.Model.IsVirtual()); c == 0 {
			c = cmp.Compare(catalogRate(a), catalogRate(b))
		}
	case component.ModelsSortNewest:
		c = b.Model.CreatedAt().Compare(a.Model.CreatedAt())
	case component.ModelsSortContext:
		if c = cmpBool(a.Model.ContextWindow() <= 0, b.Model.ContextWindow() <= 0); c == 0 {
			c = cmp.Compare(b.Model.ContextWindow(), a.Model.ContextWindow())
		}
	}
	if c != 0 {
		return c
	}
	return strings.Compare(component.ModelNames(a)[0], component.ModelNames(b)[0])
}

func catalogRate(m component.CatalogModel) int64 {
	return m.Model.PromptCostPer1KTokens() + m.Model.CompletionCostPer1KTokens()
}

// cmpBool orders false before true.
func cmpBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	default:
		return -1
	}
}

// catalogFacets lists the choices the filter controls offer: only those that
// narrow the list.
func catalogFacets(models []component.CatalogModel) component.ModelsFacets {
	var facets component.ModelsFacets
	orgs := make(map[model.OrgID]struct{})
	var hasDirect bool
	for _, m := range models {
		if name := component.ProviderName(m); name != "" && !slices.Contains(facets.Providers, name) {
			facets.Providers = append(facets.Providers, name)
		}
		if kind := m.Model.Kind(); !slices.Contains(facets.Kinds, kind) {
			facets.Kinds = append(facets.Kinds, kind)
		}
		if m.Model.IsVirtual() {
			facets.Virtual = true
		} else {
			hasDirect = true
		}
		if m.Org != nil {
			orgs[m.Org.ID()] = struct{}{}
		}
	}
	slices.Sort(facets.Providers)
	slices.Sort(facets.Kinds)
	facets.Virtual = facets.Virtual && hasDirect
	facets.SeveralOrgs = len(orgs) > 1
	return facets
}

// loadCatalogModels lists the models the user can call in every organization
// they belong to: the enabled models and the virtual models they are granted.
func (h *Handler) loadCatalogModels(ctx context.Context, userID model.UserID, memberships []model.Membership) []component.CatalogModel {
	user := httpCtx.User(ctx)
	isGlobalAdmin := user != nil && slices.Contains(user.Roles(), authz.RoleAdmin)

	orgIDs := make([]model.OrgID, 0, len(memberships))
	for _, m := range memberships {
		orgIDs = append(orgIDs, m.OrgID())
	}
	cards := h.modelCards.NewResolver(ctx, orgIDs, "", userID)

	var models []component.CatalogModel
	for _, m := range memberships {
		perms, err := h.roleStore.ResolveEffectivePermissions(ctx, userID, m.OrgID())
		if err != nil {
			slog.ErrorContext(ctx, "could not resolve permissions", slogx.Error(err), slog.String("orgID", string(m.OrgID())))
			continue
		}
		if isGlobalAdmin {
			perms = perms.AsOwner()
		}

		var org model.Organization
		if m.Org() != nil {
			org = m.Org()
		}

		// Load regular LLM models (blanket permission or per-model grants)
		if perms.HasAnyModelAccess(rbac.ModelKindLLM) {
			models = append(models, h.directCatalogModels(ctx, perms, m.OrgID(), org)...)
		}

		// Load virtual models (blanket permission or per-model grants)
		if h.virtualModelStore != nil && perms.HasAnyModelAccess(rbac.ModelKindVirtual) {
			vms, err := h.virtualModelStore.ListVirtualModels(ctx, m.OrgID())
			if err != nil {
				slog.ErrorContext(ctx, "could not list virtual models", slogx.Error(err), slog.String("orgID", string(m.OrgID())))
			} else {
				for _, vm := range vms {
					names := service.ListedModelNames(perms, "", orgSlugOf(org), vm.Name(), string(vm.ID()), rbac.ModelKindVirtual)
					if len(names) == 0 {
						continue
					}
					models = append(models, virtualCatalogModel(cards, vm, org, names))
				}
			}
		}
	}
	return models
}

// directCatalogModels lists the enabled models of an organization the
// permissions grant, with their provider.
func (h *Handler) directCatalogModels(ctx context.Context, perms rbac.PermissionSet, orgID model.OrgID, org model.Organization) []component.CatalogModel {
	llmModels, err := h.providerStore.ListEnabledLLMModels(ctx, orgID)
	if err != nil {
		slog.ErrorContext(ctx, "could not list models", slogx.Error(err), slog.String("orgID", string(orgID)))
		return nil
	}
	providers := make(map[model.ProviderID]model.Provider)
	list, err := h.providerStore.ListProviders(ctx, orgID)
	if err != nil {
		slog.ErrorContext(ctx, "could not list providers", slogx.Error(err), slog.String("orgID", string(orgID)))
	}
	for _, p := range list {
		providers[p.ID()] = p
	}

	var models []component.CatalogModel
	for _, llmModel := range llmModels {
		names := service.ListedModelNames(perms, "", orgSlugOf(org), llmModel.ProxyName(), string(llmModel.ID()), rbac.ModelKindLLM)
		if len(names) == 0 {
			continue
		}
		models = append(models, component.CatalogModel{
			Model:    llmModel,
			Org:      org,
			Provider: providers[llmModel.ProviderID()],
			Names:    names,
		})
	}
	return models
}

// virtualCatalogModel describes a virtual model with its catalogue card: its
// capabilities are undetermined when the card does not declare them.
func virtualCatalogModel(cards *service.ModelCardResolver, vm model.VirtualModel, org model.Organization, names []string) component.CatalogModel {
	card := cards.OrgModelCard(vm)
	return component.CatalogModel{
		Model:                    &virtualModelAsLLMModel{vm: vm, org: org, card: card},
		Org:                      org,
		Names:                    names,
		CapabilitiesUndetermined: !card.Declared,
	}
}

func orgSlugOf(org model.Organization) string {
	if org == nil {
		return ""
	}
	return org.Slug()
}
