package oidc

import (
	"encoding/gob"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/sessions"
	"github.com/pkg/errors"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn"
)

const userAttr = "u"

var errSessionNotFound = errors.New("session not found")

func init() {
	gob.Register(&authn.User{})
	gob.Register(time.Time{})
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
		if user.AuthenticatedAt.IsZero() {
			return errors.New("missing authentication start")
		}
		id, err := h.sessions.OpenSession(r.Context(), sessionIssuer(user), user.Subject, user.AuthenticatedAt, time.Now().Add(24*time.Hour))
		if err != nil {
			return err
		}
		user.SessionID = id
	}
	sess.Values[userAttr] = user

	if err := sess.Save(r, w); err != nil {
		return errors.WithStack(err)
	}

	return nil
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
		if err := h.sessions.CheckSession(r.Context(), user.SessionID, sessionIssuer(user), user.Subject); err != nil {
			return nil, errSessionNotFound
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

	if user, ok := sess.Values[userAttr].(*authn.User); ok && h.sessions != nil && user.SessionID != "" {
		if err := h.sessions.CloseSession(r.Context(), user.SessionID); err != nil {
			return err
		}
	}
	sess.Options.MaxAge = -1

	if err := sess.Save(r, w); err != nil {
		return errors.WithStack(err)
	}

	return nil
}

func sessionIssuer(user *authn.User) string {
	if user.Issuer != "" {
		return user.Issuer
	}
	return "urn:xolo:provider:" + user.Provider
}
