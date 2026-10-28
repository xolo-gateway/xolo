package uilocale

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/text/language"

	"github.com/xolo-gateway/xolo/internal/http/middleware/locale"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

func TestHandlerPrefersTheHostLanguage(t *testing.T) {
	var got language.Tag
	handler := Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = i18n.Language(r.Context())
	}))
	for _, tc := range []struct {
		host, cookie, accept string
		want                 language.Tag
	}{
		{"en", "es", "es", language.English},
		{"es", "", "", language.Spanish},
		{"de", "", "en", language.English},
		{"", "es", "en", language.Spanish},
		{"", "", "", language.French},
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(locale.Header, tc.host)
		req.Header.Set("Accept-Language", tc.accept)
		if tc.cookie != "" {
			req.AddCookie(&http.Cookie{Name: locale.CookieName, Value: tc.cookie})
		}
		handler.ServeHTTP(httptest.NewRecorder(), req)
		if got != tc.want {
			t.Errorf("host=%q cookie=%q accept=%q: language=%v, want %v", tc.host, tc.cookie, tc.accept, got, tc.want)
		}
	}
}
