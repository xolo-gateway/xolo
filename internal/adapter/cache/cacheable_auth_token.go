package cache

import (
	"github.com/xolo-gateway/xolo/internal/core/model"
)

type CacheableAuthToken struct {
	model.AuthToken
	// lookupKey is the hash of the key presented by the caller. It is passed in
	// rather than derived from Value(), whose content depends on the backend:
	// the gorm store returns the stored hash, an in-memory store the clear-text
	// value. Indexing on an explicit key keeps lookups correct either way, and
	// keeps the clear-text key out of the cache.
	lookupKey string
}

// CacheKeys implements [Cacheable].
func (t *CacheableAuthToken) CacheKeys() []string {
	return []string{
		t.lookupKey,
		string(t.ID()),
	}
}

func NewCacheableAuthToken(authToken model.AuthToken, lookupKey string) *CacheableAuthToken {
	return &CacheableAuthToken{AuthToken: authToken, lookupKey: lookupKey}
}

var (
	_ model.AuthToken = &CacheableAuthToken{}
	_ Cacheable       = &CacheableAuthToken{}
)
