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

// jwksDownloadBackoff is the wait after a failed download of a key set
// before another is tried: while the provider is down, requests do not each
// wait on it.
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
	// failed holds when the last download of a key set failed, until one
	// succeeds.
	failed map[string]time.Time
	// inflight holds the downloads under way, closed when they end.
	inflight map[string]chan struct{}
}

var cache jwksCache

func init() {
	cache.items = map[string]cachedJWKS{}
	cache.refetched = map[string]time.Time{}
	cache.failed = map[string]time.Time{}
	cache.inflight = map[string]chan struct{}{}
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
	delete(c.failed, jwksURL)
}

// startDownload reports whether jwksURL may be downloaded now and, when it
// may, records the download as under way. It returns the download under way,
// the one just recorded or another, to wait for, or, within
// jwksDownloadBackoff of a failed one, how long ago it failed.
func (c *jwksCache) startDownload(jwksURL string) (start bool, underWay chan struct{}, failedAgo time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if done, ok := c.inflight[jwksURL]; ok {
		return false, done, 0
	}
	if last, ok := c.failed[jwksURL]; ok && time.Since(last) < jwksDownloadBackoff {
		return false, nil, time.Since(last)
	}
	done := make(chan struct{})
	c.inflight[jwksURL] = done
	return true, done, 0
}

// downloadEnded ends the download startDownload recorded as done and, when it
// failed, starts jwksDownloadBackoff. A download Clear forgot closes nothing.
func (c *jwksCache) downloadEnded(jwksURL string, done chan struct{}, failed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if failed {
		c.failed[jwksURL] = time.Now()
	}
	if c.inflight[jwksURL] == done {
		close(done)
		delete(c.inflight, jwksURL)
	}
}

func (c *jwksCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = map[string]cachedJWKS{}
	c.refetched = map[string]time.Time{}
	c.failed = map[string]time.Time{}
	// Release the requests waiting for a download under way.
	for _, done := range c.inflight {
		close(done)
	}
	c.inflight = map[string]chan struct{}{}
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
// one expired. One download runs at a time, and none within
// jwksDownloadBackoff of a failed one, so requests do not each wait on a
// provider that is down or slow.
//
// An expired key set, up to jwksStaleLimit, is returned at once while a new
// one is downloaded in the background: the tokens signed by the known keys
// keep working, without waiting on the provider. Without any key set, the
// request waits for the download, its own or the one under way. A download
// never runs with the context of a request: a client giving up does not make
// it fail for the others nor start jwksDownloadBackoff. The error wraps
// ErrKeysUnavailable when the download fails or failed less than
// jwksDownloadBackoff ago, or when ctx ends first.
func fetchJWKS(ctx context.Context, jwksURL string) (JWKS, error) {
	cached, fresh, known := cache.Get(jwksURL)
	if fresh {
		return cached, nil
	}

	start, underWay, failedAgo := cache.startDownload(jwksURL)
	if start {
		go downloadAttempt(context.WithoutCancel(ctx), jwksURL, underWay, known)
	}
	switch {
	case known:
		return cached, nil
	case underWay == nil:
		return JWKS{}, errors.Wrapf(ErrKeysUnavailable, "%s: last download failed %s ago", jwksURL, failedAgo.Round(time.Millisecond))
	}

	select {
	case <-underWay:
	case <-ctx.Done():
		return JWKS{}, fmt.Errorf("%w: %w", ErrKeysUnavailable, ctx.Err())
	}
	if jwks, _, ok := cache.Get(jwksURL); ok {
		return jwks, nil
	}
	return JWKS{}, errors.Wrapf(ErrKeysUnavailable, "%s: the download failed", jwksURL)
}

// downloadAttempt runs the download startDownload recorded as done, bounded
// by jwksFetchTimeout, ends it and logs its failure.
func downloadAttempt(ctx context.Context, jwksURL string, done chan struct{}, usingExpiredKeySet bool) {
	ctx, cancel := context.WithTimeout(ctx, jwksFetchTimeout)
	defer cancel()
	_, err := downloadJWKS(ctx, jwksURL)
	cache.downloadEnded(jwksURL, done, err != nil)
	if err != nil {
		slog.WarnContext(ctx, "could not download the key set of an OIDC provider",
			slog.String("url", jwksURL),
			slog.Bool("usingExpiredKeySet", usingExpiredKeySet),
			slog.Any("error", err))
	}
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