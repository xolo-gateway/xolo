// Package uilocale renders a plugin UI in the language of the console page
// that embeds it.
//
// The host reverse-proxies plugin UI requests with the language it resolved
// in the X-Xolo-Locale header (cookie, then Accept-Language, then its
// configured default), and a plugin renders in it. Without the header, as
// when the UI is reached outside the host, the plugin negotiates from the
// forwarded Cookie and Accept-Language headers, then falls back to the
// host's XOLO_DEFAULT_LOCALE, which a plugin inherits from the environment
// of the server that started it.
package uilocale

import (
	"net/http"
	"os"

	"golang.org/x/text/language"

	"github.com/xolo-gateway/xolo/internal/http/middleware/locale"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

// defaultLocaleEnv names the host's default language setting.
const defaultLocaleEnv = "XOLO_DEFAULT_LOCALE"

// Handler puts the request language in the context of every request to h.
func Handler(h http.Handler) http.Handler {
	bundle, err := i18n.Load()
	if err != nil {
		panic("load embedded locales: " + err.Error())
	}
	return handler(bundle, fallback(bundle, os.Getenv(defaultLocaleEnv)), h)
}

// fallback is the host's default language, or French when it is unset or
// not supported.
func fallback(bundle *i18n.Bundle, value string) language.Tag {
	if tag, ok := bundle.Lookup(value); ok {
		return tag
	}
	return i18n.Unlocalized
}

func handler(bundle *i18n.Bundle, fallback language.Tag, h http.Handler) http.Handler {
	negotiated := locale.Middleware(bundle, fallback)(h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Either way, the response may change with the host's header.
		w.Header().Add("Vary", locale.Header)
		tag, ok := bundle.Lookup(r.Header.Get(locale.Header))
		if !ok {
			negotiated.ServeHTTP(w, r)
			return
		}
		locale.Announce(w, tag, locale.Header)
		h.ServeHTTP(w, r.WithContext(bundle.WithLanguage(r.Context(), tag)))
	})
}
