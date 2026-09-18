package cache

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// UsageStore caches the budget totals read on the proxy hot path.
//
// The totals themselves are already cheap — they come from the running
// per-day counters, not from the usage history — but they are read up to six
// times per proxied request, and the organization total is the same for every
// member. Holding them for a few seconds turns that into one read per window
// per TTL, and keeps the increments applied in place so a budget about to be
// exhausted is not seen as a stale figure for the whole TTL.
//
// Every other method is delegated untouched: reports read the usage records
// themselves and are not on the hot path.
type UsageStore struct {
	port.UsageStore

	cache port.Cache
	ttl   time.Duration

	// loading tracks the keys whose total is being read from the backend, and
	// whether a record landed while that read was in flight. Without it a total
	// read before a record was written could be stored after that record's
	// increment was applied, counting it twice and refusing requests a fresh
	// read would allow.
	//
	// mu also guards the cache writes themselves, so that marking a load stale
	// and applying its increment happen together, and so does deciding a load is
	// fresh and storing its result. Split across two critical sections, a record
	// landing in between would be counted twice or not at all. The mutex is held
	// only around cache operations, never around a backend read.
	mu      sync.Mutex
	loading map[string]*keyLoad
}

// keyLoad counts the readers loading one key and remembers whether the value
// they are about to store is already out of date.
type keyLoad struct {
	readers int
	stale   bool
}

// NewUsageStore wraps backend, caching budget totals for ttl in cache.
func NewUsageStore(backend port.UsageStore, cache port.Cache, ttl time.Duration) *UsageStore {
	return &UsageStore{
		UsageStore: backend,
		cache:      cache,
		ttl:        ttl,
		loading:    map[string]*keyLoad{},
	}
}

// SumQuotaCostSince implements port.UsageStore.
func (s *UsageStore) SumQuotaCostSince(ctx context.Context, scope model.QuotaScope, scopeID string, orgID model.OrgID, since time.Time) (int64, error) {
	key := quotaSumCacheKey(scope, scopeID, orgID, since)

	if total, exists, err := s.cache.GetInt64(ctx, key); err == nil && exists {
		return total, nil
	} else if err != nil {
		slog.WarnContext(ctx, "quota sum cache read failed, falling back to the store", slog.Any("error", err), slog.String("key", key))
	}

	s.beginLoad(key)
	total, err := s.UsageStore.SumQuotaCostSince(ctx, scope, scopeID, orgID, since)
	s.finishLoad(ctx, key, total, err == nil)
	if err != nil {
		return 0, err
	}

	return total, nil
}

// beginLoad registers a backend read for key.
func (s *UsageStore) beginLoad(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	load, exists := s.loading[key]
	if !exists {
		load = &keyLoad{}
		s.loading[key] = load
	}
	load.readers++
}

// finishLoad closes a backend read and stores its result, unless a record
// landed while it was in flight.
//
// A record written during the read is already counted by the backend, or will
// be by the next read, but storing this value now would race with its
// increment. The key is left absent instead, and the next check reads a fresh
// total. The decision and the write share one critical section with
// RecordUsage's, so a record cannot slip between them.
func (s *UsageStore) finishLoad(ctx context.Context, key string, total int64, store bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	load, exists := s.loading[key]
	if exists {
		load.readers--
		if load.readers <= 0 {
			delete(s.loading, key)
		}
	}

	if !store || !exists || load.stale {
		return
	}

	if err := s.cache.SetInt64(ctx, key, total, s.ttl); err != nil {
		slog.WarnContext(ctx, "quota sum cache write failed", slog.Any("error", err), slog.String("key", key))
	}
}

// applyRecordedCost adds cost to the cached total for key and tells the readers
// currently loading it that the value they are about to store is already out of
// date. Both happen under one lock, for the reason finishLoad describes.
func (s *UsageStore) applyRecordedCost(ctx context.Context, key string, cost int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if load, exists := s.loading[key]; exists {
		load.stale = true
	}

	_, _, err := s.cache.AddInt64(ctx, key, cost)
	return err
}

// RecordUsage implements port.UsageStore. Once the record is persisted, the
// cached totals it affects are incremented in place. Only entries already
// cached are touched: an increment on its own says nothing about a total
// nobody has read yet, and the next read loads it from the counters, this
// record included.
func (s *UsageStore) RecordUsage(ctx context.Context, record model.UsageRecord) error {
	if err := s.UsageStore.RecordUsage(ctx, record); err != nil {
		return err
	}

	for _, key := range quotaSumCacheKeysFor(record) {
		if err := s.applyRecordedCost(ctx, key, record.Cost()); err != nil {
			// A failed increment only costs a stale total until the entry
			// expires, so it is reported rather than propagated: the usage
			// record itself is already written.
			slog.WarnContext(ctx, "quota sum cache increment failed", slog.Any("error", err), slog.String("key", key))
		}
	}

	return nil
}

// quotaSumCacheKey names the entry holding one scope's spending over one budget
// window. The window start is what makes an entry roll over on its own at
// midnight, on the first of the month and on new year's day, with no
// invalidation to schedule.
//
// The bound is spelled out to the second even though the counters behind it
// have day granularity: two windows opening on the same day are different
// questions, and a key that conflated them would answer one with the other for
// the length of a TTL.
func quotaSumCacheKey(scope model.QuotaScope, scopeID string, orgID model.OrgID, since time.Time) string {
	return strings.Join([]string{
		"quota", "sum", string(scope), scopeID, string(orgID), since.UTC().Format(time.RFC3339),
	}, ":")
}

// quotaSumCacheKeysFor lists the cached totals a usage record contributes to:
// the current day, month and year of both the organization and, when the call
// was made by a user rather than an application, that user.
//
// Subscription-covered records consume no monetary budget and are left out, as
// they are by the counters themselves.
func quotaSumCacheKeysFor(record model.UsageRecord) []string {
	if !model.FeedsMonetaryBudget(record) {
		return nil
	}

	createdAt := record.CreatedAt()
	starts := []time.Time{
		model.StartOfDay(createdAt),
		model.StartOfMonth(createdAt),
		model.StartOfYear(createdAt),
	}

	keys := make([]string, 0, len(starts)*2)
	for _, start := range starts {
		keys = append(keys, quotaSumCacheKey(model.QuotaScopeOrg, string(record.OrgID()), record.OrgID(), start))
		if userID := record.UserID(); userID != "" {
			keys = append(keys, quotaSumCacheKey(model.QuotaScopeUser, string(userID), record.OrgID(), start))
		}
	}

	return keys
}

var _ port.UsageStore = &UsageStore{}
