package uilocale

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"golang.org/x/text/language"

	"github.com/xolo-gateway/xolo/internal/http/middleware/locale"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

func TestHandlerPrefersTheHostLanguage(t *testing.T) {
	bundle, err := i18n.Load()
	if err != nil {
		t.Fatal(err)
	}
	var got language.Tag
	handler := handler(bundle, language.English, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = i18n.Language(r.Context())
	}))
	for _, tc := range []struct {
		host, cookie, accept string
		want                 language.Tag
	}{
		{"es", "en", "en", language.Spanish},
		{"fr", "", "", language.French},
		{"de", "", "es", language.Spanish},
		{"", "es", "fr", language.Spanish},
		{"", "", "", language.English},
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(locale.Header, tc.host)
		req.Header.Set("Accept-Language", tc.accept)
		if tc.cookie != "" {
			req.AddCookie(&http.Cookie{Name: locale.CookieName, Value: tc.cookie})
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		vary := rec.Header().Values("Vary")
		if got != tc.want || rec.Header().Get("Content-Language") != tc.want.String() || !slices.Contains(vary, locale.Header) {
			t.Errorf("host=%q cookie=%q accept=%q: language=%v headers=%v, want %v", tc.host, tc.cookie, tc.accept, got, rec.Header(), tc.want)
		}
	}
}

func TestFallbackIsTheHostDefault(t *testing.T) {
	bundle, err := i18n.Load()
	if err != nil {
		t.Fatal(err)
	}
	for value, want := range map[string]language.Tag{"en": language.English, "es": language.Spanish, "": i18n.Unlocalized, "de": i18n.Unlocalized} {
		if got := fallback(bundle, value); got != want {
			t.Errorf("fallback(%q)=%v, want %v", value, got, want)
		}
	}
}
