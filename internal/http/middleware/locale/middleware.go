// Package locale resolves the language of each request and stores the
// visitor's explicit choice.
//
// The language is, in this order: the one stored in the locale cookie by
// POST /locale, the best match of Accept-Language, then the configured
// default. Templates format copy with i18n.T(ctx, key) in that language.
package locale

import (
	"net/http"
	"net/url"
	"slices"
	"strings"

	"golang.org/x/text/language"

	"github.com/xolo-gateway/xolo/internal/i18n"
)

// CookieName holds the language the visitor chose. It is a preference, not a
// credential: it is readable by every path of the host for a year.
const CookieName = "xolo_locale"

const cookieMaxAge = 365 * 24 * 60 * 60

// Header carries the language the host resolved for a request to the plugin
// UIs it reverse-proxies, so they render in it, configured default included.
const Header = "X-Xolo-Locale"

// Middleware puts the request language in the context and announces it.
// Paths under one of the unlocalized prefixes, such as assets or a JSON API,
// still get the language in their context, for their error pages, but are
// not announced as varying by it: a shared cache can keep a single copy.
func Middleware(bundle *i18n.Bundle, fallback language.Tag, unlocalized ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tag := language.Und
			if cookie, err := r.Cookie(CookieName); err == nil {
				tag, _ = bundle.Lookup(cookie.Value)
			}
			if tag == language.Und {
				tag = bundle.Match(r.Header.Get("Accept-Language"), fallback)
			}
			if !slices.ContainsFunc(unlocalized, func(prefix string) bool { return strings.HasPrefix(r.URL.Path, prefix) }) {
				w.Header().Set("Content-Language", tag.String())
				w.Header().Add("Vary", "Accept-Language, Cookie")
			}
			next.ServeHTTP(w, r.WithContext(bundle.WithLanguage(r.Context(), tag)))
		})
	}
}

// Handler stores the chosen language and redirects to the local page named by
// return_to. It needs no session, so the sign-in page offers the switch too,
// and refuses cross-origin posts.
func Handler(bundle *i18n.Bundle, secure bool) http.Handler {
	return http.NewCrossOriginProtection().Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		tag, ok := bundle.Lookup(r.PostFormValue("locale"))
		if !ok {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		// The redirect has no body in any language.
		w.Header().Del("Content-Language")
		w.Header().Del("Vary")
		http.SetCookie(w, &http.Cookie{Name: CookieName, Value: tag.String(), Path: "/", MaxAge: cookieMaxAge, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
		http.Redirect(w, r, LocalPath(r.PostFormValue("return_to")), http.StatusSeeOther)
	}))
}

// LocalPath keeps a redirect target on the current origin. Anything that is
// not an absolute path without a host becomes the home page.
func LocalPath(raw string) string {
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.ContainsAny(raw, "\\\r\n\t") {
		return "/"
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.User != nil {
		return "/"
	}
	return raw
}
