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

// LoginRedirect sends an unauthenticated browser to sign in. A GET request
// that is not an HTMX fragment remembers its path, so the callback returns to
// the requested page. With a single provider, the provider choice is skipped
// at most once per autoSignInCooldown: a browser that still has a session at
// the identity provider is then signed in without any interaction.
func (h *Handler) LoginRedirect(w http.ResponseWriter, r *http.Request) {
	target := "/auth/oidc/login"

	sess, err := h.getSession(r)
	if err != nil || sess == nil {
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}

	delete(sess.Values, nextURLAttr)
	if r.Method == http.MethodGet && r.Header.Get("HX-Request") == "" {
		if next, ok := localPath(r.URL.RequestURI()); ok && next != "/" {
			sess.Values[nextURLAttr] = next
		}
	}

	if err := sess.Save(r, w); err != nil {
		slog.ErrorContext(r.Context(), "could not save sign-in redirect", slogx.Error(err))
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}

	if _, err := r.Cookie(autoSignInCookie); len(h.providers) == 1 && errors.Is(err, http.ErrNoCookie) {
		http.SetCookie(w, &http.Cookie{
			Name:     autoSignInCookie,
			Value:    "1",
			Path:     "/",
			MaxAge:   int(autoSignInCooldown.Seconds()),
			HttpOnly: true,
			Secure:   sess.Options != nil && sess.Options.Secure,
			SameSite: http.SameSiteLaxMode,
		})
		target = fmt.Sprintf("/auth/oidc/providers/%s", h.providers[0].ID)
	}

	http.Redirect(w, r, target, http.StatusSeeOther)
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
