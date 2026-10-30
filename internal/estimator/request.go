package estimator

// RequestEnergy estimates the energy (Wh) and CO₂ (grams) of one request, mid
// estimates, from the active parameters and throughput of the model serving
// it. ok is false for a model without an active parameter count, which
// cannot be estimated.
func RequestEnergy(tier CloudTier, activeParams int64, tokensPerSecLow, tokensPerSecHigh float64, inputTokens, outputTokens int) (wh, co2Grams float64, ok bool) {
	if activeParams <= 0 {
		return 0, 0, false
	}
	est := NewCloudEstimator(tier).EstimateFromParams(
		float64(activeParams),
		InferenceRequest{InputTokens: inputTokens, OutputTokens: outputTokens},
		tokensPerSecLow,
		tokensPerSecHigh,
	)
	return est.Mid.TotalWh, est.Mid.Equivalences.CO2Grams, true
}
