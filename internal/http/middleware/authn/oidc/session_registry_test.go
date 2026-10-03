package oidc

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/sessions"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/ncruces/go-sqlite3/gormlite"
	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn"
	"gorm.io/gorm"
)

func TestSessionRegistryRejectsRevokedAndLegacyCookies(t *testing.T) {
	db, err := gorm.Open(gormlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	defer pool.Close()
	registry := adapter.NewStore(db)
	require.NoError(t, registry.Migrate(t.Context()))
	cookies := sessions.NewCookieStore([]byte("a-32-byte-key-for-session-testing"))
	handler := NewHandler(cookies, WithSessionRegistry(registry))
	proof := &authn.User{Provider: "idp", Issuer: "https://issuer.test", Subject: "subject", AuthenticatedAt: time.Now().Add(-time.Second)}
	request := httptest.NewRequest("GET", "/", nil)
	response := httptest.NewRecorder()
	require.NoError(t, handler.storeSessionUser(response, request, proof))
	replay := func() *authn.User {
		r := httptest.NewRequest("GET", "/", nil)
		for _, c := range response.Result().Cookies() {
			r.AddCookie(c)
		}
		u, _ := handler.retrieveSessionUser(r)
		return u
	}
	require.NotNil(t, replay())
	require.NoError(t, registry.RevokeIdentitySessions(t.Context(), proof.Issuer, proof.Subject, "logout", time.Now()))
	handler = NewHandler(cookies, WithSessionRegistry(adapter.NewStore(db)))
	require.Nil(t, replay())
	legacy := NewHandler(cookies)
	request = httptest.NewRequest("GET", "/", nil)
	response = httptest.NewRecorder()
	require.NoError(t, legacy.storeSessionUser(response, request, &authn.User{Provider: "idp", Subject: "subject"}))
	require.Nil(t, replay())
	// Login started before the logout cannot create a replacement cookie.
	response = httptest.NewRecorder()
	require.Error(t, handler.storeSessionUser(response, httptest.NewRequest("GET", "/", nil), proof))
}
