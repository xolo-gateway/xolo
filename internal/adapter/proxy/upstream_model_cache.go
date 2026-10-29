package proxy

import (
	"sync"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// upstreamModelsCacheTTL is how long an organization's models are reused. The
// plan checks read from them which models share an upstream model and at which
// rate each fills a shared budget, which only change when a model is created,
// deleted or pointed at another upstream model: a plan check may meter on a
// list up to this old after such an edit.
const upstreamModelsCacheTTL = 30 * time.Second

// orgModelCache keeps the models of each organization. Without it a per-model
// or shared plan would list every model of the organization on each proxied
// request, a read the provider store cache passes through.
type orgModelCache struct {
	ttl time.Duration

	mu      sync.Mutex
	entries map[model.OrgID]orgModelEntry
	purgeAt int
}

type orgModelEntry struct {
	models    []model.LLMModel
	expiresAt time.Time
}

func newOrgModelCache(ttl time.Duration) *orgModelCache {
	return &orgModelCache{ttl: ttl, entries: make(map[model.OrgID]orgModelEntry), purgeAt: minPurgeAt}
}

func (c *orgModelCache) get(orgID model.OrgID, now time.Time) ([]model.LLMModel, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[orgID]
	if !ok || now.After(entry.expiresAt) {
		return nil, false
	}
	return entry.models, true
}

// put stores a list. Expired entries are swept once the map outgrows its last
// sweep, so deleted organizations do not accumulate.
func (c *orgModelCache) put(orgID model.OrgID, models []model.LLMModel, now time.Time) {
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
	c.entries[orgID] = orgModelEntry{models: models, expiresAt: now.Add(c.ttl)}
}

// minPurgeAt is the size below which the in-memory maps of this package are
// never swept.
const minPurgeAt = 64
