package oidctoken

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/pkg/errors"
)

const jwksCacheDuration = 1 * time.Hour

// jwksRefetchInterval bounds the forced refetches triggered by an unknown key
// ID, so tokens with forged key IDs cannot hammer the identity provider.
const jwksRefetchInterval = 1 * time.Minute

// jwksRefetchRetryDelay is the wait after a failed forced refetch: a failure
// must not block picking up a rotated key for a whole jwksRefetchInterval.
const jwksRefetchRetryDelay = 5 * time.Second

type cachedJWKS struct {
	JWKS      JWKS
	ExpiresAt time.Time
}

type jwksCache struct {
	mu        sync.RWMutex
	items     map[string]cachedJWKS
	refetched map[string]time.Time
}

var cache jwksCache

func init() {
	cache.items = map[string]cachedJWKS{}
	cache.refetched = map[string]time.Time{}
}

func (c *jwksCache) Get(jwksURL string) (JWKS, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	item, ok := c.items[jwksURL]
	if !ok || time.Now().After(item.ExpiresAt) {
		return JWKS{}, false
	}

	return item.JWKS, true
}

func (c *jwksCache) Set(jwksURL string, jwks JWKS) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items[jwksURL] = cachedJWKS{
		JWKS:      jwks,
		ExpiresAt: time.Now().Add(jwksCacheDuration),
	}
}

func (c *jwksCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = map[string]cachedJWKS{}
	c.refetched = map[string]time.Time{}
}

// allowRefetch reports whether a forced refetch of jwksURL may happen now and,
// when it may, records it.
func (c *jwksCache) allowRefetch(jwksURL string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if last, ok := c.refetched[jwksURL]; ok && time.Since(last) < jwksRefetchInterval {
		return false
	}
	c.refetched[jwksURL] = time.Now()
	return true
}

// refetchFailed lets a forced refetch of jwksURL happen again after
// jwksRefetchRetryDelay instead of jwksRefetchInterval.
func (c *jwksCache) refetchFailed(jwksURL string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.refetched[jwksURL] = time.Now().Add(jwksRefetchRetryDelay - jwksRefetchInterval)
}

func fetchJWKS(ctx context.Context, jwksURL string) (JWKS, error) {
	if cached, ok := cache.Get(jwksURL); ok {
		return cached, nil
	}

	return downloadJWKS(ctx, jwksURL)
}

// refetchJWKS bypasses the cache to pick up keys the provider rotated in since
// the last fetch. It returns false when a refetch of jwksURL already happened
// within jwksRefetchInterval.
func refetchJWKS(ctx context.Context, jwksURL string) (JWKS, bool, error) {
	if !cache.allowRefetch(jwksURL) {
		return JWKS{}, false, nil
	}

	jwks, err := downloadJWKS(ctx, jwksURL)
	if err != nil {
		cache.refetchFailed(jwksURL)
		return JWKS{}, false, err
	}
	return jwks, true, nil
}

func downloadJWKS(ctx context.Context, jwksURL string) (JWKS, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURL, nil)
	if err != nil {
		return JWKS{}, errors.WithStack(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return JWKS{}, errors.WithStack(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return JWKS{}, errors.Errorf("jwks fetch failed with status %d", resp.StatusCode)
	}

	var jwks JWKS
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		slog.ErrorContext(ctx, "could not decode JWKS", slog.Any("error", errors.WithStack(err)))
		return JWKS{}, errors.WithStack(err)
	}

	cache.Set(jwksURL, jwks)

	return jwks, nil
}