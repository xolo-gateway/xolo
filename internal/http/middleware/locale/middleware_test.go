package locale

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"golang.org/x/text/language"

	"github.com/xolo-gateway/xolo/internal/i18n"
)

func bundle(t *testing.T) *i18n.Bundle {
	t.Helper()
	b, err := i18n.Load()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMiddlewareResolvesCookieThenHeaderThenDefault(t *testing.T) {
	var got language.Tag
	handler := Middleware(bundle(t), language.French, "/assets/", "/api/")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = i18n.Language(r.Context())
	}))
	for _, tc := range []struct {
		header, cookie string
		want           language.Tag
	}{
		{"", "", language.French},
		{"en-GB,en;q=0.9", "", language.English},
		{"es-419", "", language.Spanish},
		{"de", "", language.French},
		{"en", "es", language.Spanish},
		{"en", "de", language.English},
	} {
		req := httptest.NewRequest(http.MethodGet, "/orgs", nil)
		req.Header.Set("Accept-Language", tc.header)
		if tc.cookie != "" {
			req.AddCookie(&http.Cookie{Name: CookieName, Value: tc.cookie})
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if got != tc.want || rec.Header().Get("Content-Language") != tc.want.String() || !slices.Contains(rec.Header().Values("Vary"), "Accept-Language, Cookie") {
			t.Errorf("header=%q cookie=%q: language=%v headers=%v", tc.header, tc.cookie, got, rec.Header())
		}
	}
	for _, path := range []string{"/assets/app.css", "/api/v1/models"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Accept-Language", "es")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if got != language.Spanish || rec.Header().Get("Content-Language") != "" || len(rec.Header().Values("Vary")) != 0 {
			t.Errorf("%s: language=%v headers=%v", path, got, rec.Header())
		}
	}
}

func post(handler http.Handler, origin string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "http://xolo.example/locale", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestHandlerStoresTheChoiceAndRedirectsLocally(t *testing.T) {
	handler := Handler(bundle(t), true)
	for returnTo, location := range map[string]string{
		"/orgs/acme?tab=1":      "/orgs/acme?tab=1",
		"":                      "/",
		"//evil.example/path":   "/",
		"https://evil.example/": "/",
		"/\\evil.example":       "/",
		"/ok\r\nSet-Cookie: x":  "/",
	} {
		rec := post(handler, "", url.Values{"locale": {"es"}, "return_to": {returnTo}})
		cookies := rec.Result().Cookies()
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != location || len(cookies) != 1 {
			t.Fatalf("return_to %q: status=%d location=%q", returnTo, rec.Code, rec.Header().Get("Location"))
		}
		if c := cookies[0]; c.Name != CookieName || c.Value != "es" || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.MaxAge <= 0 || c.Path != "/" {
			t.Fatalf("cookie=%+v", c)
		}
	}
	for _, value := range []string{"", "de", "FR", "fr-CA"} {
		if rec := post(handler, "", url.Values{"locale": {value}}); rec.Code != http.StatusBadRequest || len(rec.Result().Cookies()) != 0 {
			t.Fatalf("locale %q: status=%d", value, rec.Code)
		}
	}
	if rec := post(handler, "https://evil.example", url.Values{"locale": {"en"}}); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status=%d", rec.Code)
	}
}

func TestHandlerRedirectAnnouncesNoLanguage(t *testing.T) {
	b := bundle(t)
	inner := Middleware(b, language.French)(Handler(b, true))
	// An outer layer, as CORS does, varies the response by its own header.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Origin")
		inner.ServeHTTP(w, r)
	})
	rec := post(handler, "", url.Values{"locale": {"en"}, "return_to": {"/orgs"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Content-Language") != "" || !slices.Equal(rec.Header().Values("Vary"), []string{"Origin"}) {
		t.Fatalf("status=%d headers=%v", rec.Code, rec.Header())
	}
}
