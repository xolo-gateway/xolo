//go:build e2e

// Package e2e drives a real Xolo server, built from the working tree, against
// a seeded SQLite database and a fake OpenAI-compatible provider. Every test
// talks to the server over HTTP exactly like a client would, so the whole
// chain is exercised: authentication, middlewares, plugin subprocesses, the
// upstream call and the events recorded along the way.
//
// Run with `make test-e2e` (the `e2e` build tag keeps it out of `go test ./...`).
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3/gormlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	gormadapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// Values pinned by cmd/seed (see cmd/seed/README.md).
const (
	seedSecretKey = "0e2ec2e6d5aa74c1b96c65d1b4a0f4d9ad48c6b1f3ff7a1c5f0f5f2ae4c1d3b7"
	orgAcme       = "org-acme"
	tokenAlice    = "xolo-e2e-alice-acme"
	tokenCarol    = "xolo-e2e-carol-acme" // Carol carries a user quota, Alice does not

	providerAcmeOpenAI = "prov-acme-openai"
	modelAcmeGPT4oMini = "mdl-acme-gpt4o-mini"
	modelAcmeGPT4o     = "mdl-acme-gpt4o"

	// Proxy names of the models each e2e middleware wraps.
	modelTagStrategy  = "acme/gpt-4o-mini" // pseudonymizer, strategy tag
	modelHashStrategy = "acme/gpt-4o"      // pseudonymizer, strategy hash, no HMAC key
)

// env is the shared state of the whole test binary, built once by TestMain.
var env struct {
	baseURL  string
	dsn      string
	provider *fakeProvider
	logPath  string
}

func TestMain(m *testing.M) {
	code, err := run(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: %v\n", err)
		if env.logPath != "" {
			if logs, readErr := os.ReadFile(env.logPath); readErr == nil {
				fmt.Fprintf(os.Stderr, "---- server log ----\n%s\n", logs)
			}
		}
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func run(m *testing.M) (int, error) {
	root, err := repoRoot()
	if err != nil {
		return 1, err
	}

	work, err := os.MkdirTemp("", "xolo-e2e-")
	if err != nil {
		return 1, err
	}
	// XOLO_E2E_KEEP=1 leaves the work directory (binaries, database, server
	// log) in place for a post-mortem.
	if os.Getenv("XOLO_E2E_KEEP") != "" {
		fmt.Fprintf(os.Stderr, "e2e: keeping work directory %s\n", work)
	} else {
		defer os.RemoveAll(work)
	}

	serverBin := filepath.Join(work, "server")
	pluginsDir := filepath.Join(work, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		return 1, err
	}

	// Build from the working tree so the tests exercise the code under
	// review, not whatever bin/ holds.
	if err := goRun(root, "build", "-o", serverBin, "./cmd/server"); err != nil {
		return 1, fmt.Errorf("build server: %w", err)
	}
	for _, name := range e2ePlugins {
		if err := goRun(root, "build", "-o", filepath.Join(pluginsDir, name), "./plugins/"+name); err != nil {
			return 1, fmt.Errorf("build %s plugin: %w", name, err)
		}
	}

	env.dsn = filepath.Join(work, "e2e.sqlite")
	if err := goRun(root, "run", "./cmd/seed", "-dsn", env.dsn, "-force", "-days", "1"); err != nil {
		return 1, fmt.Errorf("seed database: %w", err)
	}

	env.provider = newFakeProvider()
	defer env.provider.Close()

	if err := prepareDatabase(env.dsn, env.provider.baseURL()); err != nil {
		return 1, fmt.Errorf("prepare database: %w", err)
	}

	port, err := freePort()
	if err != nil {
		return 1, err
	}
	env.baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	env.logPath = filepath.Join(work, "server.log")

	stop, err := startServer(serverBin, pluginsDir, port)
	if err != nil {
		return 1, err
	}
	defer stop()

	if err := waitReady(env.baseURL, 90*time.Second); err != nil {
		return 1, err
	}

	return m.Run(), nil
}

// repoRoot resolves the repository root from this file's location.
func repoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot locate the test source file")
	}
	return filepath.Abs(filepath.Join(filepath.Dir(file), "..", ".."))
}

func goRun(dir string, args ...string) error {
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// startServer launches the server binary on the seeded database and returns
// a function stopping it.
func startServer(bin, pluginsDir string, port int) (func(), error) {
	logFile, err := os.Create(env.logPath)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(bin)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(),
		"XOLO_HTTP_ADDRESS=127.0.0.1:"+fmt.Sprint(port),
		"XOLO_HTTP_BASE_URL="+env.baseURL,
		"XOLO_HTTP_SESSION_KEYS=e2e-session-key-0000000000000001",
		"XOLO_STORAGE_DATABASE_DSN="+env.dsn,
		"XOLO_SECRET_KEY="+seedSecretKey,
		"XOLO_PLUGINS_DIR="+pluginsDir,
		"XOLO_LOGGER_LEVEL=0",
	)
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, fmt.Errorf("start server: %w", err)
	}

	return func() {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		logFile.Close()
	}, nil
}

// waitReady polls the models endpoint with a valid token until the server,
// its database and its authentication chain answer.
func waitReady(baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	var last string
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+tokenAlice)
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = resp.Status
		} else {
			last = err.Error()
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("server not ready after %s (last: %s)", timeout, last)
}

func openDB(dsn string) (*gorm.DB, error) {
	return gorm.Open(gormlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// ── Fake provider ────────────────────────────────────────────────────────────

// fakeProvider is an OpenAI-compatible chat completion endpoint with fully
// predictable answers: it echoes the last user message back, so a test can
// check both what reached the provider and what the client got back.
type fakeProvider struct {
	srv *httptest.Server

	mu       sync.Mutex
	requests []chatRequest
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	// Raw keeps the body as received, for assertions on anything else.
	Raw string `json:"-"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

func newFakeProvider() *fakeProvider {
	p := &fakeProvider{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", p.handleChat)
	p.srv = httptest.NewServer(mux)
	return p
}

func (p *fakeProvider) baseURL() string { return p.srv.URL + "/v1" }
func (p *fakeProvider) Close()          { p.srv.Close() }

func (p *fakeProvider) handleChat(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req.Raw = string(body)

	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()

	if req.Model == realModelBroken {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
			"type": "server_error", "message": "e2e: this model is always down",
		}})
		return
	}

	answer := "Bien reçu : " + lastUserText(req.Messages)
	resp := map[string]any{
		"id":      "chatcmpl-e2e",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   req.Model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": answer},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// lastUserText returns the text of the last user message, whether it is a
// plain string or an array of content parts.
func lastUserText(messages []chatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "user" {
			continue
		}
		switch c := messages[i].Content.(type) {
		case string:
			return c
		case []any:
			var parts []string
			for _, part := range c {
				if m, ok := part.(map[string]any); ok {
					if text, ok := m["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
			return strings.Join(parts, "\n")
		}
	}
	return ""
}

// RequestsSince returns the chat requests received after the first n.
func (p *fakeProvider) RequestsSince(n int) []chatRequest {
	all := p.Requests()
	if n > len(all) {
		return nil
	}
	return all[n:]
}

// Requests returns a copy of every chat request received so far.
func (p *fakeProvider) Requests() []chatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]chatRequest(nil), p.requests...)
}

// ── Client helpers ───────────────────────────────────────────────────────────

type chatResult struct {
	Status  int
	Body    string
	Content string // choices[0].message.content when the call succeeded
}

// chat sends a non-streamed, single-message chat completion through the gateway.
func chat(t *testing.T, token, modelName, userText string) chatResult {
	t.Helper()
	return chatMessages(t, token, modelName, []map[string]any{{"role": "user", "content": userText}})
}

// chatMessages sends a non-streamed chat completion with the given messages.
func chatMessages(t *testing.T, token, modelName string, messages []map[string]any) chatResult {
	t.Helper()
	payload := mustJSON(map[string]any{
		"model":    modelName,
		"messages": messages,
	})
	req, err := http.NewRequest(http.MethodPost, env.baseURL+"/api/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	// The first call may download the NER model: leave it room.
	client := &http.Client{Timeout: 3 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("chat completion: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	res := chatResult{Status: resp.StatusCode, Body: string(body)}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &parsed) == nil && len(parsed.Choices) > 0 {
		res.Content = parsed.Choices[0].Message.Content
	}
	return res
}

// ── Events ───────────────────────────────────────────────────────────────────

// eventSnapshot is the set of event IDs recorded for the org at one point in
// time. Comparing snapshots isolates the events a single request produced,
// which a timestamp window cannot do reliably: events are written by a
// background worker and the previous test's event may land a moment later.
type eventSnapshot map[model.EventID]struct{}

func snapshotEvents(t *testing.T) eventSnapshot {
	t.Helper()
	snap := eventSnapshot{}
	for _, evt := range queryEvents(t) {
		snap[evt.ID()] = struct{}{}
	}
	return snap
}

// newEvents returns the events of the given type recorded since the snapshot.
func newEvents(t *testing.T, snap eventSnapshot, eventType string) []model.Event {
	t.Helper()
	var out []model.Event
	for _, evt := range queryEvents(t) {
		if _, seen := snap[evt.ID()]; seen || evt.Type() != eventType {
			continue
		}
		out = append(out, evt)
	}
	return out
}

// waitForEvent polls the events store until an event of the given type is
// recorded after the snapshot, or fails the test.
func waitForEvent(t *testing.T, snap eventSnapshot, eventType string) model.Event {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if events := newEvents(t, snap, eventType); len(events) > 0 {
			return events[0]
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("no %q event recorded within 10s", eventType)
	return nil
}

// assertNoEvent checks that no event of the given type was recorded after
// the snapshot, waiting long enough for the async writer to have flushed.
func assertNoEvent(t *testing.T, snap eventSnapshot, eventType string) {
	t.Helper()
	time.Sleep(time.Second)
	if events := newEvents(t, snap, eventType); len(events) > 0 {
		t.Fatalf("unexpected %q event: %s", eventType, events[0].Message())
	}
}

func queryEvents(t *testing.T) []model.Event {
	t.Helper()
	db, err := openDB(env.dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()

	orgID := model.OrgID(orgAcme)
	events, err := gormadapter.NewStore(db).QueryEvents(context.Background(), port.EventFilter{
		OrgID:    &orgID,
		AllUsers: true,
	})
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	return events
}
