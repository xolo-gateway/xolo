package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/sessions"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/ncruces/go-sqlite3/gormlite"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn"
	"gorm.io/gorm"
)

const (
	testIssuer   = "https://id.example.test/"
	testClientID = "xolo"
	testProvider = "idp"
)

var testTenant = func() model.Tenant {
	tenant := model.NewTenant("acme", "Acme", "")
	tenant.SetID("tenant-acme")
	return tenant
}()

func newRegistry(t *testing.T) *adapter.Store {
	t.Helper()
	db, err := gorm.Open(gormlite.Open("file:"+filepath.Join(t.TempDir(), "sessions.sqlite")+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(1000)"), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() {
		if pool, err := db.DB(); err == nil {
			pool.Close()
		}
	})
	store := adapter.NewStore(db)
	require.NoError(t, store.Migrate(context.Background()))
	return store
}

// browser carries the cookies of one user agent across requests.
type browser struct {
	cookies map[string]*http.Cookie
}

func newBrowser() *browser { return &browser{cookies: map[string]*http.Cookie{}} }

func (b *browser) request(method, target string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	for _, c := range b.cookies {
		r.AddCookie(c)
	}
	ctx := httpCtx.SetBaseURL(httpCtx.SetTenant(r.Context(), testTenant), "https://xolo.example.test")
	return r.WithContext(ctx)
}

func (b *browser) keep(rec *httptest.ResponseRecorder) {
	for _, c := range rec.Result().Cookies() {
		b.cookies[c.Name] = c
	}
}

func signedInUser() *authn.User {
	return &authn.User{Provider: testProvider, Issuer: testIssuer, Subject: "subject", Email: "u@example.test"}
}

// signIn runs the start and the end of a sign-in, as the provider routes do.
func signIn(t *testing.T, h *Handler, b *browser) error {
	t.Helper()
	rec := httptest.NewRecorder()
	require.NoError(t, h.startAuthentication(rec, b.request(http.MethodGet, "/providers/idp")))
	b.keep(rec)
	rec = httptest.NewRecorder()
	err := h.storeSessionUser(rec, b.request(http.MethodGet, "/providers/idp/callback"), signedInUser())
	b.keep(rec)
	return err
}

func authenticated(t *testing.T, h *Handler, b *browser) bool {
	t.Helper()
	user, err := h.Authenticate(httptest.NewRecorder(), b.request(http.MethodGet, "/"))
	require.NoError(t, err)
	return user != nil
}

func TestSessionRegistry(t *testing.T) {
	cookies := sessions.NewCookieStore([]byte("a-32-byte-key-for-session-tests!"))

	t.Run("a session survives a restart and dies at logout", func(t *testing.T) {
		registry := newRegistry(t)
		b := newBrowser()
		require.NoError(t, signIn(t, NewHandler(cookies, WithSessionRegistry(registry)), b))

		// Another process sharing the database and the cookie keys.
		restarted := NewHandler(cookies, WithSessionRegistry(registry))
		require.True(t, authenticated(t, restarted, b))

		stolen := newBrowser()
		for name, c := range b.cookies {
			stolen.cookies[name] = c
		}
		rec := httptest.NewRecorder()
		restarted.handleLogout(rec, b.request(http.MethodGet, "/logout"))
		require.Equal(t, http.StatusTemporaryRedirect, rec.Code)
		require.False(t, authenticated(t, restarted, stolen), "a copy of the cookie outlives the logout")
	})

	t.Run("a cookie issued without registry is refused", func(t *testing.T) {
		b := newBrowser()
		rec := httptest.NewRecorder()
		require.NoError(t, NewHandler(cookies).storeSessionUser(rec, b.request(http.MethodGet, "/"), signedInUser()))
		b.keep(rec)
		require.False(t, authenticated(t, NewHandler(cookies, WithSessionRegistry(newRegistry(t))), b))
	})

	t.Run("a sign-in must have started, recently", func(t *testing.T) {
		h := NewHandler(cookies, WithSessionRegistry(newRegistry(t)))
		require.Error(t, h.storeSessionUser(httptest.NewRecorder(), newBrowser().request(http.MethodGet, "/"), signedInUser()))

		b := newBrowser()
		r := b.request(http.MethodGet, "/")
		sess, err := h.getSession(r)
		require.NoError(t, err)
		sess.Values[authenticationStartedAttr] = time.Now().Add(-model.LoginMaxDuration - time.Minute).UnixNano()
		require.Error(t, h.storeSessionUser(httptest.NewRecorder(), r, signedInUser()))
	})

	t.Run("a revocation ends the session and the sign-ins started before it", func(t *testing.T) {
		registry := newRegistry(t)
		h := NewHandler(cookies, WithSessionRegistry(registry))
		b := newBrowser()
		require.NoError(t, signIn(t, h, b))

		pending := newBrowser()
		rec := httptest.NewRecorder()
		require.NoError(t, h.startAuthentication(rec, pending.request(http.MethodGet, "/providers/idp")))
		pending.keep(rec)

		time.Sleep(10 * time.Millisecond)
		now := time.Now()
		revoked, err := registry.RevokeIdentitySessions(context.Background(), testIssuer, "subject", "jti", now, now.Add(time.Minute))
		require.NoError(t, err)
		require.True(t, revoked)
		require.False(t, authenticated(t, h, b))

		err = h.storeSessionUser(httptest.NewRecorder(), pending.request(http.MethodGet, "/providers/idp/callback"), signedInUser())
		require.ErrorIs(t, err, port.ErrNotAllowed)

		require.NoError(t, signIn(t, h, newBrowser()))
	})

	t.Run("a provider without proven issuer gets a pseudo-issuer", func(t *testing.T) {
		registry := newRegistry(t)
		h := NewHandler(cookies, WithSessionRegistry(registry))
		b := newBrowser()
		rec := httptest.NewRecorder()
		require.NoError(t, h.startAuthentication(rec, b.request(http.MethodGet, "/providers/github")))
		b.keep(rec)
		rec = httptest.NewRecorder()
		require.NoError(t, h.storeSessionUser(rec, b.request(http.MethodGet, "/"), &authn.User{Provider: "github", Subject: "42"}))
		b.keep(rec)
		require.True(t, authenticated(t, h, b))
	})
}

// newLogoutSigner serves a JWKS and signs logout tokens for testIssuer.
func newLogoutSigner(t *testing.T) (string, func(jwt.MapClaims) string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func(claims jwt.MapClaims) string {
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		token.Header["kid"] = "k1"
		signed, err := token.SignedString(key)
		require.NoError(t, err)
		return signed
	}
}

func logoutToken(jti string) jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss": testIssuer, "aud": testClientID, "sub": "subject", "jti": jti,
		"iat": now.Unix(), "exp": now.Add(2 * time.Minute).Unix(),
		"events": map[string]any{"http://schemas.openid.net/event/backchannel-logout": map[string]any{}},
	}
}

func postLogout(h *Handler, provider, contentType, query, body string) *httptest.ResponseRecorder {
	target := "/providers/" + provider + "/backchannel-logout"
	if query != "" {
		target += "?" + query
	}
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

type failingRegistry struct {
	port.SessionRegistry
	err error
}

func (r failingRegistry) RevokeIdentitySessions(context.Context, string, string, string, time.Time, time.Time) (bool, error) {
	return false, r.err
}

func TestBackchannelLogout(t *testing.T) {
	jwksURL, sign := newLogoutSigner(t)
	keysDown := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusInternalServerError)
	}))
	t.Cleanup(keysDown.Close)
	cookies := sessions.NewCookieStore([]byte("a-32-byte-key-for-session-tests!"))
	providers := []ProviderWithJWKS{
		{ID: testProvider, Issuer: testIssuer, ProvesIssuer: true, ClientID: testClientID, JWKSURL: jwksURL},
		{ID: "keysdown", Issuer: testIssuer, ProvesIssuer: true, ClientID: testClientID, JWKSURL: keysDown.URL},
		{ID: "noclient", Issuer: testIssuer, ProvesIssuer: true, JWKSURL: jwksURL},
		{ID: "github", Issuer: "https://github.com", ClientID: testClientID, JWKSURL: jwksURL},
	}
	registry := newRegistry(t)
	h := NewHandler(cookies, WithSessionRegistry(registry), WithProvidersWithJWKS(providers))
	form := func(token string) string { return url.Values{"logout_token": {token}}.Encode() }
	const formType = "application/x-www-form-urlencoded"

	b := newBrowser()
	require.NoError(t, signIn(t, h, b))

	for name, c := range map[string]struct {
		provider, contentType, query, body string
		status                             int
	}{
		"unknown provider":        {"unknown", formType, "", form(sign(logoutToken("a"))), http.StatusNotFound},
		"provider without client": {"noclient", formType, "", form(sign(logoutToken("a"))), http.StatusNotFound},
		"unproven issuer":         {"github", formType, "", form(sign(logoutToken("a"))), http.StatusNotFound},
		"json body":               {testProvider, "application/json", "", `{"logout_token":"x"}`, http.StatusBadRequest},
		"query string":            {testProvider, formType, "logout_token=x", form(sign(logoutToken("a"))), http.StatusBadRequest},
		"extra parameter":         {testProvider, formType, "", form(sign(logoutToken("a"))) + "&state=x", http.StatusBadRequest},
		"invalid token":           {testProvider, formType, "", form("not-a-token"), http.StatusBadRequest},
		// The token could not be checked: the provider must retry, not drop it.
		"keys unavailable": {"keysdown", formType, "", form(sign(logoutToken("a"))), http.StatusServiceUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			rec := postLogout(h, c.provider, c.contentType, c.query, c.body)
			require.Equal(t, c.status, rec.Code)
			require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
		})
	}
	require.True(t, authenticated(t, h, b), "a refused request revokes nothing")

	token := sign(logoutToken("logout-1"))
	rec := postLogout(h, testProvider, formType, "", form(token))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	require.False(t, authenticated(t, h, b))

	// A replay is acknowledged and changes nothing: the next session stands.
	again := newBrowser()
	require.NoError(t, signIn(t, h, again))
	rec = postLogout(h, testProvider, formType, "", form(token))
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, authenticated(t, h, again))

	// Only an unavailable dependency asks the provider to retry.
	for err, status := range map[error]int{
		errors.New("database down"): http.StatusServiceUnavailable,
		port.ErrInvalid:             http.StatusBadRequest,
	} {
		failing := NewHandler(cookies, WithSessionRegistry(failingRegistry{registry, err}), WithProvidersWithJWKS(providers))
		rec = postLogout(failing, testProvider, formType, "", form(sign(logoutToken("logout-2"))))
		require.Equal(t, status, rec.Code, err.Error())
	}

	// Without a registry there is nothing to revoke: the route does not exist.
	unregistered := NewHandler(cookies, WithProvidersWithJWKS(providers))
	rec = postLogout(unregistered, testProvider, formType, "", form(sign(logoutToken("logout-3"))))
	require.Equal(t, http.StatusNotFound, rec.Code)
}
