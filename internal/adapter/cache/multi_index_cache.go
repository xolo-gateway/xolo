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

func (c *MultiIndexCache[V]) Remove(key string) {
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
func (c *MultiIndexCache[V]) RemoveMatching(matches func(V) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	for _, key := range c.cache.Keys() {
		if item, ok := c.cache.Peek(key); ok && matches(item) {
			c.cache.Remove(key)
		}
	}
}
