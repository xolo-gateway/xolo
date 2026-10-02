package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// fakeUIHost implements pluginsdk.HostClient with configurable hooks so the
// tests can pin the data returned by GetConfig/SaveConfig.
type fakeUIHost struct {
	mu          sync.Mutex
	getConfigFn func(ctx context.Context, orgID, pluginName string) (string, error)
	saveConfigs []string
	saveErr     error
}

func newFakeUIHost() *fakeUIHost {
	return &fakeUIHost{
		getConfigFn: func(context.Context, string, string) (string, error) { return "{}", nil },
	}
}

func (h *fakeUIHost) GetConfig(ctx context.Context, orgID, pluginName string) (string, error) {
	return h.getConfigFn(ctx, orgID, pluginName)
}

func (h *fakeUIHost) SaveConfig(_ context.Context, _, _, configJSON string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.saveConfigs = append(h.saveConfigs, configJSON)
	return h.saveErr
}

func (h *fakeUIHost) ListModels(context.Context, string) ([]*proto.ModelInfo, error) {
	return nil, nil
}
func (h *fakeUIHost) GetSecret(context.Context, string, string, string, string) (string, bool, error) {
	return "", false, nil
}
func (h *fakeUIHost) SetSecret(context.Context, string, string, string, string, string) error {
	return nil
}
func (h *fakeUIHost) DeleteSecret(context.Context, string, string, string, string) error {
	return nil
}
func (h *fakeUIHost) EmitEvent(context.Context, pluginsdk.Event) error { return nil }
func (h *fakeUIHost) ChatCompletion(context.Context, *proto.HostChatCompletionRequest) (*proto.HostChatCompletionResponse, error) {
	return nil, nil
}

var _ pluginsdk.HostClient = (*fakeUIHost)(nil)

// captureLogs swaps slog's default handler for one that writes JSON records
// into the returned buffer. It returns the previous handler so the caller can
// restore it via t.Cleanup. All assertions parse JSON records line by line,
// which lets us check both the level and the field values without depending
// on Go's free-text format.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// logRecords returns the JSON records emitted during the test, one per record.
func logRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(buf.String(), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("parse log line %q: %v", line, err)
		}
		records = append(records, rec)
	}
	return records
}

func uiRequest(method, target, body, orgID string, host pluginsdk.HostClient) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	if orgID != "" {
		r.Header.Set("X-Xolo-Org-Id", orgID)
	}
	ctx := pluginsdk.ContextWithHostClientForTest(r.Context(), host)
	ctx = pluginsdk.ContextWithPluginNameForTest(ctx, "system-prompt")
	return r.WithContext(ctx)
}

const secretPrompt = "Tu es l'assistant de ACME Corp, secret: API_KEY=sk-test-12345"

// TestHandleSaveConfig_DoesNotLogPrompt verifies the regression fixed by
// issue #122: the configured system prompt must never appear in any log
// record, at any level. The previous code emitted slog.Error("system-prompt:
// SystemPrompt", slog.Any("error", cfg.SystemPrompt)) on the success path.
func TestHandleSaveConfig_DoesNotLogPrompt(t *testing.T) {
	host := newFakeUIHost()
	buf := captureLogs(t)
	handler := newUIHandler()

	req := uiRequest(http.MethodPost, "/api/config",
		"system_prompt="+secretPrompt+"&append=true",
		"org-1", host)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if buf.Len() == 0 {
		t.Fatal("no log records captured; the handler should at least emit a save log line")
	}
	for _, rec := range logRecords(t, buf) {
		serialized, _ := json.Marshal(rec)
		if strings.Contains(string(serialized), secretPrompt) {
			t.Errorf("prompt leaked into log record: %s", serialized)
		}
	}
	for _, rec := range logRecords(t, buf) {
		if rec["level"] == "ERROR" {
			t.Errorf("successful save must not emit ERROR; got record %s", rec)
		}
	}
}

// TestHandleSaveConfig_LogFields checks the success log records the act of
// saving with non-sensitive metrics only.
func TestHandleSaveConfig_LogFields(t *testing.T) {
	host := newFakeUIHost()
	buf := captureLogs(t)
	handler := newUIHandler()

	req := uiRequest(http.MethodPost, "/api/config",
		"system_prompt="+secretPrompt+"&append=true",
		"org-1", host)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	var found bool
	for _, rec := range logRecords(t, buf) {
		if rec["msg"] != "system-prompt: config saved" {
			continue
		}
		found = true
		if rec["level"] != "INFO" {
			t.Errorf("save log level = %v, want INFO", rec["level"])
		}
		if rec["org_id"] != "org-1" {
			t.Errorf("save log org_id = %v, want org-1", rec["org_id"])
		}
		if rec["append"] != true {
			t.Errorf("save log append = %v, want true", rec["append"])
		}
		want := float64(len(secretPrompt))
		if rec["prompt_length"] != want {
			t.Errorf("save log prompt_length = %v, want %v", rec["prompt_length"], want)
		}
	}
	if !found {
		t.Fatalf("no 'config saved' log record found in: %s", buf.String())
	}
	if len(host.saveConfigs) != 1 {
		t.Fatalf("expected one save call, got %d", len(host.saveConfigs))
	}
}

// TestHandleSaveConfig_SaveError_NoSuccessLog verifies that when SaveConfig
// fails, the handler does not emit a misleading 'config saved' INFO. The
// success log is only emitted after the save returns nil.
func TestHandleSaveConfig_SaveError_NoSuccessLog(t *testing.T) {
	host := newFakeUIHost()
	host.saveErr = errors.New("storage unavailable")
	buf := captureLogs(t)
	handler := newUIHandler()

	req := uiRequest(http.MethodPost, "/api/config",
		"system_prompt=hello&append=false",
		"org-1", host)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	for _, rec := range logRecords(t, buf) {
		if rec["msg"] == "system-prompt: config saved" {
			t.Errorf("failed save must not emit 'config saved'; got %s", rec)
		}
	}
}

// TestHandleSaveConfig_MissingContext_LogsWarn verifies the early-return
// branch logs a warn without putting any value under the 'error' key (the
// previous code put an int there).
func TestHandleSaveConfig_MissingContext_LogsWarn(t *testing.T) {
	buf := captureLogs(t)
	handler := newUIHandler()

	req := uiRequest(http.MethodPost, "/api/config",
		"system_prompt=hello&append=false",
		"", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	var found bool
	for _, rec := range logRecords(t, buf) {
		if !strings.Contains(rec["msg"].(string), "without host or org context") {
			continue
		}
		found = true
		if rec["level"] != "WARN" {
			t.Errorf("expected WARN, got %v", rec["level"])
		}
		if _, ok := rec["error"]; ok {
			t.Errorf("'error' key must be absent on the missing-context branch; got %v", rec["error"])
		}
	}
	if !found {
		t.Fatalf("no warn record found in: %s", buf.String())
	}
}

// TestHandleIndex_DoesNotLogRawConfig verifies the load path does not leak
// the stored configuration JSON. The previous code emitted
// slog.String("raw", raw) on every GET, where raw contains the full
// SystemPrompt field.
func TestHandleIndex_DoesNotLogRawConfig(t *testing.T) {
	rawConfig, err := json.Marshal(Config{
		SystemPrompt: secretPrompt,
		Append:       true,
	})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	host := newFakeUIHost()
	host.getConfigFn = func(context.Context, string, string) (string, error) {
		return string(rawConfig), nil
	}
	buf := captureLogs(t)
	handler := newUIHandler()

	req := uiRequest(http.MethodGet, "/", "", "org-1", host)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	for _, rec := range logRecords(t, buf) {
		serialized, _ := json.Marshal(rec)
		if strings.Contains(string(serialized), secretPrompt) {
			t.Errorf("prompt leaked into load log: %s", serialized)
		}
		if strings.Contains(string(serialized), string(rawConfig)) {
			t.Errorf("raw config JSON leaked into load log: %s", serialized)
		}
	}
	var found bool
	for _, rec := range logRecords(t, buf) {
		if rec["msg"] != "system-prompt: config loaded" {
			continue
		}
		found = true
		if rec["level"] != "INFO" {
			t.Errorf("load log level = %v, want INFO", rec["level"])
		}
		if rec["raw_length"] != float64(len(rawConfig)) {
			t.Errorf("load log raw_length = %v, want %v", rec["raw_length"], len(rawConfig))
		}
	}
	if !found {
		t.Fatalf("no 'config loaded' log record found in: %s", buf.String())
	}
}

// TestHandleIndex_GetConfigError_NoSuccessLog verifies that on a GetConfig
// failure, the handler does not emit the 'config loaded' INFO line. Only the
// WARN line is emitted, with no raw payload misreads as a success.
func TestHandleIndex_GetConfigError_NoSuccessLog(t *testing.T) {
	host := newFakeUIHost()
	host.getConfigFn = func(context.Context, string, string) (string, error) {
		return "", errors.New("boom")
	}
	buf := captureLogs(t)
	handler := newUIHandler()

	req := uiRequest(http.MethodGet, "/", "", "org-1", host)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	var foundInfo bool
	for _, rec := range logRecords(t, buf) {
		if rec["msg"] == "system-prompt: config loaded" {
			foundInfo = true
		}
	}
	if foundInfo {
		t.Errorf("failed GetConfig must not emit 'config loaded'; records: %s", buf.String())
	}
	var foundWarn bool
	for _, rec := range logRecords(t, buf) {
		if strings.Contains(rec["msg"].(string), "failed to load config") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Errorf("expected a 'failed to load config' warn, got: %s", buf.String())
	}
}
