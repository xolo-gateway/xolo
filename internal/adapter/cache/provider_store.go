package cache

import (
	"context"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// ProviderStore is an LRU cache in front of a port.ProviderStore. It caches the
// read methods exercised on the hot proxy path — GetProviderByID,
// GetLLMModelByID and GetLLMModelByProxyName — which are otherwise re-issued by
// several hooks (router, quota enforcer, subscription enforcer, usage tracker)
// on every LLM request and serialized through the single SQLite connection.
//
// It must wrap the outermost store so that every mutation goes through it and
// invalidates the relevant entries. List methods are passed through unchanged.
type ProviderStore struct {
	backend       port.ProviderStore
	providerCache *MultiIndexCache[*CacheableProvider]
	modelCache    *MultiIndexCache[*CacheableLLMModel]
}

func NewProviderStore(backend port.ProviderStore, size int, ttl time.Duration) *ProviderStore {
	return &ProviderStore{
		backend:       backend,
		providerCache: NewMultiIndexCache[*CacheableProvider](size, ttl),
		modelCache:    NewMultiIndexCache[*CacheableLLMModel](size, ttl),
	}
}

// ShareInvalidations makes every invalidation of the provider and model
// caches reach the other instances sharing the database, through coherence.
func (s *ProviderStore) ShareInvalidations(coherence *Coherence) {
	share(coherence, "providers", s.providerCache)
	share(coherence, "llm_models", s.modelCache)
}

// ── Providers ───────────────────────────────────────────────────────────────

// CreateProvider implements [port.ProviderStore].
func (s *ProviderStore) CreateProvider(ctx context.Context, p model.Provider) error {
	defer s.providerCache.Remove(string(p.ID()))
	return s.backend.CreateProvider(ctx, p)
}

// GetProviderByID implements [port.ProviderStore].
func (s *ProviderStore) GetProviderByID(ctx context.Context, id model.ProviderID) (model.Provider, error) {
	if p, exists := s.providerCache.Get(string(id)); exists {
		return p, nil
	}
	p, err := s.backend.GetProviderByID(ctx, id)
	if err != nil {
		return nil, err
	}
	s.providerCache.Add(NewCacheableProvider(p))
	return p, nil
}

// ListProviders implements [port.ProviderStore].
func (s *ProviderStore) ListProviders(ctx context.Context, orgID model.OrgID) ([]model.Provider, error) {
	return s.backend.ListProviders(ctx, orgID)
}

// SaveProvider implements [port.ProviderStore].
func (s *ProviderStore) SaveProvider(ctx context.Context, p model.Provider) error {
	defer s.providerCache.Remove(string(p.ID()))
	return s.backend.SaveProvider(ctx, p)
}

// DeleteProvider implements [port.ProviderStore].
func (s *ProviderStore) DeleteProvider(ctx context.Context, id model.ProviderID) error {
	defer s.providerCache.Remove(string(id))
	return s.backend.DeleteProvider(ctx, id)
}

// ── LLM Models ──────────────────────────────────────────────────────────────

// CreateLLMModel implements [port.ProviderStore].
func (s *ProviderStore) CreateLLMModel(ctx context.Context, m model.LLMModel) error {
	defer s.modelCache.Remove(string(m.ID()))
	return s.backend.CreateLLMModel(ctx, m)
}

// GetLLMModelByID implements [port.ProviderStore].
func (s *ProviderStore) GetLLMModelByID(ctx context.Context, id model.LLMModelID) (model.LLMModel, error) {
	if m, exists := s.modelCache.Get(string(id)); exists {
		return m, nil
	}
	m, err := s.backend.GetLLMModelByID(ctx, id)
	if err != nil {
		return nil, err
	}
	s.modelCache.Add(NewCacheableLLMModel(m))
	return m, nil
}

// GetLLMModelByProxyName implements [port.ProviderStore].
func (s *ProviderStore) GetLLMModelByProxyName(ctx context.Context, orgID model.OrgID, proxyName string) (model.LLMModel, error) {
	if m, exists := s.modelCache.Get(getLLMModelProxyNameCacheKey(orgID, proxyName)); exists {
		return m, nil
	}
	m, err := s.backend.GetLLMModelByProxyName(ctx, orgID, proxyName)
	if err != nil {
		return nil, err
	}
	s.modelCache.Add(NewCacheableLLMModel(m))
	return m, nil
}

// ListLLMModels implements [port.ProviderStore].
func (s *ProviderStore) ListLLMModels(ctx context.Context, orgID model.OrgID) ([]model.LLMModel, error) {
	return s.backend.ListLLMModels(ctx, orgID)
}

// ListEnabledLLMModels implements [port.ProviderStore].
func (s *ProviderStore) ListEnabledLLMModels(ctx context.Context, orgID model.OrgID) ([]model.LLMModel, error) {
	return s.backend.ListEnabledLLMModels(ctx, orgID)
}

// SaveLLMModel implements [port.ProviderStore].
func (s *ProviderStore) SaveLLMModel(ctx context.Context, m model.LLMModel) error {
	defer s.modelCache.Remove(string(m.ID()))
	return s.backend.SaveLLMModel(ctx, m)
}

// DeleteLLMModel implements [port.ProviderStore].
func (s *ProviderStore) DeleteLLMModel(ctx context.Context, id model.LLMModelID) error {
	defer s.modelCache.Remove(string(id))
	return s.backend.DeleteLLMModel(ctx, id)
}

var _ port.ProviderStore = &ProviderStore{}
