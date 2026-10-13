package service

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/estimator"
)

type requestSizeStore struct {
	port.UsageStore
	sizes []port.RequestSize
}

func (s *requestSizeStore) AggregateRequestSizes(context.Context, port.UsageFilter) ([]port.RequestSize, error) {
	return s.sizes, nil
}

type energyProviderStore struct {
	port.ProviderStore
	models    map[model.LLMModelID]model.LLMModel
	providers map[model.ProviderID]model.Provider
	err       error
}

func (s *energyProviderStore) GetLLMModelByID(_ context.Context, id model.LLMModelID) (model.LLMModel, error) {
	if s.err != nil {
		return nil, s.err
	}
	if m, ok := s.models[id]; ok {
		return m, nil
	}
	return nil, port.ErrNotFound
}

func (s *energyProviderStore) GetProviderByID(_ context.Context, id model.ProviderID) (model.Provider, error) {
	if p, ok := s.providers[id]; ok {
		return p, nil
	}
	return nil, port.ErrNotFound
}

func TestEstimateUsageEnergyCountsEveryRequest(t *testing.T) {
	provider := model.NewProvider("org-1", "local", "openai", "", "", "EUR")
	provider.SetCloudTier(int(estimator.TierMajorCloud))
	sized := model.NewLLMModel(provider.ID(), "org-1", "small", "small", "", 0, 0)
	sized.SetActiveParams(8_000_000_000)
	unsized := model.NewLLMModel(provider.ID(), "org-1", "unknown", "unknown", "", 0, 0)

	providers := &energyProviderStore{
		models:    map[model.LLMModelID]model.LLMModel{sized.ID(): sized, unsized.ID(): unsized},
		providers: map[model.ProviderID]model.Provider{provider.ID(): provider},
	}
	usage := &requestSizeStore{sizes: []port.RequestSize{
		{ModelID: sized.ID(), PromptTokens: 100, CompletionTokens: 50, Requests: 3},
		{ModelID: sized.ID(), PromptTokens: 2000, CompletionTokens: 10, Requests: 1},
		{ModelID: unsized.ID(), PromptTokens: 100, CompletionTokens: 50, Requests: 5},
		{ModelID: "deleted", PromptTokens: 100, CompletionTokens: 50, Requests: 5},
	}}

	energy, co2, err := EstimateUsageEnergy(context.Background(), usage, providers, port.UsageFilter{})
	if err != nil {
		t.Fatalf("EstimateUsageEnergy: %v", err)
	}

	// The same total as estimating each request on its own.
	var wantEnergy, wantCO2 float64
	cloud := estimator.NewCloudEstimator(estimator.CloudTier(provider.CloudTier()))
	for _, req := range []estimator.InferenceRequest{
		{InputTokens: 100, OutputTokens: 50},
		{InputTokens: 100, OutputTokens: 50},
		{InputTokens: 100, OutputTokens: 50},
		{InputTokens: 2000, OutputTokens: 10},
	} {
		est := cloud.EstimateFromParams(float64(sized.ActiveParams()), req, 0, 0)
		wantEnergy += est.Mid.TotalWh
		wantCO2 += est.Mid.Equivalences.CO2Grams
	}
	if wantEnergy == 0 {
		t.Fatal("the reference estimate is zero, the test proves nothing")
	}
	if math.Abs(energy-wantEnergy) > 1e-9*wantEnergy || math.Abs(co2-wantCO2) > 1e-9*wantCO2 {
		t.Errorf("got %v Wh / %v g, want %v Wh / %v g", energy, co2, wantEnergy, wantCO2)
	}
}

// A store failure is not a deleted model: the estimate fails instead of
// leaving the model out of a total the page would show and cache.
func TestEstimateUsageEnergyReportsStoreErrors(t *testing.T) {
	failure := errors.New("store unavailable")
	providers := &energyProviderStore{err: failure}
	usage := &requestSizeStore{sizes: []port.RequestSize{
		{ModelID: "m1", PromptTokens: 100, CompletionTokens: 50, Requests: 1},
	}}

	if _, _, err := EstimateUsageEnergy(context.Background(), usage, providers, port.UsageFilter{}); !errors.Is(err, failure) {
		t.Fatalf("expected the store error, got %v", err)
	}
}
