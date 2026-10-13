package service

import (
	"context"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/estimator"
)

// EstimateUsageEnergy sums the estimated energy (Wh) and CO₂ (grams, mid
// estimate) of the requests matching the filter. The estimate is non-linear
// per request, so it is computed once per model and token counts, then
// multiplied by the number of requests sharing them. Models without an active
// parameter count, or deleted since, are left out. Any other store error is
// returned: a partial total must not be shown, nor cached, as the real one.
func EstimateUsageEnergy(ctx context.Context, usage port.UsageStore, providers port.ProviderStore, filter port.UsageFilter) (totalEnergyWh, totalCO2GramsMid float64, err error) {
	sizes, err := usage.AggregateRequestSizes(ctx, filter)
	if err != nil {
		return 0, 0, errors.WithStack(err)
	}

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
		est := estimator.NewCloudEstimator(tier).EstimateFromParams(
			float64(m.ActiveParams()),
			estimator.InferenceRequest{
				InputTokens:  int(size.PromptTokens),
				OutputTokens: int(size.CompletionTokens),
			},
			m.TokensPerSecLow(),
			m.TokensPerSecHigh(),
		)
		totalEnergyWh += est.Mid.TotalWh * float64(size.Requests)
		totalCO2GramsMid += est.Mid.Equivalences.CO2Grams * float64(size.Requests)
	}
	return totalEnergyWh, totalCO2GramsMid, nil
}
