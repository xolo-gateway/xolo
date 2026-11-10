package cache

import (
	"sync"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
)

type Cacheable interface {
	CacheKeys() []string
}

type MultiIndexCache[V Cacheable] struct {
	cache      *expirable.LRU[string, V]
	mu         sync.RWMutex
	generation uint64
	// invalidated, when set, tells the other instances an entry changed (see
	// Coherence). It runs after every invalidation, outside the lock.
	invalidated func()
}

func NewMultiIndexCache[V Cacheable](size int, ttl time.Duration) *MultiIndexCache[V] {
	cache := expirable.NewLRU[string, V](size, nil, ttl)
	return &MultiIndexCache[V]{
		cache: cache,
	}
}

func (c *MultiIndexCache[V]) Add(item V) {
	c.mu.Lock()
	defer c.mu.Unlock()

	keys := item.CacheKeys()
	for _, key := range keys {
		c.cache.Add(key, item)
	}
}

func (c *MultiIndexCache[V]) Get(key string) (V, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.cache.Get(key)
}

// Remove drops the entry under key, by any of its keys, and tells the other
// instances: they may hold the entry even when this one does not.
func (c *MultiIndexCache[V]) Remove(key string) {
	c.removeLocal(key)

	if c.invalidated != nil {
		c.invalidated()
	}
}

// removeLocal drops the entry on this instance only, for a removal every
// instance decides on its own, such as an expired token.
func (c *MultiIndexCache[V]) removeLocal(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	val, ok := c.cache.Peek(key)
	if !ok {
		return
	}

	allKeys := val.CacheKeys()

	for _, k := range allKeys {
		c.cache.Remove(k)
	}
}

func (c *MultiIndexCache[V]) Len() int {
	return c.cache.Len()
}

func (c *MultiIndexCache[V]) Generation() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.generation
}

// AddIfGeneration prevents a read started before invalidation from repopulating
// the cache with a stale result after the transaction has committed.
func (c *MultiIndexCache[V]) AddIfGeneration(item V, generation uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if generation != c.generation {
		return
	}
	for _, key := range item.CacheKeys() {
		c.cache.Add(key, item)
	}
}

// RemoveMatching also removes secondary keys when the primary key was evicted.
// The other instances cannot run the predicate on their entries: they flush
// the whole cache instead.
func (c *MultiIndexCache[V]) RemoveMatching(matches func(V) bool) {
	c.mu.Lock()
	c.generation++
	for _, key := range c.cache.Keys() {
		if item, ok := c.cache.Peek(key); ok && matches(item) {
			c.cache.Remove(key)
		}
	}
	c.mu.Unlock()

	if c.invalidated != nil {
		c.invalidated()
	}
}

// Flush empties the cache after another instance changed one of its entries.
// The generation moves too, so a read started before the flush does not put
// back what it loaded from the database before the change.
func (c *MultiIndexCache[V]) Flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	c.cache.Purge()
}
