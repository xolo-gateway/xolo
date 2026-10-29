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
// embedded snapshot. A catalog that cannot be reached never blocks a page.
type Source struct {
	url      string
	interval time.Duration
	client   *http.Client

	mu        sync.Mutex
	current   *Catalog
	fetchedAt time.Time
}

// NewSource serves the catalog published at url. An empty url serves the
// embedded snapshot only.
func NewSource(url string, interval time.Duration) *Source {
	return &Source{url: url, interval: interval, client: &http.Client{Timeout: 15 * time.Second}}
}

// Catalog returns the current catalog.
func (s *Source) Catalog(ctx context.Context) *Catalog {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil && (s.url == "" || time.Since(s.fetchedAt) < s.interval) {
		return s.current
	}
	if s.url != "" {
		c, err := s.fetch(ctx)
		// A failed fetch is retried at the next interval, not on every
		// page.
		s.fetchedAt = time.Now()
		if err == nil {
			s.current = c
			return c
		}
		slog.WarnContext(ctx, "could not fetch the model catalog, keeping the last one", slog.String("url", s.url), slogx.Error(err))
	}
	if s.current == nil {
		s.current = Snapshot()
	}
	return s.current
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
