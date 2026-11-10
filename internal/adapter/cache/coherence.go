package cache

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/pkg/errors"
)

// GenerationStore keeps one counter per cache in the database every instance
// shares. Bump moves the counter of a cache after one of its entries changed;
// Generations reads them all.
type GenerationStore interface {
	Bump(ctx context.Context, name string) error
	Generations(ctx context.Context) (map[string]int64, error)
}

type flushable interface {
	Flush()
}

// bumpTimeout bounds the write telling the other instances. It runs after the
// change it announces has committed, on the writer's path.
const bumpTimeout = 2 * time.Second

// Coherence keeps the in-process caches of several instances consistent.
//
// An invalidation on one instance bumps the counter of its cache; every
// instance polls the counters and flushes a cache whose counter moved. The
// caches are small and their writes rare next to their reads, so flushing a
// whole cache is cheaper than tracking keys, and it also covers the
// predicate-based invalidations, which cannot be replayed elsewhere.
//
// An entry changed on one instance is therefore served stale by the others
// for at most one poll interval, instead of up to the cache TTL. An instance
// flushes its own caches too when it sees its own bump: harmless, the next
// reads reload them.
type Coherence struct {
	store    GenerationStore
	interval time.Duration

	mu     sync.Mutex
	caches map[string][]flushable
	seen   map[string]int64
	synced bool
}

func NewCoherence(store GenerationStore, interval time.Duration) *Coherence {
	return &Coherence{
		store:    store,
		interval: interval,
		caches:   map[string][]flushable{},
		seen:     map[string]int64{},
	}
}

// share registers cache under name and hooks its invalidations. A nil
// coherence leaves the cache local to the instance.
func share[V Cacheable](coherence *Coherence, name string, cache *MultiIndexCache[V]) {
	if coherence == nil {
		return
	}

	coherence.mu.Lock()
	coherence.caches[name] = append(coherence.caches[name], cache)
	coherence.mu.Unlock()

	cache.invalidated = func() { coherence.invalidated(name) }
}

func (c *Coherence) invalidated(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), bumpTimeout)
	defer cancel()

	if err := c.store.Bump(ctx, name); err != nil {
		// The change itself has committed: the other instances serve the old
		// entry until its TTL expires.
		slog.ErrorContext(ctx, "could not tell the other instances a cache entry changed", slog.String("cache", name), slog.Any("error", errors.WithStack(err)))
	}
}

// Run polls the counters until ctx is done.
func (c *Coherence) Run(ctx context.Context) error {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		if err := c.sync(ctx); err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "could not read the cache generations", slog.Any("error", errors.WithStack(err)))
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// sync flushes the caches whose counter moved since the previous poll. The
// first poll flushes everything: what the caches hold was loaded before any
// counter was known.
func (c *Coherence) sync(ctx context.Context) error {
	generations, err := c.store.Generations(ctx)
	if err != nil {
		return errors.WithStack(err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for name, caches := range c.caches {
		generation := generations[name]
		if c.synced && c.seen[name] == generation {
			continue
		}
		c.seen[name] = generation
		for _, cache := range caches {
			cache.Flush()
		}
	}
	c.synced = true

	return nil
}
