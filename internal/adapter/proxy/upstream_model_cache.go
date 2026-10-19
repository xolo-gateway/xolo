package proxy

import (
	"sync"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// upstreamModelsCacheTTL is how long the models sharing an upstream model are
// reused. The grouping only changes when a model is created, deleted or
// pointed at another upstream model, so a plan check may meter on a grouping
// up to this old after such an edit.
const upstreamModelsCacheTTL = 30 * time.Second

// upstreamModelCache keeps, per requested model, the models of its provider
// that target the same upstream model. Without it a per-model plan would list
// every model of the organization on each proxied request, a read the provider
// store cache passes through.
type upstreamModelCache struct {
	ttl time.Duration

	mu      sync.Mutex
	entries map[model.LLMModelID]upstreamModelEntry
	purgeAt int
}

type upstreamModelEntry struct {
	ids       []model.LLMModelID
	expiresAt time.Time
}

func newUpstreamModelCache(ttl time.Duration) *upstreamModelCache {
	return &upstreamModelCache{ttl: ttl, entries: make(map[model.LLMModelID]upstreamModelEntry), purgeAt: minPurgeAt}
}

func (c *upstreamModelCache) get(id model.LLMModelID, now time.Time) ([]model.LLMModelID, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[id]
	if !ok || now.After(entry.expiresAt) {
		return nil, false
	}
	return entry.ids, true
}

// put stores a grouping. Expired entries are swept once the map outgrows its
// last sweep, so deleted models do not accumulate.
func (c *upstreamModelCache) put(id model.LLMModelID, ids []model.LLMModelID, now time.Time) {
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
	c.entries[id] = upstreamModelEntry{ids: ids, expiresAt: now.Add(c.ttl)}
}

// minPurgeAt is the size below which the in-memory maps of this package are
// never swept.
const minPurgeAt = 64
