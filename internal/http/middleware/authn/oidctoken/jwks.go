package oidctoken

import (
	"context"
	"encoding/json"
	"fmt"
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

// jwksDownloadBackoff is the wait after a download of a key set started or
// failed before another is tried: while the provider is down, requests do not
// each wait on it.
const jwksDownloadBackoff = 10 * time.Second

// jwksStaleLimit bounds how long a key set is still used past its expiry when
// the provider cannot serve a new one.
const jwksStaleLimit = 24 * time.Hour

type cachedJWKS struct {
	JWKS      JWKS
	ExpiresAt time.Time
}

type jwksCache struct {
	mu        sync.RWMutex
	items     map[string]cachedJWKS
	refetched map[string]time.Time
	// attempted holds when the last download of a key set that did not
	// succeed started or failed, until one succeeds.
	attempted map[string]time.Time
}

var cache jwksCache

func init() {
	cache.items = map[string]cachedJWKS{}
	cache.refetched = map[string]time.Time{}
	cache.attempted = map[string]time.Time{}
}

// Get returns the key set cached for jwksURL, whether it is still fresh, and
// whether there is one at all: an expired key set is returned up to
// jwksStaleLimit past its expiry.
func (c *jwksCache) Get(jwksURL string) (jwks JWKS, fresh, ok bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	item, found := c.items[jwksURL]
	if !found {
		return JWKS{}, false, false
	}
	now := time.Now()
	if now.After(item.ExpiresAt.Add(jwksStaleLimit)) {
		return JWKS{}, false, false
	}

	return item.JWKS, !now.After(item.ExpiresAt), true
}

func (c *jwksCache) Set(jwksURL string, jwks JWKS) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items[jwksURL] = cachedJWKS{
		JWKS:      jwks,
		ExpiresAt: time.Now().Add(jwksCacheDuration),
	}
	delete(c.attempted, jwksURL)
}

// startDownload reports whether jwksURL may be downloaded now and, when it
// may, records the attempt: the requests arriving while it runs, or within
// jwksDownloadBackoff of it, do not start their own.
func (c *jwksCache) startDownload(jwksURL string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if last, ok := c.attempted[jwksURL]; ok && time.Since(last) < jwksDownloadBackoff {
		return false
	}
	c.attempted[jwksURL] = time.Now()
	return true
}

// downloadFailed starts jwksDownloadBackoff again from the failure, which may
// come jwksFetchTimeout after the attempt.
func (c *jwksCache) downloadFailed(jwksURL string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.attempted[jwksURL] = time.Now()
}

func (c *jwksCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = map[string]cachedJWKS{}
	c.refetched = map[string]time.Time{}
	c.attempted = map[string]time.Time{}
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

// fetchJWKS returns the key set of jwksURL, downloading it when the cached
// one expired. At most one download runs per jwksDownloadBackoff, so requests
// do not each wait on a provider that is down or slow.
//
// An expired key set, up to jwksStaleLimit, is returned at once while a new
// one is downloaded in the background: the tokens signed by the known keys
// keep working, without waiting on the provider. Without any key set, the
// download is waited for, and the error wraps ErrKeysUnavailable when it
// fails or another one is under way.
func fetchJWKS(ctx context.Context, jwksURL string) (JWKS, error) {
	cached, fresh, known := cache.Get(jwksURL)
	if fresh {
		return cached, nil
	}

	if !cache.startDownload(jwksURL) {
		if known {
			return cached, nil
		}
		return JWKS{}, errors.Wrapf(ErrKeysUnavailable, "%s: a download is under way or failed less than %s ago", jwksURL, jwksDownloadBackoff)
	}

	if known {
		go func() {
			_, _ = downloadAttempt(context.WithoutCancel(ctx), jwksURL, true)
		}()
		return cached, nil
	}

	jwks, err := downloadAttempt(ctx, jwksURL, false)
	if err != nil {
		return JWKS{}, fmt.Errorf("%w: %w", ErrKeysUnavailable, err)
	}
	return jwks, nil
}

// downloadAttempt runs the download startDownload allowed, bounded by
// jwksFetchTimeout, and logs its failure.
func downloadAttempt(ctx context.Context, jwksURL string, usingExpiredKeySet bool) (JWKS, error) {
	ctx, cancel := context.WithTimeout(ctx, jwksFetchTimeout)
	defer cancel()
	jwks, err := downloadJWKS(ctx, jwksURL)
	if err != nil {
		cache.downloadFailed(jwksURL)
		slog.WarnContext(ctx, "could not download the key set of an OIDC provider",
			slog.String("url", jwksURL),
			slog.Bool("usingExpiredKeySet", usingExpiredKeySet),
			slog.Any("error", err))
		return JWKS{}, err
	}
	return jwks, nil
}

// refetchJWKS bypasses the cache to pick up keys the provider rotated in since
// the last fetch. It returns false when a refetch of jwksURL already happened
// within jwksRefetchInterval.
func refetchJWKS(ctx context.Context, jwksURL string) (JWKS, bool, error) {
	if !cache.allowRefetch(jwksURL) {
		return JWKS{}, false, nil
	}

	ctx, cancel := context.WithTimeout(ctx, jwksFetchTimeout)
	defer cancel()
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