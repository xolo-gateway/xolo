package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// fakeAuthServer is an MCP server protected by its own OAuth authorization
// server: dynamic registration, PKCE, refresh tokens rotated on use and
// revoked with the whole authorization when replayed.
type fakeAuthServer struct {
	*httptest.Server

	mu       sync.Mutex
	codes    map[string]string // code -> PKCE challenge
	access   map[string]bool
	refresh  map[string]bool
	used     map[string]bool
	revoked  bool
	counter  int
	lastAuth string
}

func newFakeAuthServer(t *testing.T) *fakeAuthServer {
	t.Helper()
	s := &fakeAuthServer{codes: map[string]string{}, access: map[string]bool{}, refresh: map[string]bool{}, used: map[string]bool{}}

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "echoes text"}, echo)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		s.mu.Lock()
		valid := s.access[token] && !s.revoked
		s.lastAuth = token
		s.mu.Unlock()
		if !valid {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+s.URL+`/.well-known/oauth-protected-resource/mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"resource": s.URL + "/mcp", "authorization_servers": []string{s.URL}})
	})
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"issuer":                           s.URL,
			"authorization_endpoint":           s.URL + "/authorize",
			"token_endpoint":                   s.URL + "/token",
			"registration_endpoint":            s.URL + "/register",
			"response_types_supported":         []string{"code"},
			"code_challenge_methods_supported": []string{"S256"},
		})
	})
	mux.HandleFunc("POST /register", func(w http.ResponseWriter, r *http.Request) {
		var metadata map[string]any
		json.NewDecoder(r.Body).Decode(&metadata)
		metadata["client_id"] = "client-1"
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, metadata)
	})
	// The user is deemed to consent at once.
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("resource") != s.URL+"/mcp" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.counter++
		code := fmt.Sprintf("code-%d", s.counter)
		s.codes[code] = q.Get("code_challenge")
		s.mu.Unlock()
		redirect, _ := url.Parse(q.Get("redirect_uri"))
		redirect.RawQuery = url.Values{"code": {code}, "state": {q.Get("state")}}.Encode()
		http.Redirect(w, r, redirect.String(), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		s.mu.Lock()
		defer s.mu.Unlock()
		switch r.FormValue("grant_type") {
		case "authorization_code":
			challenge, ok := s.codes[r.FormValue("code")]
			delete(s.codes, r.FormValue("code"))
			sum := sha256.Sum256([]byte(r.FormValue("code_verifier")))
			if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
		case "refresh_token":
			token := r.FormValue("refresh_token")
			if s.used[token] {
				s.revoked = true
			}
			if !s.refresh[token] || s.revoked {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			delete(s.refresh, token)
			s.used[token] = true
		default:
			http.Error(w, `{"error":"unsupported_grant_type"}`, http.StatusBadRequest)
			return
		}
		s.counter++
		access, refresh := fmt.Sprintf("access-%d", s.counter), fmt.Sprintf("refresh-%d", s.counter)
		s.access[access], s.refresh[refresh] = true, true
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 3600, "refresh_token": refresh})
	})

	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// uiRequest runs a request through the plugin UI as the Xolo proxy would.
func uiRequest(t *testing.T, ui http.Handler, method, target, userID string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("X-Xolo-Org-Id", "~:"+userID)
	req.Header.Set("X-Xolo-User-Id", userID)
	req.Header.Set("X-Xolo-Plugin-Base-Path", "/profile/plugins/mcp-bridge/ui/")
	req.Header.Set("X-Xolo-Public-Base-URL", "https://xolo.example.net")
	rec := httptest.NewRecorder()
	ui.ServeHTTP(rec, req)
	return rec
}

// authorize runs the whole authorization of the user through the plugin UI.
func authorize(t *testing.T, p *Plugin, ui http.Handler, userID, nodeID, endpoint string) {
	t.Helper()

	connect := "/connect?" + url.Values{"nodeId": {nodeID}, "endpoint": {endpoint}}.Encode()
	if rec := uiRequest(t, ui, http.MethodGet, connect, userID, nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Autoriser") {
		t.Fatalf("connect page: %d %s", rec.Code, rec.Body.String())
	}

	rec := uiRequest(t, ui, http.MethodPost, "/connect", userID, url.Values{"nodeId": {nodeID}, "endpoint": {endpoint}})
	if rec.Code != http.StatusFound {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	authURL := rec.Header().Get("Location")

	res, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Get(authURL)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	res.Body.Close()
	callback, _ := url.Parse(res.Header.Get("Location"))
	if !strings.HasPrefix(callback.String(), "https://xolo.example.net/profile/plugins/mcp-bridge/ui/callback?") {
		t.Fatalf("unexpected redirect uri %s", callback)
	}

	if rec := uiRequest(t, ui, http.MethodGet, "/callback?"+callback.RawQuery, userID, nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "désormais") {
		t.Fatalf("callback: %d %s", rec.Code, rec.Body.String())
	}
}

func newOAuthPlugin(t *testing.T) (*Plugin, http.Handler, *fakeHostClient) {
	t.Helper()
	hc := newFakeHostClient()
	p := &Plugin{}
	p.SetHostClient(hc)
	p.oauthClient().host = func(context.Context) (pluginsdk.HostClient, string) { return hc, "mcp-bridge" }
	return p, newUIHandler(p.oauthClient()), hc
}

func oauthRequestContext(endpoint, userID string) *proto.RequestContext {
	return &proto.RequestContext{
		OrgId:      "org-1",
		NodeId:     "node-1",
		UserId:     userID,
		ConfigJson: `{"endpoint":"` + endpoint + `","authMode":"oauth","publicBaseURL":"https://xolo.example.net"}`,
	}
}

func toolNames(t *testing.T, p *Plugin, reqCtx *proto.RequestContext) []string {
	t.Helper()
	out, err := p.ListTools(context.Background(), &proto.ListToolsInput{Ctx: reqCtx})
	if err != nil {
		t.Fatalf("ListTools: %+v", err)
	}
	var names []string
	for _, tool := range out.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestPlugin_OAuth_ConnectThenCall(t *testing.T) {
	as := newFakeAuthServer(t)
	endpoint := as.URL + "/mcp"
	p, ui, _ := newOAuthPlugin(t)
	reqCtx := oauthRequestContext(endpoint, "user-1")

	if names := toolNames(t, p, reqCtx); len(names) != 1 || names[0] != connectToolName {
		t.Fatalf("expected the connect tool only before authorizing, got %v", names)
	}
	out, err := p.CallTool(context.Background(), &proto.CallToolInput{Ctx: reqCtx, Name: connectToolName, ArgumentsJson: "{}"})
	if err != nil || out.IsError || !strings.Contains(out.ResultText, "https://xolo.example.net/profile/plugins/mcp-bridge/ui/connect?") {
		t.Fatalf("expected the connect link, got %+v %v", out, err)
	}
	if out, _ := p.CallTool(context.Background(), &proto.CallToolInput{Ctx: reqCtx, Name: "echo", ArgumentsJson: `{"text":"hi"}`}); !out.IsError {
		t.Error("expected a tool call before authorizing to fail")
	}

	authorize(t, p, ui, "user-1", "node-1", endpoint)

	if names := toolNames(t, p, reqCtx); len(names) != 1 || names[0] != "echo" {
		t.Fatalf("expected the server tools once authorized, got %v", names)
	}
	out, err = p.CallTool(context.Background(), &proto.CallToolInput{Ctx: reqCtx, Name: "echo", ArgumentsJson: `{"text":"hi"}`})
	if err != nil || out.IsError || out.ResultText != "echo: hi" {
		t.Fatalf("CallTool: %+v %v", out, err)
	}

	// Another user of the same node has not authorized anything.
	if names := toolNames(t, p, oauthRequestContext(endpoint, "user-2")); len(names) != 1 || names[0] != connectToolName {
		t.Errorf("expected the connect tool for another user, got %v", names)
	}
	// A token is never sent to another server than the one it was obtained for.
	if names := toolNames(t, p, oauthRequestContext(as.URL+"/other", "user-1")); len(names) != 1 || names[0] != connectToolName {
		t.Errorf("expected the connect tool for another endpoint, got %v", names)
	}
}

func TestPlugin_OAuth_RefreshRotatesTheToken(t *testing.T) {
	as := newFakeAuthServer(t)
	endpoint := as.URL + "/mcp"
	p, ui, hc := newOAuthPlugin(t)
	reqCtx := oauthRequestContext(endpoint, "user-1")
	authorize(t, p, ui, "user-1", "node-1", endpoint)

	expire := func() {
		var token oauthToken
		json.Unmarshal([]byte(hc.secrets["node-1:"+oauthSecretKey("user-1")]), &token)
		token.Expiry = time.Now().Add(-time.Minute)
		data, _ := json.Marshal(token)
		hc.secrets["node-1:"+oauthSecretKey("user-1")] = string(data)
	}

	// Two refreshes in a row: the second one must present the rotated
	// refresh token, or the server revokes the authorization.
	for i := range 2 {
		expire()
		if names := toolNames(t, p, reqCtx); len(names) != 1 || names[0] != "echo" {
			t.Fatalf("refresh %d: expected the server tools, got %v", i, names)
		}
	}
	as.mu.Lock()
	revoked := as.revoked
	as.mu.Unlock()
	if revoked {
		t.Fatal("a refresh token was replayed")
	}

	// Once the authorization is revoked, the user is asked to authorize again.
	as.mu.Lock()
	as.revoked = true
	as.mu.Unlock()
	expire()
	if names := toolNames(t, p, reqCtx); len(names) != 1 || names[0] != connectToolName {
		t.Errorf("expected the connect tool after a revocation, got %v", names)
	}
}

func TestPlugin_OAuth_CallbackOfAnotherUserIsRejected(t *testing.T) {
	as := newFakeAuthServer(t)
	endpoint := as.URL + "/mcp"
	_, ui, hc := newOAuthPlugin(t)

	rec := uiRequest(t, ui, http.MethodPost, "/connect", "user-1", url.Values{"nodeId": {"node-1"}, "endpoint": {endpoint}})
	res, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Get(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	res.Body.Close()
	callback, _ := url.Parse(res.Header.Get("Location"))

	if rec := uiRequest(t, ui, http.MethodGet, "/callback?"+callback.RawQuery, "user-2", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("expected the callback of another user to be rejected, got %d", rec.Code)
	}
	if len(hc.secrets) != 0 {
		t.Errorf("expected no token to be stored, got %v", hc.secrets)
	}
	// The request is consumed: the legitimate user cannot replay it either.
	if rec := uiRequest(t, ui, http.MethodGet, "/callback?"+callback.RawQuery, "user-1", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("expected a consumed request to be rejected, got %d", rec.Code)
	}
}

func TestPlugin_OAuth_ConnectRejectsInvalidLinks(t *testing.T) {
	_, ui, _ := newOAuthPlugin(t)
	for _, target := range []string{"/connect?nodeId=node-1&endpoint=javascript:alert(1)", "/connect?endpoint=https://example.net/mcp"} {
		if rec := uiRequest(t, ui, http.MethodGet, target, "user-1", nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", target, rec.Code)
		}
	}
}
