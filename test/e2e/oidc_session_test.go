//go:build e2e

package e2e

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

const (
	fakeIdPProvider = "fakeidp"
	fakeIdPClientID = "xolo-e2e"
	fakeIdPSubject  = "e2e-oidc-subject"
)

// fakeIdP is an OIDC provider answering every authorization request at once
// for a single user, and signing logout tokens on demand.
type fakeIdP struct {
	srv *httptest.Server
	key *rsa.PrivateKey

	mu    sync.Mutex
	codes map[string]bool
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	idp := &fakeIdP{key: key, codes: map[string]bool{}}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 idp.issuer(),
			"authorization_endpoint": idp.issuer() + "/authorize",
			"token_endpoint":         idp.issuer() + "/token",
			"userinfo_endpoint":      idp.issuer() + "/userinfo",
			"jwks_uri":               idp.issuer() + "/jwks",
		})
	})
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		code := randomCode()
		idp.mu.Lock()
		idp.codes[code] = true
		idp.mu.Unlock()
		target, err := url.Parse(r.URL.Query().Get("redirect_uri"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		query := target.Query()
		query.Set("code", code)
		query.Set("state", r.URL.Query().Get("state"))
		target.RawQuery = query.Encode()
		http.Redirect(w, r, target.String(), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		idp.mu.Lock()
		known := idp.codes[r.PostForm.Get("code")]
		delete(idp.codes, r.PostForm.Get("code"))
		idp.mu.Unlock()
		if !known {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		now := time.Now()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access-" + randomCode(),
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token": idp.sign(t, jwt.MapClaims{
				"iss": idp.issuer(), "aud": fakeIdPClientID, "sub": fakeIdPSubject,
				"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
				"email": "oidc-e2e@example.test", "email_verified": true,
			}),
		})
	})
	mux.HandleFunc("GET /userinfo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"sub": fakeIdPSubject, "email": "oidc-e2e@example.test", "email_verified": true, "preferred_username": "oidc-e2e",
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	idp.srv = httptest.NewServer(mux)
	t.Cleanup(idp.srv.Close)
	return idp
}

func (idp *fakeIdP) issuer() string { return idp.srv.URL }

func (idp *fakeIdP) sign(t *testing.T, claims jwt.MapClaims) string {
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "k1"
	signed, err := token.SignedString(idp.key)
	require.NoError(t, err)
	return signed
}

func (idp *fakeIdP) logoutToken(t *testing.T, jti string) string {
	now := time.Now()
	return idp.sign(t, jwt.MapClaims{
		"iss": idp.issuer(), "aud": fakeIdPClientID, "sub": fakeIdPSubject, "jti": jti,
		"iat": now.Unix(), "exp": now.Add(2 * time.Minute).Unix(),
		"events": map[string]any{"http://schemas.openid.net/event/backchannel-logout": map[string]any{}},
	})
}

func randomCode() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// TestOIDCDurableSession signs in through a provider, restarts the server,
// then logs the identity out through the back channel: the cookie survives
// the restart and dies with the logout, a replayed logout changes nothing.
func TestOIDCDurableSession(t *testing.T) {
	dir := t.TempDir()
	idp := newFakeIdP(t)

	dsn := filepath.Join(dir, "oidc.sqlite")
	db, err := openDB(env.dsn)
	require.NoError(t, err)
	require.NoError(t, db.Exec("VACUUM INTO ?", dsn).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	port, err := freePort()
	require.NoError(t, err)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	prefix := "XOLO_HTTP_AUTHN_OIDC_PROVIDER_" + strings.ToUpper(fakeIdPProvider) + "_"
	config := append(serverEnv(env.pluginsDir, port, baseURL, dsn),
		"XOLO_HTTP_AUTHN_OIDC_PROVIDERS="+fakeIdPProvider,
		prefix+"KEY="+fakeIdPClientID,
		prefix+"SECRET=e2e-secret",
		prefix+"DISCOVERY_URL="+idp.issuer()+"/.well-known/openid-configuration",
		prefix+"LABEL=Fake IdP",
		"XOLO_HTTP_AUTHN_ACTIVE_BY_DEFAULT=true",
	)
	logPath := filepath.Join(dir, "server.log")
	start := func() func() {
		stop, err := launchServer(env.serverBin, logPath, config)
		require.NoError(t, err)
		require.NoError(t, waitReady(baseURL, 90*time.Second))
		return stop
	}
	stop := start()
	t.Cleanup(func() { stop() })
	t.Cleanup(func() {
		if t.Failed() {
			if logs, err := os.ReadFile(logPath); err == nil {
				t.Logf("---- oidc server log ----\n%s", logs)
			}
		}
	})

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	browser := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	signedIn := func() bool {
		t.Helper()
		client := &http.Client{Jar: jar, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
		resp, err := client.Get(baseURL + "/")
		require.NoError(t, err)
		resp.Body.Close()
		location := resp.Header.Get("Location")
		require.Falsef(t, resp.StatusCode >= 500, "status %d", resp.StatusCode)
		return !strings.Contains(location, "/auth/oidc/login")
	}

	resp, err := browser.Get(baseURL + "/auth/oidc/providers/" + fakeIdPProvider)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotContains(t, resp.Request.URL.Path, "/auth/", "the sign-in ends in the application")
	require.True(t, signedIn())

	// The session is persisted: another process accepts the same cookie.
	stop()
	stop = start()
	require.True(t, signedIn(), "the session survives a restart")

	logout := func(token string) int {
		t.Helper()
		resp, err := http.PostForm(baseURL+"/auth/oidc/providers/"+fakeIdPProvider+"/backchannel-logout", url.Values{"logout_token": {token}})
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
		return resp.StatusCode
	}
	token := idp.logoutToken(t, "e2e-logout-1")
	require.Equal(t, http.StatusOK, logout(token))
	require.False(t, signedIn(), "the cookie outlives the back-channel logout")

	// A new sign-in stands, even against a replay of the same logout token.
	resp, err = browser.Get(baseURL + "/auth/oidc/providers/" + fakeIdPProvider)
	require.NoError(t, err)
	resp.Body.Close()
	require.True(t, signedIn())
	require.Equal(t, http.StatusOK, logout(token))
	require.True(t, signedIn(), "a replayed logout token revoked the new session")

	require.Equal(t, http.StatusBadRequest, logout("not-a-token"))
	require.True(t, signedIn())
}
