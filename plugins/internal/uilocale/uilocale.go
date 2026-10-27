// Package uilocale renders a plugin UI in the language of the console page
// that embeds it.
//
// The host reverse-proxies plugin UI requests with the browser's Cookie and
// Accept-Language headers, so a plugin resolves the language exactly as the
// host does: the locale cookie, then Accept-Language, then French. The host's
// configured default language is not known to the plugin.
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
	return locale.Middleware(bundle, i18n.Unlocalized)(h)
}
