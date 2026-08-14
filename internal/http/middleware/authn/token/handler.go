package token

import (
	"net/http"

	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/gorilla/sessions"
)

type Handler struct {
	mux          *http.ServeMux
	sessionStore sessions.Store
	sessionName  string
	userStore    port.UserStore
	// orgStore resolves the tenant owning the organization an application token
	// is scoped to. A user token carries its tenant through its owner; an
	// application token only knows its organization.
	orgStore port.OrgStore
}

// ServeHTTP implements [http.Handler].
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func NewHandler(sessionStore sessions.Store, userStore port.UserStore, orgStore port.OrgStore, funcs ...OptionFunc) *Handler {
	opts := NewOptions(funcs...)
	h := &Handler{
		mux:          http.NewServeMux(),
		sessionStore: sessionStore,
		sessionName:  opts.SessionName,
		userStore:    userStore,
		orgStore:     orgStore,
	}

	h.mux.HandleFunc("GET /login", h.getLoginPage)
	h.mux.HandleFunc("POST /login", h.handleLogin)
	h.mux.HandleFunc("POST /logout", h.handleLogout)

	return h
}

var _ http.Handler = &Handler{}
