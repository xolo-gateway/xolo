package oidc

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/sessions"
	"github.com/stretchr/testify/require"
)

func newLoginRedirectHandler(providers ...Provider) *Handler {
	return NewHandler(sessions.NewCookieStore([]byte("a-32-byte-key-for-session-tests!")), WithProviders(providers...))
}

// storedNextURL reads the post-login path the browser's session holds.
func storedNextURL(t *testing.T, h *Handler, b *browser) (string, bool) {
	t.Helper()
	sess, err := h.getSession(b.request(http.MethodGet, "/auth/oidc/providers/idp/callback"))
	require.NoError(t, err)
	next, ok := sess.Values[nextURLAttr].(string)
	return next, ok
}

func TestLoginRedirectSkipsProviderChoiceOnce(t *testing.T) {
	h := newLoginRedirectHandler(Provider{ID: "dex"})
	b := newBrowser()

	rec := httptest.NewRecorder()
	h.LoginRedirect(rec, b.request(http.MethodGet, "/orgs/home/admin/providers?tab=models"))
	b.keep(rec)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/auth/oidc/providers/dex", rec.Header().Get("Location"))
	next, ok := storedNextURL(t, h, b)
	require.True(t, ok)
	require.Equal(t, "/orgs/home/admin/providers?tab=models", next)

	// A failed sign-in clears the session and comes back: the login page is
	// shown instead of looping through the identity provider.
	delete(b.cookies, "xolo_auth_oidc")
	rec = httptest.NewRecorder()
	h.LoginRedirect(rec, b.request(http.MethodGet, "/orgs/home/"))
	require.Equal(t, "/auth/oidc/login", rec.Header().Get("Location"))
}

func TestLoginRedirectShowsChoiceWithSeveralProviders(t *testing.T) {
	h := newLoginRedirectHandler(Provider{ID: "dex"}, Provider{ID: "google"})
	b := newBrowser()

	rec := httptest.NewRecorder()
	h.LoginRedirect(rec, b.request(http.MethodGet, "/orgs/home/"))
	b.keep(rec)
	require.Equal(t, "/auth/oidc/login", rec.Header().Get("Location"))
	next, ok := storedNextURL(t, h, b)
	require.True(t, ok)
	require.Equal(t, "/orgs/home/", next)
}

func TestLoginRedirectRemembersOnlyPageNavigations(t *testing.T) {
	for _, c := range []struct {
		name   string
		method string
		target string
		htmx   bool
	}{
		{"root", http.MethodGet, "/", false},
		{"htmx fragment", http.MethodGet, "/orgs/home/", true},
		{"form submission", http.MethodPost, "/orgs/home/", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newLoginRedirectHandler(Provider{ID: "dex"}, Provider{ID: "google"})
			b := newBrowser()
			r := b.request(c.method, c.target)
			if c.htmx {
				r.Header.Set("HX-Request", "true")
			}
			rec := httptest.NewRecorder()
			h.LoginRedirect(rec, r)
			require.Empty(t, rec.Result().Cookies(), "a hit that stores nothing saved the session")
			b.keep(rec)
			_, ok := storedNextURL(t, h, b)
			require.False(t, ok)
		})
	}
}

func TestLocalPath(t *testing.T) {
	for value, want := range map[string]bool{
		"/orgs/home/":           true,
		"/orgs/home/?a=b":       true,
		"":                      false,
		"orgs":                  false,
		"//evil.example":        false,
		`/\evil.example`:        false,
		"https://evil.example/": false,
		"/a\r\nSet-Cookie: x":   false,
	} {
		_, ok := localPath(value)
		require.Equal(t, want, ok, value)
	}
}

// Signing out of Xolo leaves the session at the identity provider open: the
// next unauthenticated page shows the login page instead of signing the
// browser straight back in.
func TestLoginRedirectShowsChoiceAfterLogout(t *testing.T) {
	h := newLoginRedirectHandler(Provider{ID: "dex"})
	b := newBrowser()

	rec := httptest.NewRecorder()
	h.handleLogout(rec, b.request(http.MethodGet, "/auth/oidc/logout"))
	b.keep(rec)

	rec = httptest.NewRecorder()
	h.LoginRedirect(rec, b.request(http.MethodGet, "/"))
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/auth/oidc/login", rec.Header().Get("Location"))
}

// A previously stored path that the current hit does not replace is removed,
// which needs the session to be saved.
func TestLoginRedirectForgetsPreviousPath(t *testing.T) {
	h := newLoginRedirectHandler(Provider{ID: "dex"}, Provider{ID: "google"})
	b := newBrowser()

	rec := httptest.NewRecorder()
	h.LoginRedirect(rec, b.request(http.MethodGet, "/orgs/home/"))
	b.keep(rec)

	rec = httptest.NewRecorder()
	h.LoginRedirect(rec, b.request(http.MethodGet, "/"))
	b.keep(rec)
	_, ok := storedNextURL(t, h, b)
	require.False(t, ok)
}
