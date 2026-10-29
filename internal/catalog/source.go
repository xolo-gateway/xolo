package catalog

import (
	"context"
	_ "embed"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
)

// snapshot is the release embedded in the binary, for an instance that
// cannot reach the catalog.
//
//go:embed snapshot.json
var snapshot []byte

// maxCatalogSize bounds a downloaded release.
const maxCatalogSize = 32 << 20

// Snapshot returns the release embedded in the binary.
func Snapshot() *Catalog {
	c, err := Parse(snapshot)
	if err != nil {
		panic(errors.Wrap(err, "embedded catalog snapshot"))
	}
	return c
}

// Source serves the catalog: the release at its URL, fetched at most once
// per refresh interval, or else the last one it fetched, or else the
// embedded snapshot. A catalog that cannot be reached never blocks a page:
// the fetch runs in the background while callers get the current catalog.
type Source struct {
	url      string
	interval time.Duration
	client   *http.Client

	mu        sync.Mutex
	current   *Catalog
	fetchedAt time.Time
	// inflight is closed when the fetch in progress, if any, ends.
	inflight chan struct{}
}

// NewStaticSource always serves c, as a test or an offline tool needs.
func NewStaticSource(c *Catalog) *Source {
	return &Source{current: c}
}

// NewSource serves the catalog published at url. An empty url serves the
// embedded snapshot only.
func NewSource(url string, interval time.Duration) *Source {
	return &Source{url: url, interval: interval, client: &http.Client{Timeout: 15 * time.Second}}
}

// Catalog returns the current catalog, and starts fetching a newer release
// in the background when the refresh interval has elapsed.
func (s *Source) Catalog(ctx context.Context) *Catalog {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		s.current = Snapshot()
	}
	if s.url != "" && s.inflight == nil && time.Since(s.fetchedAt) >= s.interval {
		s.inflight = make(chan struct{})
		go s.refresh(context.WithoutCancel(ctx), s.inflight)
	}
	return s.current
}

// Refresh fetches the release now and waits for it, as a startup or a test
// does. It keeps the current catalog when the fetch fails.
func (s *Source) Refresh(ctx context.Context) {
	s.mu.Lock()
	if s.url == "" {
		s.mu.Unlock()
		return
	}
	done := s.inflight
	if done == nil {
		done = make(chan struct{})
		s.inflight = done
		s.mu.Unlock()
		s.refresh(ctx, done)
		return
	}
	s.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (s *Source) refresh(ctx context.Context, done chan struct{}) {
	c, err := s.fetch(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	defer close(done)
	s.inflight = nil
	// A failed fetch is retried at the next interval, not on every page.
	s.fetchedAt = time.Now()
	if err != nil {
		slog.WarnContext(ctx, "could not fetch the model catalog, keeping the current one", slog.String("url", s.url), slogx.Error(err))
		return
	}
	s.current = c
}

func (s *Source) fetch(ctx context.Context) (*Catalog, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	res, err := s.client.Do(req)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.Errorf("unexpected status %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxCatalogSize))
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return Parse(data)
}
