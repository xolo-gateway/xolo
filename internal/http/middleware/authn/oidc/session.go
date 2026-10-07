package oidc

import (
	"encoding/gob"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/sessions"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn"
)

const userAttr = "u"

// authenticationStartedAttr holds, in unix nanoseconds, when the pending
// sign-in started: a revocation refuses the sign-ins started before it.
const authenticationStartedAttr = "authentication_started"

var errSessionNotFound = errors.New("session not found")

func init() {
	gob.Register(&authn.User{})
}

func (h *Handler) storeSessionUser(w http.ResponseWriter, r *http.Request, user *authn.User) error {
	sess, err := h.getSession(r)
	if err != nil {
		return errors.WithStack(err)
	}

	// Stamp the tenant the session was opened on, so it can not be replayed on
	// another one (see authn.Middleware).
	if tenant := httpCtx.Tenant(r.Context()); tenant != nil {
		user.TenantID = string(tenant.ID())
	}

	if h.sessions != nil {
		if err := h.openSession(r, sess, user); err != nil {
			return errors.WithStack(err)
		}
	}

	sess.Values[userAttr] = user

	if err := sess.Save(r, w); err != nil {
		return errors.WithStack(err)
	}

	return nil
}

// openSession registers the session of a sign-in started at most
// model.LoginMaxDuration ago, and binds the cookie to it.
func (h *Handler) openSession(r *http.Request, sess *sessions.Session, user *authn.User) error {
	started, ok := sess.Values[authenticationStartedAttr].(int64)
	delete(sess.Values, authenticationStartedAttr)
	if !ok {
		return errors.New("sign-in start missing")
	}
	startedAt := time.Unix(0, started)
	now := time.Now()
	if startedAt.Before(now.Add(-model.LoginMaxDuration)) {
		return errors.New("sign-in expired")
	}
	if user.TenantID == "" {
		return errors.New("tenant missing")
	}

	id, err := h.sessions.OpenSession(r.Context(), model.OIDCSession{
		TenantID:        model.TenantID(user.TenantID),
		Issuer:          sessionIssuer(user),
		Subject:         user.Subject,
		AuthenticatedAt: startedAt,
		ExpiresAt:       now.Add(h.sessionTTL),
	})
	if err != nil {
		return errors.WithStack(err)
	}
	user.SessionID = id
	return nil
}

// sessionIssuer is the issuer the registry keys a session on: the proven
// issuer, which a logout token can name, or else the provider pseudo-issuer.
func sessionIssuer(user *authn.User) string {
	if user.Issuer != "" {
		return user.Issuer
	}
	return model.OIDCPseudoIssuer(user.Provider)
}

func (h *Handler) retrieveSessionUser(r *http.Request) (*authn.User, error) {
	sess, err := h.getSession(r)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	user, ok := sess.Values[userAttr].(*authn.User)
	if !ok {
		return nil, errors.WithStack(errSessionNotFound)
	}

	if h.sessions != nil {
		// A cookie issued before the registry carries no session and is
		// refused like a revoked one.
		err := h.sessions.CheckSession(r.Context(), user.SessionID, model.TenantID(user.TenantID), sessionIssuer(user), user.Subject)
		if errors.Is(err, port.ErrNotFound) {
			return nil, errors.WithStack(errSessionNotFound)
		}
		if err != nil {
			return nil, errors.WithStack(err)
		}
	}

	return user, nil
}

func (h *Handler) getSession(r *http.Request) (*sessions.Session, error) {
	sess, err := h.sessionStore.Get(r, h.sessionName)
	if err != nil {
		slog.ErrorContext(r.Context(), "could not retrieve session from store", slog.Any("error", errors.WithStack(err)))
		return sess, errors.WithStack(errSessionNotFound)
	}

	return sess, nil
}

func (h *Handler) clearSession(w http.ResponseWriter, r *http.Request) error {
	sess, err := h.getSession(r)
	if err != nil && !errors.Is(err, errSessionNotFound) {
		return errors.WithStack(err)
	}

	if sess == nil {
		return nil
	}

	// The cookie may designate a session already revoked: closing it is
	// then a no-op.
	if user, ok := sess.Values[userAttr].(*authn.User); ok && h.sessions != nil && user.SessionID != "" {
		if err := h.sessions.CloseSession(r.Context(), user.SessionID); err != nil {
			return errors.WithStack(err)
		}
	}

	sess.Options.MaxAge = -1

	if err := sess.Save(r, w); err != nil {
		return errors.WithStack(err)
	}

	return nil
}
