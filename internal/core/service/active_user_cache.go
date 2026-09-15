package service

import (
	"strconv"
	"sync"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// DefaultActiveUserCacheTTL is how long an active-user count is reused. The
// count is a denominator over a window of hours: it moves slowly, and a value a
// few seconds stale changes an allowance by at most one competitor.
const DefaultActiveUserCacheTTL = 30 * time.Second

// activeUserKey identifies one count: the plan (org and provider) and the
// window. It deliberately does not name a user. The count is taken over the
// whole plan and shared by everyone on it; whether the caller is among the
// counted is a separate, cheap presence check. Keying on the caller would pay
// the expensive count once per active user per TTL instead of once per plan.
//
// The window start is quantised to the TTL before it gets here. On a sliding
// window it is now−duration, a different instant on every request — down to the
// nanosecond and the monotonic clock reading that time.Time equality includes —
// and as a raw key it would make the cache write-only while still paying the
// count. Freshness is carried by expiresAt, not by the key; the key only has to
// tell windows apart.
type activeUserKey struct {
	orgID      model.OrgID
	providerID model.ProviderID
	bucket     int64 // window start in TTL-sized steps
}

// activeUserKeyFor builds the key for one count. Quantising a sliding window's
// start makes requests within one TTL of each other share an entry; the entry
// expires on its own before the drift exceeds the TTL.
func activeUserKeyFor(orgID model.OrgID, providerID model.ProviderID, since time.Time, ttl time.Duration) activeUserKey {
	bucket := since.UnixNano()
	if ttl > 0 {
		bucket /= int64(ttl)
	}
	return activeUserKey{orgID: orgID, providerID: providerID, bucket: bucket}
}

// flightKey is the singleflight key for one count: the same identity as the
// cache key, spelled as a string.
func (k activeUserKey) flightKey() string {
	return string(k.orgID) + "|" + string(k.providerID) + "|" + strconv.FormatInt(k.bucket, 10)
}

type activeUserEntry struct {
	count     int64
	degraded  bool // the count could not be read; count is meaningless
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
	// purgeAt is the size past which the next put sweeps expired entries. It is
	// doubled after each sweep so the cost stays amortised O(1) per insertion
	// instead of walking the whole map under the lock every time.
	purgeAt int
}

const minPurgeAt = 64

func newActiveUserCache(ttl time.Duration) *activeUserCache {
	return &activeUserCache{
		ttl:     ttl,
		entries: make(map[activeUserKey]activeUserEntry),
		purgeAt: minPurgeAt,
	}
}

// get returns an entry still within its TTL. A degraded entry records that the
// count failed recently: the caller falls back without asking the store again,
// so a database that cannot answer the count is not asked to on every request.
func (c *activeUserCache) get(key activeUserKey, now time.Time) (entry activeUserEntry, ok bool) {
	if c == nil || c.ttl <= 0 {
		return activeUserEntry{}, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok = c.entries[key]
	if !ok || now.After(entry.expiresAt) {
		return activeUserEntry{}, false
	}
	return entry, true
}

// put stores a count. Keys carry a window bucket, so they stop being written
// to on their own and would accumulate one set per elapsed bucket; expired
// entries are swept once the map outgrows its last sweep, which keeps the
// sweeping cost proportional to the insertions rather than to the map size.
func (c *activeUserCache) put(key activeUserKey, entry activeUserEntry, now time.Time) {
	if c == nil || c.ttl <= 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.entries) >= c.purgeAt {
		for k, e := range c.entries {
			if now.After(e.expiresAt) {
				delete(c.entries, k)
			}
		}
		c.purgeAt = max(2*len(c.entries), minPurgeAt)
	}

	entry.expiresAt = now.Add(c.ttl)
	c.entries[key] = entry
}

// size reports how many entries the cache holds, for tests.
func (c *activeUserCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
