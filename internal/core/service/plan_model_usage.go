package service

import (
	"slices"
	"strings"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// UpstreamModelUsage is the consumption of one upstream model in the window of
// a per-model plan constraint.
type UpstreamModelUsage struct {
	// UpstreamModel is the real model name the provider meters.
	UpstreamModel string
	// ModelIDs are the provider's models targeting it, the scope the
	// constraint counts for this upstream model.
	ModelIDs []model.LLMModelID
	Tokens   int64
	Value    int64
}

// GroupPlanUsageByUpstreamModel folds a per-model usage breakdown of one
// provider onto the upstream models it meters, so that the models published
// under several proxy names count as one, as they do for the enforcer. Only
// upstream models with usage are returned, sorted by name. Usage of a model
// no longer listed (deleted since) is left out: it cannot be attributed to an
// upstream model any more.
func GroupPlanUsageByUpstreamModel(models []model.LLMModel, providerID model.ProviderID, usage map[model.LLMModelID]port.PlanUsageTotals) []UpstreamModelUsage {
	byUpstream := make(map[string]*UpstreamModelUsage)
	for _, m := range models {
		if m.ProviderID() != providerID {
			continue
		}
		entry, ok := byUpstream[m.RealModel()]
		if !ok {
			entry = &UpstreamModelUsage{UpstreamModel: m.RealModel()}
			byUpstream[m.RealModel()] = entry
		}
		entry.ModelIDs = append(entry.ModelIDs, m.ID())
		if totals, ok := usage[m.ID()]; ok {
			entry.Tokens += totals.Tokens
			entry.Value += totals.Value
		}
	}

	result := make([]UpstreamModelUsage, 0, len(byUpstream))
	for _, entry := range byUpstream {
		if !hasUsage(entry.ModelIDs, usage) {
			continue
		}
		result = append(result, *entry)
	}
	slices.SortFunc(result, func(a, b UpstreamModelUsage) int {
		return strings.Compare(a.UpstreamModel, b.UpstreamModel)
	})
	return result
}

// hasUsage reports whether one of the models was used in the window, even for
// no billable token: a request recorded at zero still shows the model is in use.
func hasUsage(ids []model.LLMModelID, usage map[model.LLMModelID]port.PlanUsageTotals) bool {
	for _, id := range ids {
		if _, ok := usage[id]; ok {
			return true
		}
	}
	return false
}

// WeighSharedPlanUsage sums a per-model usage breakdown of one provider into
// the units of a shared constraint's budgets, each model weighted by its own
// budget (model.PlanConstraint.Weigh). The weight follows the upstream model,
// so the models published under several proxy names weigh the same. Usage of
// a model no longer listed, or of every model when models is empty because
// they could not be read, counts at the highest rate of the constraint
// (WeighUnknown): dropping it would hand its share of the window back to the
// members, and a lower rate could let the window close late.
func WeighSharedPlanUsage(c model.PlanConstraint, models []model.LLMModel, usage map[model.LLMModelID]port.PlanUsageTotals) PlanUsage {
	upstream := make(map[model.LLMModelID]string, len(models))
	for _, m := range models {
		upstream[m.ID()] = m.RealModel()
	}
	var total PlanUsage
	for id, totals := range usage {
		realModel, known := upstream[id]
		tokens, value := c.Weigh(realModel, totals.Tokens, totals.Value)
		if !known {
			tokens, value = c.WeighUnknown(totals.Tokens, totals.Value)
		}
		total.Tokens += tokens
		total.Value += value
	}
	return total
}
