package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// errStillBackingOff is returned when there is no cache at all yet and the
// last fetch attempt is still within cfg.MinRetryIntervalSeconds — the cold
// counterpart of serving a stale cache: don't hit a downed external API on
// every single request even before any list was ever fetched successfully.
var errStillBackingOff = errors.New("term list unavailable, retry backoff in effect")

// termFetcher fetches the current term list from the external source. The
// real implementation (client.go) calls the configured HTTP API; tests
// inject a fake so cache/backoff logic can be verified without a real
// network call.
type termFetcher interface {
	FetchTerms(ctx context.Context, cfg Config, authValue string) ([]termEntry, error)
}

// cacheEntry is what's kept in memory for one node placement, i.e. one
// (orgID, nodeID) pair — not one per org, since two placements of this
// plugin can legitimately point at different api_url/config (the config a
// PreRequest call sees comes from the node's own place in the pipeline
// graph, not a shared org-wide store).
type cacheEntry struct {
	terms   []termEntry
	matcher *matcher
	tokenOf map[string]string // uuid -> token

	// collisions counts how many entries needed a longer token than
	// cfg.TokenHexLength to stay unique, for admin visibility.
	collisions int

	fetchedAt time.Time
}

// termCache resolves the current term list for a node placement, with a TTL,
// stale-serve-on-failure, and a fetch backoff floor so a downed external API
// isn't hit on every single request during an outage — whether or not a
// cache entry already exists.
type termCache struct {
	mu          sync.Mutex
	entries     map[string]*cacheEntry
	lastAttempt map[string]time.Time
	fetcher     termFetcher
	now         func() time.Time // overridable in tests
}

func newTermCache(fetcher termFetcher) *termCache {
	return &termCache{
		entries:     make(map[string]*cacheEntry),
		lastAttempt: make(map[string]time.Time),
		fetcher:     fetcher,
		now:         time.Now,
	}
}

func cacheKey(orgID, nodeID string) string {
	return orgID + "/" + nodeID
}

// resolveTerms returns the current cacheEntry for (orgID, nodeID), refreshing
// it from the external API as needed:
//
//   - a fresh cache (younger than cfg.CacheTTLSeconds) is returned as is,
//     stale=false, no network call.
//   - a stale (or nonexistent) cache whose last fetch attempt is younger
//     than cfg.MinRetryIntervalSeconds is returned as is (stale=true when an
//     entry exists) without attempting a new fetch — this is what keeps a
//     downed external API from being hit on every single request, both
//     while serving a stale list and while still cold.
//   - otherwise a fetch is attempted: success replaces the cache entry and
//     returns it fresh; failure with a prior entry serves it stale; failure
//     with no prior entry at all returns (nil, false, err) — the true
//     fail-open path, which PreRequest turns into a full passthrough.
func (c *termCache) resolveTerms(ctx context.Context, orgID, nodeID string, cfg Config, authValue string) (*cacheEntry, bool, error) {
	key := cacheKey(orgID, nodeID)
	now := c.now()

	c.mu.Lock()
	entry := c.entries[key]
	lastAttempt := c.lastAttempt[key]
	c.mu.Unlock()

	ttl := time.Duration(cfg.CacheTTLSeconds) * time.Second
	if entry != nil && now.Sub(entry.fetchedAt) < ttl {
		return entry, false, nil
	}

	backoff := time.Duration(cfg.MinRetryIntervalSeconds) * time.Second
	if !lastAttempt.IsZero() && now.Sub(lastAttempt) < backoff {
		if entry != nil {
			return entry, true, nil
		}
		return nil, false, errStillBackingOff
	}

	terms, fetchErr := c.fetcher.FetchTerms(ctx, cfg, authValue)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastAttempt[key] = now

	if fetchErr != nil {
		if entry != nil {
			return entry, true, nil
		}
		return nil, false, fetchErr
	}

	newEntry := buildCacheEntry(terms, cfg, now)
	c.entries[key] = newEntry
	return newEntry, false, nil
}

// buildCacheEntry indexes a freshly fetched term list: builds the matching
// trie and the deterministic uuid<->token tables.
func buildCacheEntry(terms []termEntry, cfg Config, fetchedAt time.Time) *cacheEntry {
	tokenOf, collisions := buildTokenTables(terms, cfg)
	return &cacheEntry{
		terms:      terms,
		matcher:    buildMatcher(terms),
		tokenOf:    tokenOf,
		collisions: collisions,
		fetchedAt:  fetchedAt,
	}
}

// tokenForUUID derives the placeholder token for one term: "[" + PREFIX + "_"
// + first hexLen characters of the uuid (dashes stripped) + "]". Deterministic
// per uuid — no per-session nonce — so the same client/property always maps
// to the same token across requests and across a whole conversation.
func tokenForUUID(uuid, category, fallbackPrefix string, hexLen int) string {
	prefix := strings.ToUpper(strings.TrimSpace(category))
	if prefix == "" {
		prefix = strings.ToUpper(fallbackPrefix)
	}
	stripped := strings.ReplaceAll(uuid, "-", "")
	if hexLen > len(stripped) {
		hexLen = len(stripped)
	}
	return "[" + prefix + "_" + stripped[:hexLen] + "]"
}

// maxTokenSuffixLength is a stripped RFC4122 uuid's length (32 hex chars) —
// the point past which lengthening a token can no longer resolve a collision.
const maxTokenSuffixLength = 32

// buildTokenTables assigns every term a token, starting at cfg.TokenHexLength
// and extending it (for every entry sharing the colliding token, together)
// until all tokens are unique. Terms with an empty uuid are skipped; a
// duplicate uuid keeps only its first occurrence.
func buildTokenTables(terms []termEntry, cfg Config) (tokenOf map[string]string, collisions int) {
	type candidate struct {
		term   termEntry
		length int
	}

	baseLen := cfg.TokenHexLength
	if baseLen <= 0 {
		baseLen = defaultTokenHexLength
	}

	seen := make(map[string]bool, len(terms))
	candidates := make([]*candidate, 0, len(terms))
	for _, t := range terms {
		if t.UUID == "" || seen[t.UUID] {
			continue
		}
		seen[t.UUID] = true
		candidates = append(candidates, &candidate{term: t, length: baseLen})
	}

	for {
		byToken := make(map[string][]*candidate, len(candidates))
		for _, c := range candidates {
			tok := tokenForUUID(c.term.UUID, c.term.Category, cfg.TokenPrefixFallback, c.length)
			byToken[tok] = append(byToken[tok], c)
		}

		conflict, grew := false, false
		for _, group := range byToken {
			if len(group) < 2 {
				continue
			}
			conflict = true
			collisions += len(group) - 1
			for _, c := range group {
				if c.length < maxTokenSuffixLength {
					c.length++
					grew = true
				}
			}
		}
		if !conflict || !grew {
			break
		}
	}

	tokenOf = make(map[string]string, len(candidates))
	for _, c := range candidates {
		tok := tokenForUUID(c.term.UUID, c.term.Category, cfg.TokenPrefixFallback, c.length)
		tokenOf[c.term.UUID] = tok
	}
	return tokenOf, collisions
}
