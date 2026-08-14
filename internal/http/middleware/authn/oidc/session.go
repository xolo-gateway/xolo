package oidc

import (
	"encoding/gob"
	"log/slog"
	"net/http"

	"github.com/gorilla/sessions"
	"github.com/pkg/errors"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn"
)

const userAttr = "u"

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

	sess.Options.MaxAge = -1

	if err := sess.Save(r, w); err != nil {
		return errors.WithStack(err)
	}

	return nil
}
