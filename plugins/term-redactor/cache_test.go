package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeFetcher struct {
	mu    sync.Mutex
	calls int
	terms []termEntry
	err   error
}

func (f *fakeFetcher) FetchTerms(_ context.Context, _ Config, _ string) ([]termEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.terms, nil
}

func (f *fakeFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func testConfig() Config {
	cfg := defaultConfig()
	cfg.CacheTTLSeconds = 100
	cfg.MinRetryIntervalSeconds = 30
	return cfg
}

func TestTermCache_FreshCacheServedWithoutRefetch(t *testing.T) {
	fetcher := &fakeFetcher{terms: []termEntry{{UUID: "u1", Name: "Jean Dupont"}}}
	c := newTermCache(fetcher)
	now := time.Now()
	c.now = func() time.Time { return now }

	cfg := testConfig()

	entry, stale, err := c.resolveTerms(context.Background(), "org-1", "node-1", cfg, "")
	if err != nil {
		t.Fatalf("resolveTerms: %v", err)
	}
	if stale {
		t.Error("expected fresh entry on first fetch")
	}
	if len(entry.terms) != 1 {
		t.Fatalf("expected 1 term, got %d", len(entry.terms))
	}

	// Still within TTL: no second fetch.
	now = now.Add(50 * time.Second)
	if _, _, err := c.resolveTerms(context.Background(), "org-1", "node-1", cfg, ""); err != nil {
		t.Fatalf("resolveTerms: %v", err)
	}
	if got := fetcher.callCount(); got != 1 {
		t.Errorf("fetch calls = %d, want 1 (served from cache within TTL)", got)
	}
}

func TestTermCache_StaleFetchFailureServesStaleCache(t *testing.T) {
	fetcher := &fakeFetcher{terms: []termEntry{{UUID: "u1", Name: "Jean Dupont"}}}
	c := newTermCache(fetcher)
	now := time.Now()
	c.now = func() time.Time { return now }

	cfg := testConfig()
	cfg.CacheTTLSeconds = 10
	cfg.MinRetryIntervalSeconds = 5

	if _, _, err := c.resolveTerms(context.Background(), "org-1", "node-1", cfg, ""); err != nil {
		t.Fatalf("initial resolveTerms: %v", err)
	}

	// TTL expired and past the retry backoff: a refetch is attempted, fails,
	// but the existing (now stale) entry must still be served.
	now = now.Add(20 * time.Second)
	fetcher.err = errors.New("api unreachable")

	entry, stale, err := c.resolveTerms(context.Background(), "org-1", "node-1", cfg, "")
	if err != nil {
		t.Fatalf("resolveTerms should fail open to the stale cache, got error: %v", err)
	}
	if !stale {
		t.Error("expected stale=true when serving a cache that failed to refresh")
	}
	if entry == nil || len(entry.terms) != 1 {
		t.Fatalf("expected the stale entry to still be returned, got %+v", entry)
	}
	if got := fetcher.callCount(); got != 2 {
		t.Errorf("fetch calls = %d, want 2 (one initial, one failed refresh attempt)", got)
	}
}

func TestTermCache_BackoffSkipsRefetchAfterFailure(t *testing.T) {
	fetcher := &fakeFetcher{terms: []termEntry{{UUID: "u1", Name: "Jean Dupont"}}}
	c := newTermCache(fetcher)
	now := time.Now()
	c.now = func() time.Time { return now }

	cfg := testConfig()
	cfg.CacheTTLSeconds = 10
	cfg.MinRetryIntervalSeconds = 15

	if _, _, err := c.resolveTerms(context.Background(), "org-1", "node-1", cfg, ""); err != nil {
		t.Fatalf("initial resolveTerms: %v", err)
	}

	now = now.Add(20 * time.Second) // TTL expired, and past the 15s backoff floor
	fetcher.err = errors.New("api unreachable")
	if _, _, err := c.resolveTerms(context.Background(), "org-1", "node-1", cfg, ""); err != nil {
		t.Fatalf("resolveTerms: %v", err)
	}
	if got := fetcher.callCount(); got != 2 {
		t.Fatalf("fetch calls = %d, want 2 after the failed refresh attempt", got)
	}

	// Still stale, but well within the retry backoff window: must not hit
	// the external API again.
	now = now.Add(5 * time.Second)
	entry, stale, err := c.resolveTerms(context.Background(), "org-1", "node-1", cfg, "")
	if err != nil {
		t.Fatalf("resolveTerms: %v", err)
	}
	if !stale || entry == nil {
		t.Fatalf("expected the stale cache to still be served, got entry=%v stale=%v", entry, stale)
	}
	if got := fetcher.callCount(); got != 2 {
		t.Errorf("fetch calls = %d, want 2 (backoff should have skipped this attempt)", got)
	}
}

func TestTermCache_NoCacheAndFetchFailure_IsFailOpen(t *testing.T) {
	fetcher := &fakeFetcher{err: errors.New("api unreachable")}
	c := newTermCache(fetcher)
	c.now = time.Now

	entry, _, err := c.resolveTerms(context.Background(), "org-1", "node-1", testConfig(), "")
	if err == nil {
		t.Fatal("expected an error when there is no cache at all and the fetch fails")
	}
	if entry != nil {
		t.Errorf("expected a nil entry, got %+v", entry)
	}
}

func TestTermCache_ColdBackoffSkipsRepeatedFailedFetch(t *testing.T) {
	fetcher := &fakeFetcher{err: errors.New("api unreachable")}
	c := newTermCache(fetcher)
	now := time.Now()
	c.now = func() time.Time { return now }

	cfg := testConfig()
	cfg.MinRetryIntervalSeconds = 30

	if _, _, err := c.resolveTerms(context.Background(), "org-1", "node-1", cfg, ""); err == nil {
		t.Fatal("expected the first call to fail (no cache, fetch error)")
	}
	if got := fetcher.callCount(); got != 1 {
		t.Fatalf("fetch calls = %d, want 1", got)
	}

	// Still within the backoff window and still no cache at all: must not
	// hit the external API again on every request while it's down.
	now = now.Add(5 * time.Second)
	if _, _, err := c.resolveTerms(context.Background(), "org-1", "node-1", cfg, ""); err == nil {
		t.Fatal("expected resolveTerms to keep failing open while backing off")
	}
	if got := fetcher.callCount(); got != 1 {
		t.Errorf("fetch calls = %d, want 1 (backoff should have skipped this attempt)", got)
	}
}

func TestTermCache_RecoversAfterBackoffWindow(t *testing.T) {
	fetcher := &fakeFetcher{err: errors.New("boom"), terms: []termEntry{{UUID: "u1", Name: "Jean Dupont"}}}
	c := newTermCache(fetcher)
	now := time.Now()
	c.now = func() time.Time { return now }

	cfg := testConfig()
	cfg.CacheTTLSeconds = 10
	cfg.MinRetryIntervalSeconds = 5

	// First call: no cache yet, fetch fails -> fail open.
	if _, _, err := c.resolveTerms(context.Background(), "org-1", "node-1", cfg, ""); err == nil {
		t.Fatal("expected the first call to fail (no cache, fetch error)")
	}

	// Past the backoff window: retried, this time it succeeds.
	now = now.Add(6 * time.Second)
	fetcher.err = nil
	entry, stale, err := c.resolveTerms(context.Background(), "org-1", "node-1", cfg, "")
	if err != nil {
		t.Fatalf("resolveTerms: %v", err)
	}
	if stale {
		t.Error("a successful fetch should not be reported as stale")
	}
	if entry == nil || len(entry.terms) != 1 {
		t.Fatalf("expected the recovered entry to carry the fetched terms, got %+v", entry)
	}
}

func TestTermCache_DifferentNodesAreCachedIndependently(t *testing.T) {
	fetcher := &fakeFetcher{terms: []termEntry{{UUID: "u1", Name: "Jean Dupont"}}}
	c := newTermCache(fetcher)
	c.now = time.Now

	cfg := testConfig()
	if _, _, err := c.resolveTerms(context.Background(), "org-1", "node-A", cfg, ""); err != nil {
		t.Fatalf("resolveTerms: %v", err)
	}
	if _, _, err := c.resolveTerms(context.Background(), "org-1", "node-B", cfg, ""); err != nil {
		t.Fatalf("resolveTerms: %v", err)
	}
	if got := fetcher.callCount(); got != 2 {
		t.Errorf("fetch calls = %d, want 2 (each node placement caches independently)", got)
	}
}

func TestBuildTokenTables_UsesCategoryAsPrefixWithFallback(t *testing.T) {
	cfg := defaultConfig()
	tokenOf, collisions := buildTokenTables([]termEntry{
		{UUID: "11111111-1111-1111-1111-111111111111", Name: "Jean Dupont", Category: "client"},
		{UUID: "22222222-2222-2222-2222-222222222222", Name: "Résidence du Parc"},
	}, cfg)

	if collisions != 0 {
		t.Fatalf("collisions = %d, want 0", collisions)
	}

	clientToken := tokenOf["11111111-1111-1111-1111-111111111111"]
	if !strings.HasPrefix(clientToken, "[CLIENT_") {
		t.Errorf("token = %q, want prefix %q", clientToken, "[CLIENT_")
	}

	fallbackToken := tokenOf["22222222-2222-2222-2222-222222222222"]
	if !strings.HasPrefix(fallbackToken, "["+defaultTokenPrefixFallback+"_") {
		t.Errorf("token = %q, want prefix %q (no category -> fallback)", fallbackToken, "["+defaultTokenPrefixFallback+"_")
	}
}

func TestBuildTokenTables_ResolvesCollisionByExtendingBothTokens(t *testing.T) {
	cfg := defaultConfig()
	cfg.TokenHexLength = 8

	// Both uuids share the same first 8 hex characters once dashes are
	// stripped, forcing a collision at the configured token length.
	terms := []termEntry{
		{UUID: "aaaaaaaa-0000-0000-0000-000000000001", Name: "Client Un", Category: "client"},
		{UUID: "aaaaaaaa-0000-0000-0000-000000000002", Name: "Client Deux", Category: "client"},
	}

	tokenOf, collisions := buildTokenTables(terms, cfg)

	if collisions == 0 {
		t.Fatal("expected at least one collision to be detected and resolved")
	}
	t1 := tokenOf[terms[0].UUID]
	t2 := tokenOf[terms[1].UUID]
	if t1 == "" || t2 == "" {
		t.Fatalf("expected both uuids to get a token, got %q and %q", t1, t2)
	}
	if t1 == t2 {
		t.Fatalf("expected distinct tokens after collision resolution, both = %q", t1)
	}
}

func TestBuildTokenTables_DeterministicAcrossCalls(t *testing.T) {
	cfg := defaultConfig()
	terms := []termEntry{{UUID: "33333333-3333-3333-3333-333333333333", Name: "Jean Dupont", Category: "client"}}

	tokenOf1, _ := buildTokenTables(terms, cfg)
	tokenOf2, _ := buildTokenTables(terms, cfg)

	if tokenOf1[terms[0].UUID] != tokenOf2[terms[0].UUID] {
		t.Errorf("token differs across calls: %q vs %q, want deterministic per uuid", tokenOf1[terms[0].UUID], tokenOf2[terms[0].UUID])
	}
}

func TestBuildTokenTables_SkipsEmptyUUIDAndDedupesDuplicates(t *testing.T) {
	cfg := defaultConfig()
	tokenOf, _ := buildTokenTables([]termEntry{
		{UUID: "", Name: "Sans UUID"},
		{UUID: "u1", Name: "Premier"},
		{UUID: "u1", Name: "Doublon"},
	}, cfg)

	if len(tokenOf) != 1 {
		t.Fatalf("tokenOf = %v, want exactly one entry (empty uuid skipped, duplicate uuid deduped)", tokenOf)
	}
	if _, ok := tokenOf["u1"]; !ok {
		t.Error("expected uuid \"u1\" to have a token")
	}
}
