// Package locale resolves the language of each request and stores the
// visitor's explicit choice.
//
// The language is, in this order: the closest supported language to the one
// the signed-in member chose (see Member), the one stored in the locale
// cookie by POST /locale, the best match of Accept-Language, then the
// configured default. Templates format copy with i18n.T(ctx, key) in that
// language.
//
// POST /locale always sets the cookie, so the choice holds signed out. A
// signed-in member's choice is also stored as their locale, the one member
// field Xolo writes itself while the control plane owns members.
package locale

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"golang.org/x/text/language"

	"github.com/xolo-gateway/xolo/internal/core/model"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

// CookieName holds the language the visitor chose. It is a preference, not a
// credential: it is readable by every path of the host for a year.
const CookieName = "xolo_locale"

const cookieMaxAge = 365 * 24 * 60 * 60

// resolvedFrom are the request headers Middleware resolves the language from.
var resolvedFrom = []string{"Accept-Language", "Cookie"}

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
				Announce(w, tag, resolvedFrom...)
			}
			next.ServeHTTP(w, r.WithContext(bundle.WithLanguage(r.Context(), tag)))
		})
	}
}

// Member puts in the context the supported language closest to the locale
// the signed-in member chose, ahead of the cookie and Accept-Language. A
// locale none is close to, such as pt-BR, leaves the language Middleware
// resolved. It runs once the user is resolved, inside Middleware.
func Member(bundle *i18n.Bundle) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			user := httpCtx.User(ctx)
			if user == nil || user.Preferences() == nil {
				next.ServeHTTP(w, r)
				return
			}
			tag, ok := bundle.Closest(user.Preferences().Locale())
			if !ok || tag == i18n.Language(ctx) {
				next.ServeHTTP(w, r)
				return
			}
			// The session cookie the member is resolved from is already
			// part of the announced Vary.
			if w.Header().Get("Content-Language") != "" {
				w.Header().Set("Content-Language", tag.String())
			}
			next.ServeHTTP(w, r.WithContext(bundle.WithLanguage(ctx, tag)))
		})
	}
}

// Store records the language a member chooses.
type Store interface {
	Set(ctx context.Context, tenantID model.TenantID, userID model.UserID, locale string) error
}

// Announce declares the language of a response and the request headers it
// was resolved from, so a cache keeps one copy per language.
func Announce(w http.ResponseWriter, tag language.Tag, resolvedFrom ...string) {
	w.Header().Set("Content-Language", tag.String())
	w.Header().Add("Vary", strings.Join(resolvedFrom, ", "))
}

// Handler stores the chosen language and redirects to the local page named by
// return_to. It needs no session, so the sign-in page offers the switch too,
// and refuses cross-origin posts. signedIn, when set, resolves the user of a
// signed-in request and lets a signed-out one through without: the choice
// of an active member is then stored as their locale too.
func Handler(bundle *i18n.Bundle, secure bool, store Store, signedIn func(http.Handler) http.Handler) http.Handler {
	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		tag, ok := bundle.Lookup(r.PostFormValue("locale"))
		if !ok {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		if user := httpCtx.User(r.Context()); user != nil && user.Active() && store != nil {
			if err := store.Set(r.Context(), httpCtx.TenantID(r.Context()), user.ID(), tag.String()); err != nil {
				slog.ErrorContext(r.Context(), "could not store the member locale", slog.Any("error", err))
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				return
			}
		}
		// The redirect has no body in any language. Only the announcement of
		// Middleware is withdrawn: a Vary set by another layer, such as CORS's
		// Origin, still applies.
		w.Header().Del("Content-Language")
		withdrawVary(w.Header(), strings.Join(resolvedFrom, ", "))
		http.SetCookie(w, &http.Cookie{Name: CookieName, Value: tag.String(), Path: "/", MaxAge: cookieMaxAge, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
		http.Redirect(w, r, LocalPath(r.PostFormValue("return_to")), http.StatusSeeOther)
	})
	if signedIn != nil {
		handler = signedIn(handler)
	}
	return http.NewCrossOriginProtection().Handler(handler)
}

// withdrawVary removes one value from the Vary header, keeping the others.
func withdrawVary(header http.Header, value string) {
	kept := slices.DeleteFunc(header.Values("Vary"), func(v string) bool { return v == value })
	header.Del("Vary")
	for _, v := range kept {
		header.Add("Vary", v)
	}
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
