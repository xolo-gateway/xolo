package passthrough

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	httpx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn"
)

const testUpstreamCredential = "Bearer sk-ant-oat-upstream-credential"

type recordedCall struct {
	identity  identity
	modelName string
	usage     TokenUsage
}

type spyRecorder struct {
	calls []recordedCall
}

func (s *spyRecorder) Record(_ context.Context, id identity, modelName string, usage TokenUsage) {
	s.calls = append(s.calls, recordedCall{identity: id, modelName: modelName, usage: usage})
}

// authenticatedContext mirrors what the authn + bridge + memberships chain
// leaves on the context before the relay runs.
func authenticatedContext(t *testing.T) context.Context {
	t.Helper()

	user := model.NewUser("tenant-1", "oidc", "subject-1", "dev@example.com", "Dev", true)

	ctx := httpx.SetUser(context.Background(), user)
	return authn.SetContextUser(ctx, &authn.User{
		Email:    "dev@example.com",
		Provider: "oidc",
		Subject:  "subject-1",
		OrgID:    "org-1",
		TokenID:  "token-1",
	})
}

// newTestHandler wires a relay against a stub upstream and returns both, plus
// the recorder spy.
func newTestHandler(t *testing.T, upstream http.HandlerFunc) (*Handler, *spyRecorder, *httptest.Server) {
	t.Helper()

	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)

	recorder := &spyRecorder{}
	handler, err := NewHandler(server.URL, "X-Xolo-Key", []string{"/v1/messages"}, 30*time.Second, recorder)
	if err != nil {
		t.Fatalf("could not create handler: %v", err)
	}

	return handler, recorder, server
}

func TestHandlerRelaysUpstreamCredential(t *testing.T) {
	var (
		gotAuthorization string
		gotXoloHeader    string
		gotPath          string
		gotQuery         string
	)

	handler, _, _ := newTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		gotXoloHeader = r.Header.Get("X-Xolo-Key")
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"message","model":"m","usage":{"input_tokens":1,"output_tokens":1}}`))
	})

	ctx := context.WithValue(authenticatedContext(t), contextKeyUpstreamCredential, testUpstreamCredential)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages?beta=true", strings.NewReader(`{}`)).WithContext(ctx)
	req.Header.Set("X-Xolo-Key", "should-not-be-forwarded")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotAuthorization != testUpstreamCredential {
		t.Errorf("upstream Authorization = %q, want the client credential", gotAuthorization)
	}
	if gotXoloHeader != "" {
		t.Errorf("Xolo credential leaked upstream: %q", gotXoloHeader)
	}
	if gotPath != "/v1/messages" {
		t.Errorf("upstream path = %q, want /v1/messages", gotPath)
	}
	if gotQuery != "beta=true" {
		t.Errorf("upstream query = %q, want beta=true — clients depend on it", gotQuery)
	}
}

func TestHandlerRecordsUsageFromStreamedResponse(t *testing.T) {
	handler, recorder, _ := newTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-sonnet-5\",\"usage\":{\"input_tokens\":500,\"cache_read_input_tokens\":100}}}\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":25}}\n")
	})

	ctx := context.WithValue(authenticatedContext(t), contextKeyUpstreamCredential, testUpstreamCredential)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`)).WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if len(recorder.calls) != 1 {
		t.Fatalf("recorded %d calls, want 1", len(recorder.calls))
	}

	call := recorder.calls[0]
	if call.modelName != "claude-sonnet-5" {
		t.Errorf("model = %q, want claude-sonnet-5", call.modelName)
	}
	if got, want := call.usage.PromptTokens(), 600; got != want {
		t.Errorf("PromptTokens = %d, want %d", got, want)
	}
	if call.usage.OutputTokens != 25 {
		t.Errorf("OutputTokens = %d, want 25", call.usage.OutputTokens)
	}
	if call.identity.OrgID != "org-1" {
		t.Errorf("OrgID = %q, want org-1", call.identity.OrgID)
	}
	if call.identity.AuthTokenID != "token-1" {
		t.Errorf("AuthTokenID = %q, want token-1", call.identity.AuthTokenID)
	}
}

// An upstream error bills nothing, so recording it would inflate the ledger
// with usage the operator was never charged for.
func TestHandlerDoesNotRecordFailedCalls(t *testing.T) {
	handler, recorder, _ := newTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","usage":{"input_tokens":10,"output_tokens":0}}`))
	})

	ctx := context.WithValue(authenticatedContext(t), contextKeyUpstreamCredential, testUpstreamCredential)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`)).WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want the upstream 429 relayed verbatim", rec.Code)
	}
	if len(recorder.calls) != 0 {
		t.Errorf("recorded %d calls, want none", len(recorder.calls))
	}
}

// Enabling the relay for one endpoint must not implicitly open the whole
// upstream API.
func TestHandlerRefusesPathsOutsideAllowlist(t *testing.T) {
	reached := false
	handler, _, _ := newTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		reached = true
	})

	ctx := context.WithValue(authenticatedContext(t), contextKeyUpstreamCredential, testUpstreamCredential)
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations", strings.NewReader(`{}`)).WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if reached {
		t.Error("request reached the upstream despite being outside the allowlist")
	}
}

func TestHandlerRejectsMissingUpstreamCredential(t *testing.T) {
	reached := false
	handler, _, _ := newTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		reached = true
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`)).
		WithContext(authenticatedContext(t))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if reached {
		t.Error("request reached the upstream without a credential to relay")
	}
}

func TestHandlerRejectsUnidentifiedCaller(t *testing.T) {
	reached := false
	handler, _, _ := newTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		reached = true
	})

	ctx := context.WithValue(context.Background(), contextKeyUpstreamCredential, testUpstreamCredential)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`)).WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if reached {
		t.Error("an unidentified request reached the upstream")
	}
}

// The client refuses to send any request until this probe succeeds, and it
// sends it with no credentials at all.
func TestProbeHandlerAnswersWithoutCredentials(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/hello", nil)
			rec := httptest.NewRecorder()

			ProbeHandler().ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", rec.Code)
			}
		})
	}
}
