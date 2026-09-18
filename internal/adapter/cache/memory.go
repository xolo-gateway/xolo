package cache

import (
	"context"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// memoryEntry is a cached value with its own deadline.
//
// The expiry is held here rather than delegated to an expiring LRU because
// AddInt64 must not extend it: those caches renew the deadline on every write,
// so a counter under load would be refreshed forever and never re-read from the
// database — precisely the drift the TTL exists to bound.
type memoryEntry struct {
	value     int64
	expiresAt time.Time
}

func (e memoryEntry) expired(now time.Time) bool {
	return !e.expiresAt.IsZero() && now.After(e.expiresAt)
}

// MemoryCache is the in-process implementation of port.Cache: an LRU held by
// the gateway itself. It is the right choice for a single replica, and remains
// correct — if less effective — with several, since every entry it holds is
// recomputable from the database and ages out on its own.
type MemoryCache struct {
	entries *lru.Cache[string, memoryEntry]
	// The LRU is safe for concurrent use, but AddInt64 is a read-modify-write
	// over two of its calls and would otherwise lose increments under load —
	// exactly the load this cache exists to absorb.
	mu sync.Mutex
}

// NewMemoryCache returns a cache holding at most size entries. A size below one
// is raised to one, which the underlying LRU requires.
func NewMemoryCache(size int) *MemoryCache {
	if size < 1 {
		size = 1
	}
	// lru.New only fails on a non-positive size, which the guard above rules out.
	entries, _ := lru.New[string, memoryEntry](size)
	return &MemoryCache{entries: entries}
}

// GetInt64 implements port.Cache.
func (c *MemoryCache) GetInt64(ctx context.Context, key string) (int64, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, exists := c.entries.Get(key)
	if !exists {
		return 0, false, nil
	}
	if entry.expired(time.Now()) {
		c.entries.Remove(key)
		return 0, false, nil
	}
	return entry.value, true, nil
}

// SetInt64 implements port.Cache.
func (c *MemoryCache) SetInt64(ctx context.Context, key string, value int64, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry := memoryEntry{value: value}
	if ttl > 0 {
		entry.expiresAt = time.Now().Add(ttl)
	}
	c.entries.Add(key, entry)
	return nil
}

// AddInt64 implements port.Cache.
func (c *MemoryCache) AddInt64(ctx context.Context, key string, delta int64) (int64, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Peek only avoids reordering on the read itself; the Add below moves the
	// key to the front regardless. What this method does guarantee is that an
	// absent key stays absent, and that the entry keeps the deadline it was
	// created with.
	entry, exists := c.entries.Peek(key)
	if !exists {
		return 0, false, nil
	}
	if entry.expired(time.Now()) {
		c.entries.Remove(key)
		return 0, false, nil
	}

	entry.value += delta
	c.entries.Add(key, entry)
	return entry.value, true, nil
}

// Delete implements port.Cache.
func (c *MemoryCache) Delete(ctx context.Context, keys ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, key := range keys {
		c.entries.Remove(key)
	}
	return nil
}

var _ port.Cache = &MemoryCache{}
