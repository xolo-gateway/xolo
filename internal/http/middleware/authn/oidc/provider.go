package oidc

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/bornholm/go-x/slogx"
	"github.com/markbates/goth"
	"github.com/markbates/goth/gothic"
	"github.com/pkg/errors"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn"
)

func (h *Handler) handleProvider(w http.ResponseWriter, r *http.Request) {
	if _, err := gothic.CompleteUserAuth(w, r); err == nil {
		http.Redirect(w, r, "/auth/oidc/logout", http.StatusTemporaryRedirect)
	} else {
		if h.sessions != nil {
			if err := h.startAuthentication(w, r); err != nil {
				slog.ErrorContext(r.Context(), "could not start authentication", slogx.Error(err))
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				return
			}
		}
		gothic.BeginAuthHandler(w, r)
	}
}

// startAuthentication records when the sign-in starts, before the redirection
// to the identity provider.
func (h *Handler) startAuthentication(w http.ResponseWriter, r *http.Request) error {
	sess, err := h.getSession(r)
	if err != nil {
		return errors.WithStack(err)
	}
	sess.Values[authenticationStartedAttr] = time.Now().UnixNano()
	return errors.WithStack(sess.Save(r, w))
}

func (h *Handler) handleProviderCallback(w http.ResponseWriter, r *http.Request) {
	gothUser, err := gothic.CompleteUserAuth(w, r)
	if err != nil {
		slog.ErrorContext(r.Context(), "could not complete user auth", slog.Any("error", errors.WithStack(err)))
		http.Redirect(w, r, "/auth/oidc/logout", http.StatusTemporaryRedirect)
		return
	}

	// Never log gothUser: it carries the access, refresh and ID tokens.

	// A multi-tenant instance registers one goth provider per tenant host, so the
	// name goth hands back carries that host. Identities are keyed on
	// (tenant, provider, subject): strip it, or the same account would be a
	// different user on every hostname it ever signed in from.
	user := &authn.User{
		Email:       gothUser.Email,
		Provider:    BaseProviderID(gothUser.Provider),
		DisplayName: getUserDisplayName(gothUser),
	}

	rawSubject := gothUser.RawData["sub"]
	if subject, ok := rawSubject.(string); ok {
		user.Subject = subject
	}

	if user.Subject == "" {
		user.Subject = gothUser.UserID
	}

	if user.Subject == "" {
		slog.ErrorContext(r.Context(), "could not authenticate user", slog.Any("error", errors.New("user subject missing")))
		http.Redirect(w, r, "/auth/logout", http.StatusTemporaryRedirect)
		return
	}

	if user.Email == "" {
		slog.ErrorContext(r.Context(), "could not authenticate user", slog.Any("error", errors.New("user email missing")))
		http.Redirect(w, r, "/auth/oidc/logout", http.StatusTemporaryRedirect)
		return
	}

	if user.Provider == "" {
		slog.ErrorContext(r.Context(), "could not authenticate user", slog.Any("error", errors.New("user provider missing")))
		http.Redirect(w, r, "/auth/oidc/logout", http.StatusTemporaryRedirect)
		return
	}

	if err := h.proveIdentity(user, gothUser.RawData); err != nil {
		slog.ErrorContext(r.Context(), "could not authenticate user", slog.Any("error", err), slog.String("provider", user.Provider))
		http.Redirect(w, r, "/auth/oidc/logout", http.StatusTemporaryRedirect)
		return
	}

	if err := h.storeSessionUser(w, r, user); err != nil {
		slog.ErrorContext(r.Context(), "could not store session user", slog.Any("error", errors.WithStack(err)))
		http.Redirect(w, r, "/auth/oidc/logout", http.StatusTemporaryRedirect)
		return
	}

	// Honour a post-login redirect stored in the session (e.g. from the invite join flow).
	redirectTo := "/"
	if sess, err := h.getSession(r); err == nil && sess != nil {
		if next, ok := sess.Values["nextURL"].(string); ok && next != "" {
			redirectTo = next
			delete(sess.Values, "nextURL")
			_ = sess.Save(r, w)
		}
	}

	http.Redirect(w, r, redirectTo, http.StatusSeeOther)
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user, err := h.retrieveSessionUser(r)
	if err != nil && !errors.Is(err, errSessionNotFound) {
		slog.WarnContext(ctx, "could not retrieve user from session", slogx.Error(err))
	}

	if err := h.clearSession(w, r); err != nil && !errors.Is(err, errSessionNotFound) {
		slog.ErrorContext(ctx, "could not retrieve clear session", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if user == nil {
		http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
		return
	}

	baseURL := httpCtx.BaseURL(ctx)

	redirectURL := baseURL.JoinPath(fmt.Sprintf("/auth/oidc/providers/%s/logout", user.Provider))

	http.Redirect(w, r, redirectURL.String(), http.StatusTemporaryRedirect)
}

func (h *Handler) handleProviderLogout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if err := gothic.Logout(w, r); err != nil {
		slog.WarnContext(ctx, "could not logout user", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	baseURL := httpCtx.BaseURL(ctx)

	http.Redirect(w, r, baseURL.String(), http.StatusTemporaryRedirect)
}

func getUserDisplayName(user goth.User) string {
	var displayName string

	rawPreferredUsername, exists := user.RawData["preferred_username"]
	if exists {
		if preferredUsername, ok := rawPreferredUsername.(string); ok {
			displayName = preferredUsername
		}
	}

	if displayName == "" {
		displayName = user.NickName
	}

	if displayName == "" {
		displayName = user.Name
	}

	if displayName == "" {
		displayName = user.FirstName + " " + user.LastName
	}

	if displayName == "" {
		displayName = user.UserID
	}

	return displayName
}

// proveIdentity records what the provider proved about the sign-in: its
// issuer, when the provider has a genuine one, and whether it verified the
// email. The code flow reads the claims straight from the provider's token and
// userinfo endpoints over TLS (OIDC Core 3.1.3.7); an iss claim naming another
// issuer is refused.
func (h *Handler) proveIdentity(user *authn.User, raw map[string]any) error {
	if issuer, ok := h.provenIssuer(user.Provider); ok {
		if iss, present := raw["iss"].(string); present && iss != issuer {
			return errors.New("unexpected issuer")
		}
		user.Issuer = issuer
	}

	user.EmailVerified = authn.IsTrue(raw["email_verified"])
	// Google's v2 userinfo names the claim verified_email.
	if verified, present := raw["verified_email"]; present && user.Provider == "google" {
		user.EmailVerified = authn.IsTrue(verified)
	}
	return nil
}
