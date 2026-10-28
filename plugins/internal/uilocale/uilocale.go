// Package uilocale renders a plugin UI in the language of the console page
// that embeds it.
//
// The host reverse-proxies plugin UI requests with the language it resolved
// in the X-Xolo-Locale header (cookie, then Accept-Language, then its
// configured default), and a plugin renders in it. Without the header, as
// when the UI is reached outside the host, the plugin negotiates from the
// forwarded Cookie and Accept-Language headers, then falls back to French.
package uilocale

import (
	"net/http"

	"github.com/xolo-gateway/xolo/internal/http/middleware/locale"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

// Handler puts the request language in the context of every request to h.
func Handler(h http.Handler) http.Handler {
	bundle, err := i18n.Load()
	if err != nil {
		panic("load embedded locales: " + err.Error())
	}
	negotiated := locale.Middleware(bundle, i18n.Unlocalized)(h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tag, ok := bundle.Lookup(r.Header.Get(locale.Header))
		if !ok {
			negotiated.ServeHTTP(w, r)
			return
		}
		h.ServeHTTP(w, r.WithContext(bundle.WithLanguage(r.Context(), tag)))
	})
}
