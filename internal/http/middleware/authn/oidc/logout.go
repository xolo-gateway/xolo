package oidc

import (
	"errors"
	"mime"
	"net/http"

	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn/oidctoken"
)

func (h *Handler) handleBackchannelLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.sessions == nil {
		http.NotFound(w, r)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/x-www-form-urlencoded" {
		http.Error(w, "invalid logout request", 400)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if r.URL.RawQuery != "" || r.ParseForm() != nil || len(r.PostForm) != 1 || len(r.PostForm["logout_token"]) != 1 {
		http.Error(w, "invalid logout request", 400)
		return
	}
	for _, p := range h.providersWithJWKS {
		if p.ID != r.PathValue("provider") || p.ClientID == "" || p.JWKSURL == "" {
			continue
		}
		claims, err := oidctoken.VerifyLogoutToken(r.Context(), r.PostForm.Get("logout_token"), p.Issuer, p.ClientID, p.JWKSURL)
		if err != nil {
			http.Error(w, "invalid logout token", 400)
			return
		}
		err = h.sessions.RevokeIdentitySessions(r.Context(), p.Issuer, claims.Subject, claims.ID, claims.IssuedAt.Time)
		if errors.Is(err, port.ErrAlreadyExists) {
			http.Error(w, "logout replay", 400)
			return
		}
		if err != nil {
			http.Error(w, "logout unavailable", 503)
			return
		}
		w.WriteHeader(200)
		return
	}
	http.NotFound(w, r)
}
