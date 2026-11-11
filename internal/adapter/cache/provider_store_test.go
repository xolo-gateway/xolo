package cache

import (
	"context"
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// countingProviderStore is a minimal port.ProviderStore backend that records how
// many times each cached read method reaches the backend.
type countingProviderStore struct {
	port.ProviderStore

	provider model.Provider
	llmModel model.LLMModel

	getProviderByID int
	getModelByID    int
	getModelByProxy int
}

func (s *countingProviderStore) GetProviderByID(ctx context.Context, id model.ProviderID) (model.Provider, error) {
	s.getProviderByID++
	return s.provider, nil
}

func (s *countingProviderStore) GetLLMModelByID(ctx context.Context, id model.LLMModelID) (model.LLMModel, error) {
	s.getModelByID++
	return s.llmModel, nil
}

func (s *countingProviderStore) GetLLMModelByProxyName(ctx context.Context, orgID model.OrgID, proxyName string) (model.LLMModel, error) {
	s.getModelByProxy++
	return s.llmModel, nil
}

func (s *countingProviderStore) SaveProvider(ctx context.Context, p model.Provider) error { return nil }

func (s *countingProviderStore) SaveLLMModel(ctx context.Context, m model.LLMModel) error { return nil }

func newTestStores() (*countingProviderStore, *ProviderStore) {
	provider := model.NewProvider("org-1", "OpenAI", "openai", "https://api.openai.com", "secret", "USD")
	llmModel := model.NewLLMModel(provider.ID(), "org-1", "gpt-x", "gpt-4", "", 100, 200)
	backend := &countingProviderStore{provider: provider, llmModel: llmModel}
	return backend, NewProviderStore(backend, 128, time.Minute)
}

func TestProviderStoreCachesReads(t *testing.T) {
	backend, store := newTestStores()
	ctx := context.Background()
	id := backend.provider.ID()

	for range 3 {
		if _, err := store.GetProviderByID(ctx, id); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if backend.getProviderByID != 1 {
		t.Fatalf("expected 1 backend hit, got %d", backend.getProviderByID)
	}
}

func TestProviderStoreModelCachedByBothKeys(t *testing.T) {
	backend, store := newTestStores()
	ctx := context.Background()
	m := backend.llmModel

	// Priming via proxy name must also populate the by-ID index (same value,
	// two keys), so a subsequent by-ID lookup does not hit the backend.
	if _, err := store.GetLLMModelByProxyName(ctx, m.OrgID(), m.ProxyName()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := store.GetLLMModelByID(ctx, m.ID()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if backend.getModelByProxy != 1 {
		t.Fatalf("expected 1 proxy-name backend hit, got %d", backend.getModelByProxy)
	}
	if backend.getModelByID != 0 {
		t.Fatalf("expected 0 by-ID backend hits (served from cache), got %d", backend.getModelByID)
	}
}

func TestProviderStoreSaveInvalidates(t *testing.T) {
	backend, store := newTestStores()
	ctx := context.Background()
	id := backend.provider.ID()

	if _, err := store.GetProviderByID(ctx, id); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := store.SaveProvider(ctx, backend.provider); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := store.GetProviderByID(ctx, id); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if backend.getProviderByID != 2 {
		t.Fatalf("expected 2 backend hits (cache invalidated by save), got %d", backend.getProviderByID)
	}
}

func TestProviderStoreModelSaveInvalidatesProxyNameKey(t *testing.T) {
	backend, store := newTestStores()
	ctx := context.Background()
	m := backend.llmModel

	if _, err := store.GetLLMModelByProxyName(ctx, m.OrgID(), m.ProxyName()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := store.SaveLLMModel(ctx, m); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// After invalidation, the proxy-name key must miss and re-hit the backend.
	if _, err := store.GetLLMModelByProxyName(ctx, m.OrgID(), m.ProxyName()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if backend.getModelByProxy != 2 {
		t.Fatalf("expected 2 proxy-name backend hits (cache invalidated by save), got %d", backend.getModelByProxy)
	}
}

// Deleting a provider deletes its models in the database: their cached
// entries must go too.
func TestDeleteProviderDropsItsCachedModels(t *testing.T) {
	store := NewProviderStore(deletingBackend{}, 10, time.Hour)
	m := model.NewLLMModel("prov-1", "org-1", "fast", "fast-real", "", 0, 0)
	store.modelCache.Add(NewCacheableLLMModel(m))

	if err := store.DeleteProvider(context.Background(), "prov-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, cached := store.modelCache.Get(string(m.ID())); cached {
		t.Fatal("a model of the deleted provider is still cached")
	}
}

type deletingBackend struct{ port.ProviderStore }

func (deletingBackend) DeleteProvider(context.Context, model.ProviderID) error { return nil }

// A model whose ID key was evicted keeps its proxy-name key: saving it must
// drop that one too.
func TestSaveLLMModelDropsAnEntryWhoseIDKeyWasEvicted(t *testing.T) {
	store := NewProviderStore(savingBackend{}, 10, time.Hour)
	m := model.NewLLMModel("prov-1", "org-1", "fast", "fast-real", "", 0, 0)
	store.modelCache.Add(NewCacheableLLMModel(m))
	store.modelCache.cache.Remove(string(m.ID()))

	if err := store.SaveLLMModel(context.Background(), m); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, cached := store.modelCache.Get(getLLMModelProxyNameCacheKey("org-1", "fast")); cached {
		t.Fatal("the proxy-name key of the saved model is still cached")
	}
}

type savingBackend struct{ port.ProviderStore }

func (savingBackend) SaveLLMModel(context.Context, model.LLMModel) error { return nil }
