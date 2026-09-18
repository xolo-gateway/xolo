package webui

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/slogx"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/profile/component"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authz"
)

func (h *Handler) getModelsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	memberships := httpCtx.Memberships(ctx)

	rangeParam := r.URL.Query().Get("range")
	since := dashboardRangeToSince(rangeParam)
	showAll := r.URL.Query().Get("show_all") == "true"
	sortParam := r.URL.Query().Get("sort")
	orderParam := r.URL.Query().Get("order")
	// Normalise unknown sort values so the URL and the active UI tab never
	// disagree (e.g. ?sort=price_invalid would otherwise render "Usage" as
	// active while actually falling back to usage ordering). The result is
	// forwarded to ModelsPageVModel.Sort, which modelsSortControl reads to
	// decide which segment is rendered as the active <a> (the active
	// segment carries the next-cycle href); keep the normalisation here so
	// a future template-only move cannot desync the tab state from the
	// actual ordering.
	if sortParam != "" && sortParam != "price" {
		sortParam = ""
	}
	// Each criterion has its own default direction:
	//   - price is naturally read ascending (cheapest first);
	//   - usage is naturally read descending (most-used first).
	// orderParam overrides the default when set to the opposite value.
	order := orderDesc
	if sortParam == "price" {
		order = orderAsc
	}
	orderExplicit := false
	switch orderParam {
	case "asc":
		order = orderAsc
		orderExplicit = true
	case "desc":
		order = orderDesc
		orderExplicit = true
	}

	modelUsages := h.loadModelUsages(ctx, user.ID(), memberships, since)

	sort.Slice(modelUsages, func(i, j int) bool {
		return compareModelUsages(modelUsages[i], modelUsages[j], sortParam, order)
	})

	allModelUsages := modelUsages
	totalCount := len(modelUsages)
	remainingCount := totalCount - component.DefaultMaxDisplayedModels

	if !showAll && totalCount > component.DefaultMaxDisplayedModels {
		modelUsages = modelUsages[:component.DefaultMaxDisplayedModels]
	} else {
		remainingCount = 0
	}

	vmodel := component.ModelsPageVModel{
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "models",
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: "Espace personnel", Href: "/usage"},
				{Label: "Modèles", Href: ""},
			},
			Context: common.ContextPersonal,
		},
		ModelUsages:    modelUsages,
		AllModelUsages: allModelUsages,
		Range:          rangeParam,
		RemainingCount: remainingCount,
		ShowAll:        showAll,
		Sort:           sortParam,
		Order:          order.String(),
		OrderExplicit:  orderExplicit,
	}

	templ.Handler(component.ModelsPage(vmodel)).ServeHTTP(w, r)
}

// sortOrder encodes the order in which compareModelUsages ranks rows.
type sortOrder int

const (
	// orderAsc is the natural ascending direction: cheaper first under
	// price sort, fewer requests first under usage sort.
	orderAsc sortOrder = iota
	// orderDesc reverses the natural direction: more expensive first
	// under price sort, more requests first under usage sort.
	orderDesc
)

// String returns the URL form of the order: "asc" or "desc". Used to
// propagate the effective ordering to ModelsPageVModel.Order so the
// toggle button can render the right icon and the templ href builders
// can preserve it across links.
func (o sortOrder) String() string {
	switch o {
	case orderDesc:
		return "desc"
	default:
		return "asc"
	}
}

// compareModelUsages orders two ModelUsage rows for the /models page.
//
// sortParam semantics:
//   - "price" — order by per-1K rate (PromptCostPer1KTokens +
//     CompletionCostPer1KTokens). Virtual models have no direct pricing
//     so they are pushed to the end of the list regardless of order.
//     This is the same rate the card display builds from, NOT
//     Aggregate.TotalCost over the selected period. Ties (including
//     virtual-vs-virtual) fall back to the usage comparison below.
//   - anything else (including "") — fall through to the existing usage
//     comparison (total requests).
//
// ord flips the direction of the active sort. Under usage sort with
// orderAsc, models with no usage (Aggregate nil or TotalRequests == 0)
// are pushed to the end so the user does not see untested models at
// the top. Under usage sort with orderDesc, higher-usage models lead
// the list and unused models trail — this is the default historical
// behaviour and is preserved.
//
// Rows that compare equal under every tie-breaker (e.g. two virtual
// models with nil aggregates) keep the order produced by loadModelUsages;
// sort.Slice is unstable, so two such rows may swap between renders.
func compareModelUsages(a, b component.ModelUsage, sortParam string, ord sortOrder) bool {
	if sortParam == "price" {
		aVirtual := a.Model.IsVirtual()
		bVirtual := b.Model.IsVirtual()
		if aVirtual != bVirtual {
			// Virtual models have no rate to compare, keep them at the
			// end of the list regardless of order.
			return !aVirtual
		}
		// Both are non-virtual (the early return on aVirtual != bVirtual
		// guarantees this). Two non-virtual models with different per-1K
		// rates are ordered in the requested direction; equal-rate pairs
		// fall through to the usage comparison below.
		if !aVirtual {
			ac := a.Model.PromptCostPer1KTokens() + a.Model.CompletionCostPer1KTokens()
			bc := b.Model.PromptCostPer1KTokens() + b.Model.CompletionCostPer1KTokens()
			if ac != bc {
				if ord == orderDesc {
					return ac > bc
				}
				return ac < bc
			}
		}
	}

	aa := a.Aggregate
	bb := b.Aggregate

	// Under usage sort with orderAsc, push models with no usage (nil
	// aggregate or TotalRequests == 0) to the end so the user does not
	// land on untested models first. Under orderDesc this rule is a
	// no-op: it would still leave untested models at the bottom, which
	// matches the historical default ordering (descending first).
	if sortParam != "price" && ord == orderAsc {
		aUnused := aa == nil || aa.TotalRequests == 0
		bUnused := bb == nil || bb.TotalRequests == 0
		if aUnused != bUnused {
			return !aUnused
		}
	}

	if aa == nil && bb == nil {
		return false
	}
	if aa == nil {
		return false
	}
	if bb == nil {
		return true
	}
	if ord == orderDesc {
		return aa.TotalRequests > bb.TotalRequests
	}
	return aa.TotalRequests < bb.TotalRequests
}

func (h *Handler) loadModelUsages(ctx context.Context, userID model.UserID, memberships []model.Membership, since time.Time) []component.ModelUsage {
	user := httpCtx.User(ctx)
	isGlobalAdmin := user != nil && slices.Contains(user.Roles(), authz.RoleAdmin)

	var modelUsages []component.ModelUsage
	for _, m := range memberships {
		perms, err := h.roleStore.ResolveEffectivePermissions(ctx, userID, m.OrgID())
		if err != nil {
			slog.ErrorContext(ctx, "could not resolve permissions", slogx.Error(err), slog.String("orgID", string(m.OrgID())))
			continue
		}
		canUseOrg := isGlobalAdmin || perms.IsOwner() || perms.Has(rbac.PermModelUseOrg)
		canUseVirtual := isGlobalAdmin || perms.IsOwner() || perms.Has(rbac.PermModelUseVirtual)
		hasLLMGrants := perms.HasAnyGrant(rbac.ModelKindLLM)
		hasVirtualGrants := perms.HasAnyGrant(rbac.ModelKindVirtual)

		var org model.Organization
		if m.Org() != nil {
			org = m.Org()
		}

		// Load regular LLM models (blanket permission or per-model grants)
		if canUseOrg || hasLLMGrants {
			models, err := h.providerStore.ListEnabledLLMModels(ctx, m.OrgID())
			if err != nil {
				slog.ErrorContext(ctx, "could not list models", slogx.Error(err), slog.String("orgID", string(m.OrgID())))
			} else {
				providerCache := make(map[model.ProviderID]model.Provider)
				for _, llmModel := range models {
					if !canUseOrg && !perms.HasModelAccess(string(llmModel.ID()), rbac.ModelKindLLM) {
						continue
					}
					modelID := llmModel.ID()
					modelAgg, err := h.usageStore.AggregateUsage(ctx, port.UsageFilter{
						UserID:  &userID,
						ModelID: &modelID,
						Since:   &since,
					})
					if err != nil {
						slog.ErrorContext(ctx, "could not aggregate model usage", slogx.Error(err))
						modelAgg = nil
					}
					providerID := llmModel.ProviderID()
					if _, ok := providerCache[providerID]; !ok {
						p, err := h.providerStore.GetProviderByID(ctx, providerID)
						if err != nil {
							slog.ErrorContext(ctx, "could not get provider", slogx.Error(err), slog.String("providerID", string(providerID)))
						} else {
							providerCache[providerID] = p
						}
					}
					modelUsages = append(modelUsages, component.ModelUsage{
						Model:     llmModel,
						Org:       org,
						Aggregate: modelAgg,
						Provider:  providerCache[providerID],
					})
				}
			}
		}

		// Load virtual models (blanket permission or per-model grants)
		if h.virtualModelStore != nil && (canUseVirtual || hasVirtualGrants) {
			vms, err := h.virtualModelStore.ListVirtualModels(ctx, m.OrgID())
			if err != nil {
				slog.ErrorContext(ctx, "could not list virtual models", slogx.Error(err), slog.String("orgID", string(m.OrgID())))
			} else {
				for _, vm := range vms {
					if !canUseVirtual && !perms.HasModelAccess(string(vm.ID()), rbac.ModelKindVirtual) {
						continue
					}
					qualifiedName := org.Slug() + "/" + vm.Name()
					modelAgg, err := h.usageStore.AggregateUsage(ctx, port.UsageFilter{
						UserID:         &userID,
						ProxyModelName: &qualifiedName,
						Since:          &since,
					})
					if err != nil {
						slog.ErrorContext(ctx, "could not aggregate virtual model usage", slogx.Error(err))
						modelAgg = nil
					}
					wrappedModel := &virtualModelAsLLMModel{vm: vm, org: org}
					modelUsages = append(modelUsages, component.ModelUsage{
						Model:     wrappedModel,
						Org:       org,
						Aggregate: modelAgg,
					})
				}
			}
		}
	}
	return modelUsages
}
