package mcpclient_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/xolo-gateway/xolo/internal/adapter/mcpclient"
)

// sessionCounter counts the MCP sessions used on a test server.
//
// It reads the Mcp-Session-Id request header rather than touching the body: the
// initialize carries none and every later request of a session carries the one
// it opened, so the distinct IDs are the sessions opened. Reading the body would
// break the streaming handler.
type sessionCounter struct {
	mu  sync.Mutex
	ids map[string]struct{}
}

func (c *sessionCounter) observe(r *http.Request) {
	id := r.Header.Get("Mcp-Session-Id")
	if id == "" {
		return
	}
	c.mu.Lock()
	c.ids[id] = struct{}{}
	c.mu.Unlock()
}

// sessions returns how many distinct sessions the server saw.
func (c *sessionCounter) sessions() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.ids)
}

// newCountingServer starts an MCP server counting the sessions used on it.
func newCountingServer(t *testing.T) (*httptest.Server, *sessionCounter) {
	t.Helper()

	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "v1"}, nil)
	mcp.AddTool(mcpServer, &mcp.Tool{Name: "greet", Description: "greets someone"}, greet)

	counter := &sessionCounter{ids: map[string]struct{}{}}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		counter.observe(r)
		handler.ServeHTTP(w, r)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		srv.CloseClientConnections()
		srv.Close()
	})
	return srv, counter
}

// TestConnector_OneSessionAcrossToolRounds is the third acceptance criterion:
// a request resolving three tool calls opens a single MCP session.
func TestConnector_OneSessionAcrossToolRounds(t *testing.T) {
	srv, counter := newCountingServer(t)

	connector := mcpclient.NewConnector(mcpclient.Config{Endpoint: srv.URL + "/mcp"}, time.Minute)
	defer connector.Close()

	ctx := context.Background()
	for round := range 3 {
		err := connector.Do(ctx, func(session *mcp.ClientSession) error {
			tools, err := mcpclient.BuildTools(ctx, session, nil)
			if err != nil {
				return err
			}
			result, err := tools[0].Execute(ctx, map[string]any{"name": "xolo"})
			if err != nil {
				return err
			}
			if result.Text() != "hello xolo" {
				t.Errorf("round %d: expected %q, got %q", round, "hello xolo", result.Text())
			}
			return nil
		})
		if err != nil {
			t.Fatalf("round %d: Do: %v", round, err)
		}
	}

	if got := counter.sessions(); got != 1 {
		t.Errorf("expected a single MCP session for three tool rounds, the server saw %d", got)
	}
}

// TestConnector_ConcurrentCallsShareOneSession makes sure two concurrent calls
// of one conversation do not race into two sessions.
func TestConnector_ConcurrentCallsShareOneSession(t *testing.T) {
	srv, counter := newCountingServer(t)

	connector := mcpclient.NewConnector(mcpclient.Config{Endpoint: srv.URL + "/mcp"}, time.Minute)
	defer connector.Close()

	ctx := context.Background()
	start := make(chan struct{})
	errs := make(chan error, 4)
	for range 4 {
		go func() {
			<-start
			errs <- connector.Do(ctx, func(session *mcp.ClientSession) error {
				_, _, err := mcpclient.CallToolText(ctx, session, "greet", `{"name":"xolo"}`)
				return err
			})
		}()
	}
	close(start)
	for range 4 {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent call: %v", err)
		}
	}

	if got := counter.sessions(); got != 1 {
		t.Errorf("expected concurrent calls of one conversation to share a session, the server saw %d", got)
	}
}

// TestConnector_RetriesOnceOnADeadSession checks a session that outlived its
// server is replaced rather than failing the call outright.
func TestConnector_RetriesOnceOnADeadSession(t *testing.T) {
	srv, _ := newCountingServer(t)

	connector := mcpclient.NewConnector(mcpclient.Config{Endpoint: srv.URL + "/mcp"}, time.Minute)
	defer connector.Close()

	ctx := context.Background()
	if err := connector.Do(ctx, func(session *mcp.ClientSession) error {
		_, _, err := mcpclient.CallToolText(ctx, session, "greet", `{"name":"a"}`)
		return err
	}); err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Drop the connection under the session: the next call has to notice the
	// session is over, and reconnect rather than reuse it.
	srv.CloseClientConnections()

	err := connector.Do(ctx, func(session *mcp.ClientSession) error {
		_, _, callErr := mcpclient.CallToolText(ctx, session, "greet", `{"name":"b"}`)
		return callErr
	})
	if err != nil {
		t.Fatalf("expected the call to be retried on a fresh session: %v", err)
	}
}
