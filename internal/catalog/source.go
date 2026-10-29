package catalog

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
)

// ErrUnavailable reports that no release of the catalog could be read yet.
var ErrUnavailable = errors.New("model catalog unavailable")

// maxCatalogSize bounds a downloaded release.
const maxCatalogSize = 32 << 20

// unavailableRetry bounds the wait before trying again while no release has
// been read: the refresh interval (a day by default) is for keeping a release
// fresh, not for leaving the catalog unavailable.
const unavailableRetry = time.Minute

// Source serves the catalog published at its URL: the last release it read,
// fetched again at most once per refresh interval. Xolo embeds no copy of
// the catalog: until a first release is read, the catalog is unavailable. A
// fetch never blocks a page: it runs in the background while callers get
// the release already read.
type Source struct {
	url      string
	interval time.Duration
	client   *http.Client

	mu        sync.Mutex
	current   *Catalog
	lastErr   error
	fetchedAt time.Time
	// inflight is closed when the fetch in progress, if any, ends.
	inflight chan struct{}
}

// NewSource serves the catalog published at url, read again every interval,
// a minute at least: a shorter one would fetch on every page.
func NewSource(url string, interval time.Duration) *Source {
	return &Source{url: url, interval: max(interval, unavailableRetry), client: &http.Client{Timeout: 15 * time.Second}}
}

// NewStaticSource always serves c, as a test or an offline tool needs.
func NewStaticSource(c *Catalog) *Source {
	return &Source{current: c}
}

// Catalog returns the last release read, and starts fetching a newer one in
// the background when the refresh interval has elapsed. It returns
// ErrUnavailable while no release could be read.
func (s *Source) Catalog(ctx context.Context) (*Catalog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	wait := s.interval
	if s.current == nil {
		wait = min(wait, unavailableRetry)
	}
	if s.url != "" && s.inflight == nil && time.Since(s.fetchedAt) >= wait {
		s.inflight = make(chan struct{})
		go s.refresh(context.WithoutCancel(ctx), s.inflight)
	}
	if s.current == nil {
		if s.lastErr != nil {
			return nil, errors.Wrap(ErrUnavailable, s.lastErr.Error())
		}
		return nil, errors.WithStack(ErrUnavailable)
	}
	return s.current, nil
}

// Refresh fetches the release now and waits for it, or for the fetch already
// in progress, as the start of the server does. It keeps the release already
// read when the fetch fails.
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
		s.lastErr = err
		slog.WarnContext(ctx, "could not fetch the model catalog", slog.String("url", s.url), slogx.Error(err))
		return
	}
	s.current, s.lastErr = c, nil
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
