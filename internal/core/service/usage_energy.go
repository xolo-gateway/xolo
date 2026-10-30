package service

import (
	"context"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/estimator"
)

// EstimateUsageEnergy sums the estimated energy (Wh) and CO₂ (grams, mid
// estimate) of the requests matching the filter.
//
// The whole days of the period come with the energy estimated as their
// requests were recorded. The rest is estimated here: the estimate is
// non-linear per request, so it is computed once per model and token counts,
// then multiplied by the number of requests sharing them. Models without an
// active parameter count, or deleted since, are left out. Any other store error
// is returned: a partial total must not be shown, nor cached, as the real one.
//
// The stored energy is kept as it was estimated: a model deleted since still
// counts on the whole days, and a change of its parameters does not reach them.
func EstimateUsageEnergy(ctx context.Context, usage port.UsageStore, providers port.ProviderStore, filter port.UsageFilter) (totalEnergyWh, totalCO2GramsMid float64, err error) {
	stored, sizes, err := usage.AggregateEnergy(ctx, filter)
	if err != nil {
		return 0, 0, errors.WithStack(err)
	}
	totalEnergyWh, totalCO2GramsMid = stored.Wh, stored.CO2Grams

	models := make(map[model.LLMModelID]model.LLMModel)
	tiers := make(map[model.ProviderID]estimator.CloudTier)
	for _, size := range sizes {
		m, ok := models[size.ModelID]
		if !ok {
			loaded, err := providers.GetLLMModelByID(ctx, size.ModelID)
			switch {
			case errors.Is(err, port.ErrNotFound):
				loaded = nil
			case err != nil:
				return 0, 0, errors.WithStack(err)
			}
			models[size.ModelID] = loaded
			m = loaded
		}
		if m == nil || m.ActiveParams() <= 0 {
			continue
		}
		pid := m.ProviderID()
		tier, ok := tiers[pid]
		if !ok {
			tier = estimator.TierHyperscaler
			p, err := providers.GetProviderByID(ctx, pid)
			switch {
			case err == nil:
				tier = estimator.CloudTier(p.CloudTier())
			case !errors.Is(err, port.ErrNotFound):
				return 0, 0, errors.WithStack(err)
			}
			tiers[pid] = tier
		}
		wh, co2, _ := estimator.RequestEnergy(tier, m.ActiveParams(), m.TokensPerSecLow(), m.TokensPerSecHigh(),
			int(size.PromptTokens), int(size.CompletionTokens))
		totalEnergyWh += wh * float64(size.Requests)
		totalCO2GramsMid += co2 * float64(size.Requests)
	}
	return totalEnergyWh, totalCO2GramsMid, nil
}
