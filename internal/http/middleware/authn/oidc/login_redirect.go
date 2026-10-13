package oidc

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bornholm/go-x/slogx"
)

// nextURLAttr holds the local path the callback redirects to after sign-in.
const nextURLAttr = "nextURL"

// autoSignInCookie marks that LoginRedirect skipped the provider choice. It
// lives apart from the session, which a failed callback clears through the
// logout route, so that a sign-in that keeps failing does not loop between the
// application and the identity provider: while it is set, the login page is
// shown instead.
const autoSignInCookie = "xolo_auto_sign_in"

const autoSignInCooldown = 30 * time.Second

// signedOutCookie marks a browser that signed out. Signing out of Xolo does
// not end the session at the identity provider: skipping the provider choice
// would sign the browser straight back in. It lasts as long as the browser
// session and is cleared by the next successful sign-in.
const signedOutCookie = "xolo_signed_out"

// LoginRedirect sends an unauthenticated browser to sign in. A GET request
// that is not an HTMX fragment remembers its path, so the callback returns to
// the requested page. With a single provider, the provider choice is skipped
// at most once per autoSignInCooldown, and never after a sign-out: a browser
// that still has a session at the identity provider is then signed in without
// any interaction.
func (h *Handler) LoginRedirect(w http.ResponseWriter, r *http.Request) {
	target := "/auth/oidc/login"

	sess, err := h.getSession(r)
	if err != nil || sess == nil {
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}

	previous, hadNext := sess.Values[nextURLAttr]
	delete(sess.Values, nextURLAttr)
	if r.Method == http.MethodGet && r.Header.Get("HX-Request") == "" {
		if next, ok := localPath(r.URL.RequestURI()); ok && next != "/" {
			sess.Values[nextURLAttr] = next
		}
	}

	// An unauthenticated hit that stores nothing leaves the session alone.
	if current, hasNext := sess.Values[nextURLAttr]; hasNext != hadNext || current != previous {
		if err := sess.Save(r, w); err != nil {
			slog.ErrorContext(r.Context(), "could not save sign-in redirect", slogx.Error(err))
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
	}

	if len(h.providers) == 1 && !hasCookie(r, autoSignInCookie) && !hasCookie(r, signedOutCookie) {
		h.setFlagCookie(w, r, autoSignInCookie, int(autoSignInCooldown.Seconds()))
		target = fmt.Sprintf("/auth/oidc/providers/%s", h.providers[0].ID)
	}

	http.Redirect(w, r, target, http.StatusSeeOther)
}

func hasCookie(r *http.Request, name string) bool {
	_, err := r.Cookie(name)
	return !errors.Is(err, http.ErrNoCookie)
}

// setFlagCookie sets a cookie that only carries a flag, with the Secure
// attribute of the session cookie. A zero maxAge lasts as long as the browser
// session, a negative one deletes the cookie.
func (h *Handler) setFlagCookie(w http.ResponseWriter, r *http.Request, name string, maxAge int) {
	secure := false
	if sess, err := h.getSession(r); err == nil && sess != nil && sess.Options != nil {
		secure = sess.Options.Secure
	}
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "1",
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// localPath accepts only an absolute path on the current origin: no scheme,
// no host, and no leading "//" or "/\" a browser would read as another host.
func localPath(value string) (string, bool) {
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.HasPrefix(value, `/\`) {
		return "", false
	}
	if strings.ContainsAny(value, "\r\n") {
		return "", false
	}
	return value, true
}
