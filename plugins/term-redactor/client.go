package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// httpTermFetcher fetches the term list from the configured external API.
// Implements termFetcher (cache.go).
type httpTermFetcher struct {
	client *http.Client
}

func newHTTPTermFetcher() *httpTermFetcher {
	return &httpTermFetcher{client: &http.Client{}}
}

// FetchTerms performs a GET on cfg.APIURL, optionally authenticated with
// authValue under cfg.APIAuthHeaderName, and decodes the response body as a
// JSON array of {"uuid": "...", "name": "...", "category": "..."} objects
// (category optional).
func (f *httpTermFetcher) FetchTerms(ctx context.Context, cfg Config, authValue string) ([]termEntry, error) {
	if cfg.APIURL == "" {
		return nil, fmt.Errorf("api_url not configured")
	}

	timeoutSeconds := cfg.HTTPTimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = defaultHTTPTimeoutSeconds
	}
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, cfg.APIURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if authValue != "" {
		headerName := cfg.APIAuthHeaderName
		if headerName == "" {
			headerName = defaultAPIAuthHeaderName
		}
		req.Header.Set(headerName, authValue)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch term list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("fetch term list: unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var terms []termEntry
	if err := json.NewDecoder(resp.Body).Decode(&terms); err != nil {
		return nil, fmt.Errorf("decode term list: %w", err)
	}
	return terms, nil
}
