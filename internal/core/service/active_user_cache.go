package service

import (
	"sync"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// DefaultActiveUserCacheTTL is how long an active-user count is reused. The
// count is a denominator over a window of hours: it moves slowly, and a value a
// few seconds stale changes an allowance by at most one competitor.
const DefaultActiveUserCacheTTL = 30 * time.Second

// activeUserKey identifies one count. The window start is part of it, so a new
// window never reads a previous window's count, and the excluded user too, since
// the count is taken without them.
type activeUserKey struct {
	orgID      model.OrgID
	providerID model.ProviderID
	since      time.Time
	excluded   model.UserID
}

type activeUserEntry struct {
	count     int64
	expiresAt time.Time
}

// activeUserCache holds active-user counts for a short while.
//
// The count is a COUNT(DISTINCT user_id) over the whole window, which neither
// SQLite nor PostgreSQL can answer without walking the range — on a weekly
// window of a busy org, tens of thousands of index entries, on every proxy
// request. Caching is safe precisely because the allocator already treats this
// count as an optimisation: losing it degrades to the whole-membership share.
type activeUserCache struct {
	ttl time.Duration

	mu      sync.Mutex
	entries map[activeUserKey]activeUserEntry
}

func newActiveUserCache(ttl time.Duration) *activeUserCache {
	return &activeUserCache{ttl: ttl, entries: make(map[activeUserKey]activeUserEntry)}
}

// get returns a count still within its TTL.
func (c *activeUserCache) get(key activeUserKey, now time.Time) (int64, bool) {
	if c == nil || c.ttl <= 0 {
		return 0, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[key]
	if !ok || now.After(entry.expiresAt) {
		return 0, false
	}
	return entry.count, true
}

// put stores a count, dropping the entries that have expired along the way:
// keys carry a window start, so they stop being written to on their own and
// would otherwise accumulate one set per elapsed window.
func (c *activeUserCache) put(key activeUserKey, count int64, now time.Time) {
	if c == nil || c.ttl <= 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for k, e := range c.entries {
		if now.After(e.expiresAt) {
			delete(c.entries, k)
		}
	}

	c.entries[key] = activeUserEntry{count: count, expiresAt: now.Add(c.ttl)}
}
