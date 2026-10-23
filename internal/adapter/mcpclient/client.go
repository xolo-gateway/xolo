// Package mcpclient connects to external MCP servers over the Streamable
// HTTP transport, using the official github.com/modelcontextprotocol/go-sdk.
package mcpclient

import (
	"context"
	"maps"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pkg/errors"
)

// Config describes how to reach and authenticate against an MCP server.
type Config struct {
	Endpoint       string
	AuthHeaderName string
	AuthValue      string
	Headers        map[string]string
	Timeout        time.Duration
	// MaxRetries bounds how many times a transient failure (HTTP 429/502/503/504,
	// timeouts, connection errors) is retried with exponential backoff.
	// <= 0 falls back to DefaultMaxRetries.
	MaxRetries int
	// RetryBaseDelay is the initial backoff delay, doubled after each retry.
	// <= 0 falls back to DefaultRetryBaseDelay.
	RetryBaseDelay time.Duration
}

// DefaultMaxRetries and DefaultRetryBaseDelay are applied when Config leaves
// the corresponding fields unset, so tool calls are resilient to transient
// MCP server hiccups (rate limiting, slow cold starts) out of the box.
const (
	DefaultMaxRetries     = 3
	DefaultRetryBaseDelay = 500 * time.Millisecond
)

// authRoundTripper injects static headers into every outgoing request.
type authRoundTripper struct {
	headers map[string]string
	next    http.RoundTripper
}

func (rt *authRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for name, value := range rt.headers {
		if value != "" {
			req.Header.Set(name, value)
		}
	}
	return rt.next.RoundTrip(req)
}

// DefaultTimeout is applied when Config leaves Timeout unset. It is
// deliberately generous: this is an http.Client-wide timeout, applied to
// every request made over the connection's lifetime (init, the tool call
// itself, and the Streamable HTTP notification stream), and MCP tools that
// perform RAG/LLM work server-side (embeddings, retrieval, judging, answer
// generation) routinely take tens of seconds to respond.
const DefaultTimeout = 120 * time.Second

// Connect opens a Streamable HTTP session against the configured MCP server.
//
// Each call opens its own session: a caller resolving several calls of one
// conversation wants a Connector instead.
func Connect(ctx context.Context, cfg Config) (*mcp.ClientSession, error) {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	headers := make(map[string]string, len(cfg.Headers)+1)
	maps.Copy(headers, cfg.Headers)
	if cfg.AuthHeaderName != "" && cfg.AuthValue != "" {
		headers[cfg.AuthHeaderName] = cfg.AuthValue
	}

	maxRetries := cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = DefaultMaxRetries
	}
	retryBaseDelay := cfg.RetryBaseDelay
	if retryBaseDelay <= 0 {
		retryBaseDelay = DefaultRetryBaseDelay
	}

	httpClient := &http.Client{
		Timeout: timeout,
		Transport: &retryingRoundTripper{
			next:       &authRoundTripper{headers: headers, next: http.DefaultTransport},
			maxRetries: maxRetries,
			baseDelay:  retryBaseDelay,
		},
	}

	transport := &mcp.StreamableClientTransport{
		Endpoint:   cfg.Endpoint,
		HTTPClient: httpClient,
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "xolo", Version: "1.0.0"}, nil)

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, errors.Wrap(err, "connect to MCP server")
	}

	return session, nil
}
