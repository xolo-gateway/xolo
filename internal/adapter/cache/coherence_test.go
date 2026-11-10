package cache

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// sharedGenerations stands for the database the instances share.
type sharedGenerations struct {
	mu          sync.Mutex
	generations map[string]int64
	bumps       int
}

func (s *sharedGenerations) Bump(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generations[name]++
	s.bumps++
	return nil
}

func (s *sharedGenerations) Generations(context.Context) (map[string]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := make(map[string]int64, len(s.generations))
	for name, generation := range s.generations {
		copied[name] = generation
	}
	return copied, nil
}

type entry struct{ keys []string }

func (e entry) CacheKeys() []string { return e.keys }

// instance is one process: its own cache, registered with its own coherence.
type instance struct {
	cache     *MultiIndexCache[entry]
	coherence *Coherence
}

func newInstance(t *testing.T, store GenerationStore) instance {
	t.Helper()
	i := instance{
		cache:     NewMultiIndexCache[entry](10, time.Hour),
		coherence: NewCoherence(store, time.Hour),
	}
	share(i.coherence, "tokens", i.cache)
	if err := i.coherence.sync(context.Background()); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	return i
}

func TestCoherenceFlushesTheOtherInstances(t *testing.T) {
	store := &sharedGenerations{generations: map[string]int64{}}
	a, b := newInstance(t, store), newInstance(t, store)

	token := entry{keys: []string{"tok-1", "hash-1"}}
	a.cache.Add(token)
	b.cache.Add(token)

	// Revoked on A, which never cached it under that key on B's side: the
	// invalidation must reach B even though A only removes its own copy.
	a.cache.Remove("tok-1")

	if _, ok := b.cache.Get("hash-1"); !ok {
		t.Fatal("B lost the entry before polling")
	}
	if err := b.coherence.sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if _, ok := b.cache.Get("hash-1"); ok {
		t.Fatal("B still serves the revoked entry after polling")
	}

	// A key A never held is still announced: another instance may hold it.
	b.cache.Add(token)
	a.cache.Remove("unknown-on-a")
	_ = b.coherence.sync(context.Background())
	if _, ok := b.cache.Get("tok-1"); ok {
		t.Fatal("an invalidation of a key absent from A did not reach B")
	}
}

func TestCoherenceFlushesOnPredicateRemoval(t *testing.T) {
	store := &sharedGenerations{generations: map[string]int64{}}
	a, b := newInstance(t, store), newInstance(t, store)

	b.cache.Add(entry{keys: []string{"tok-2"}})
	a.cache.RemoveMatching(func(entry) bool { return true })
	_ = b.coherence.sync(context.Background())

	if _, ok := b.cache.Get("tok-2"); ok {
		t.Fatal("a predicate removal on A did not flush B")
	}
}

func TestCoherenceLocalRemovalIsNotAnnounced(t *testing.T) {
	store := &sharedGenerations{generations: map[string]int64{}}
	a := newInstance(t, store)

	a.cache.Add(entry{keys: []string{"tok-3"}})
	a.cache.removeLocal("tok-3")

	if store.bumps != 0 {
		t.Fatalf("a local removal bumped the generation %d time(s)", store.bumps)
	}
}

// A read that started before the flush must not put back the entry it loaded
// before the change.
func TestCoherenceFlushRejectsReadsStartedBefore(t *testing.T) {
	store := &sharedGenerations{generations: map[string]int64{}}
	a, b := newInstance(t, store), newInstance(t, store)

	generation := b.cache.Generation()
	a.cache.Remove("tok-4")
	_ = b.coherence.sync(context.Background())

	b.cache.AddIfGeneration(entry{keys: []string{"tok-4"}}, generation)
	if _, ok := b.cache.Get("tok-4"); ok {
		t.Fatal("a read started before the flush repopulated the cache")
	}
}

func TestCoherenceFirstSyncFlushes(t *testing.T) {
	store := &sharedGenerations{generations: map[string]int64{"tokens": 7}}
	cache := NewMultiIndexCache[entry](10, time.Hour)
	coherence := NewCoherence(store, time.Hour)
	share(coherence, "tokens", cache)

	cache.Add(entry{keys: []string{"tok-5"}})
	_ = coherence.sync(context.Background())
	if _, ok := cache.Get("tok-5"); ok {
		t.Fatal("the first sync kept entries loaded before any generation was known")
	}

	cache.Add(entry{keys: []string{"tok-5"}})
	_ = coherence.sync(context.Background())
	if _, ok := cache.Get("tok-5"); !ok {
		t.Fatal("a sync without any change flushed the cache")
	}
}

func TestCacheWithoutCoherenceStaysLocal(t *testing.T) {
	cache := NewMultiIndexCache[entry](10, time.Hour)
	share[entry](nil, "tokens", cache)
	cache.Add(entry{keys: []string{"tok-6"}})
	cache.Remove("tok-6")
	if _, ok := cache.Get("tok-6"); ok {
		t.Fatal("remove did not remove")
	}
}

type fakeProviderBackend struct {
	port.ProviderStore
	read func() model.Provider
}

func (b fakeProviderBackend) GetProviderByID(context.Context, model.ProviderID) (model.Provider, error) {
	return b.read(), nil
}

// A provider read in flight on B while A changes the provider must not put the
// old provider back after B flushed: B would use the old key or URL until the
// TTL expires.
func TestCoherenceProviderReadStraddlingAFlush(t *testing.T) {
	store := &sharedGenerations{generations: map[string]int64{}}

	var b *ProviderStore
	var coherenceB *Coherence
	stale := model.NewProvider("org-1", "openai", "openai", "https://old.example", "old-key", "EUR")
	b = NewProviderStore(fakeProviderBackend{read: func() model.Provider {
		// While the read runs: A commits the change, B polls and flushes.
		_ = store.Bump(context.Background(), "providers")
		if err := coherenceB.sync(context.Background()); err != nil {
			t.Fatalf("sync: %v", err)
		}
		return stale
	}}, 10, time.Hour)
	coherenceB = NewCoherence(store, time.Hour)
	b.ShareInvalidations(coherenceB)
	_ = coherenceB.sync(context.Background())

	if _, err := b.GetProviderByID(context.Background(), stale.ID()); err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, cached := b.providerCache.Get(string(stale.ID())); cached {
		t.Fatal("a read started before the flush cached the old provider")
	}
}

// The same holds for a local invalidation: Remove moves the generation too.
func TestRemoveRejectsReadsStartedBefore(t *testing.T) {
	cache := NewMultiIndexCache[entry](10, time.Hour)
	generation := cache.Generation()
	cache.Remove("tok-7")
	cache.AddIfGeneration(entry{keys: []string{"tok-7"}}, generation)
	if _, ok := cache.Get("tok-7"); ok {
		t.Fatal("a read started before the removal repopulated the cache")
	}
}

// Removing by a key the LRU already evicted still drops the item's other keys:
// a revoked token must not stay reachable by its hash.
func TestRemoveAfterEvictionDropsTheOtherKeys(t *testing.T) {
	cache := NewMultiIndexCache[entry](10, time.Hour)
	cache.Add(entry{keys: []string{"tok-8", "hash-8"}})
	cache.cache.Remove("tok-8")

	cache.Remove("tok-8")
	if _, ok := cache.Get("hash-8"); ok {
		t.Fatal("the hash key of a removed token is still cached")
	}
}
