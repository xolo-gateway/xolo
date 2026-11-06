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
// must not block picking up a rotated key for a whole jwksRefetchInterval. The
// failure also starts jwksDownloadBackoff, the longer of the two.
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
	// inflight holds the downloads under way.
	inflight map[string]*jwksDownload
}

// jwksDownload is a download of a key set, shared by the requests waiting for
// it. jwks and err are set before done is closed.
type jwksDownload struct {
	done chan struct{}
	jwks JWKS
	err  error
}

// wait returns the key set the download got, or an error wrapping
// ErrKeysUnavailable when it failed or ctx ends first.
func (d *jwksDownload) wait(ctx context.Context, jwksURL string) (JWKS, error) {
	select {
	case <-d.done:
	case <-ctx.Done():
		return JWKS{}, fmt.Errorf("%w: %w", ErrKeysUnavailable, ctx.Err())
	}
	if d.err != nil {
		return JWKS{}, errors.Wrapf(ErrKeysUnavailable, "%s: the download failed: %v", jwksURL, d.err)
	}
	return d.jwks, nil
}

var cache jwksCache

func init() {
	cache.items = map[string]cachedJWKS{}
	cache.refetched = map[string]time.Time{}
	cache.failed = map[string]time.Time{}
	cache.inflight = map[string]*jwksDownload{}
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
func (c *jwksCache) startDownload(jwksURL string) (start bool, underWay *jwksDownload, failedAgo time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if d, ok := c.inflight[jwksURL]; ok {
		return false, d, 0
	}
	if last, ok := c.failed[jwksURL]; ok && time.Since(last) < jwksDownloadBackoff {
		return false, nil, time.Since(last)
	}
	d := &jwksDownload{done: make(chan struct{})}
	c.inflight[jwksURL] = d
	return true, d, 0
}

// downloadEnded ends the download d with its result and, when it failed,
// starts jwksDownloadBackoff. A download Clear forgot is left alone.
func (c *jwksCache) downloadEnded(jwksURL string, d *jwksDownload, jwks JWKS, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err != nil {
		c.failed[jwksURL] = time.Now()
	}
	if c.inflight[jwksURL] == d {
		d.jwks, d.err = jwks, err
		close(d.done)
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
	for _, d := range c.inflight {
		d.err = errors.New("key set cache cleared")
		close(d.done)
	}
	c.inflight = map[string]*jwksDownload{}
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

	return underWay.wait(ctx, jwksURL)
}

// downloadAttempt runs the download startDownload recorded as d, bounded
// by jwksFetchTimeout, ends it and logs its failure.
func downloadAttempt(ctx context.Context, jwksURL string, d *jwksDownload, usingExpiredKeySet bool) {
	downloadCtx, cancel := context.WithTimeout(ctx, jwksFetchTimeout)
	defer cancel()
	jwks, err := downloadJWKS(downloadCtx, jwksURL)
	cache.downloadEnded(jwksURL, d, jwks, err)
	if err != nil {
		slog.WarnContext(ctx, "could not download the key set of an OIDC provider",
			slog.String("url", jwksURL),
			slog.Bool("usingExpiredKeySet", usingExpiredKeySet),
			slog.Any("error", err))
	}
}

// refetchJWKS downloads the key set of jwksURL again, fresh or not, to pick up
// keys the provider rotated in since the last download. It returns false when
// a refetch of jwksURL already happened within jwksRefetchInterval.
//
// It shares the coordination of fetchJWKS: it waits for the download under
// way instead of starting another, its own download never runs with the
// context of the request and starts jwksDownloadBackoff when it fails, and it
// does not call the provider within that backoff. The error then wraps
// ErrKeysUnavailable.
func refetchJWKS(ctx context.Context, jwksURL string) (JWKS, bool, error) {
	if !cache.allowRefetch(jwksURL) {
		return JWKS{}, false, nil
	}

	start, underWay, failedAgo := cache.startDownload(jwksURL)
	if start {
		go downloadAttempt(context.WithoutCancel(ctx), jwksURL, underWay, false)
	}
	if underWay == nil {
		cache.refetchFailed(jwksURL)
		return JWKS{}, false, errors.Wrapf(ErrKeysUnavailable, "%s: last download failed %s ago", jwksURL, failedAgo.Round(time.Millisecond))
	}

	jwks, err := underWay.wait(ctx, jwksURL)
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