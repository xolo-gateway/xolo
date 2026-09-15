package service

import (
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

func TestActiveUserCache_SweepsExpiredEntriesWithoutWalkingOnEveryPut(t *testing.T) {
	// Keys carry a window bucket, so a sliding window writes a new key every TTL
	// and would grow the map forever. Expired entries must go — but in amortised
	// sweeps, not by walking the whole map under the lock on each insertion.
	const ttl = 30 * time.Second
	c := newActiveUserCache(ttl)

	now := time.Unix(1_700_000_000, 0)
	// Write far more entries than the sweep threshold, each in its own bucket
	// and each already expired by the time the next batch arrives.
	for i := 0; i < 10*minPurgeAt; i++ {
		at := now.Add(time.Duration(i) * ttl)
		key := activeUserKeyFor("org", "prov", at, model.UserID("u"), ttl)
		c.put(key, int64(i), at)
	}

	// Only entries still within their TTL may remain: the sweep keeps the map
	// bounded by a small multiple of the live set, not by the total written.
	if size := c.size(); size > 2*minPurgeAt {
		t.Errorf("cache holds %d entries after writing %d expiring ones, want it bounded near the live set", size, 10*minPurgeAt)
	}
}

func TestActiveUserKeyFor_QuantisesTheWindowStart(t *testing.T) {
	const ttl = 30 * time.Second
	// Aligned on a bucket boundary: quantisation tolerates one miss per crossed
	// boundary, so the instants compared here must sit inside the same step.
	base := time.Unix(1_700_000_000, 0).Truncate(ttl)

	same := activeUserKeyFor("org", "prov", base.Add(time.Second), "u", ttl)
	if got := activeUserKeyFor("org", "prov", base.Add(20*time.Second), "u", ttl); got != same {
		t.Errorf("two starts within one TTL produced different keys: %+v vs %+v", same, got)
	}
	if got := activeUserKeyFor("org", "prov", base.Add(2*ttl), "u", ttl); got == same {
		t.Error("two starts a TTL apart produced the same key")
	}
	if got := activeUserKeyFor("org", "prov", base.Add(time.Second), "other", ttl); got == same {
		t.Error("two excluded users produced the same key")
	}
}
