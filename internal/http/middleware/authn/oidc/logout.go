package oidc

import (
	"log/slog"
	"mime"
	"net/http"

	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn/oidctoken"
	"github.com/xolo-gateway/xolo/internal/metrics"
)

// maxLogoutRequestSize bounds the body of a back-channel logout request.
const maxLogoutRequestSize = 16 << 10

// handleBackchannelLogout implements OIDC Back-Channel Logout 1.0 by subject:
// a verified logout token revokes every session of its issuer and subject, in
// every tenant, and refuses the sign-ins started before it. A token already
// processed changes nothing and is acknowledged, so the provider may retry.
func (h *Handler) handleBackchannelLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	provider, ok := h.backchannelProvider(r.PathValue("provider"))
	if !ok || h.sessions == nil {
		http.NotFound(w, r)
		return
	}

	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/x-www-form-urlencoded" || r.URL.RawQuery != "" {
		h.refuseLogout(w, r, errors.New("malformed logout request"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxLogoutRequestSize)
	if err := r.ParseForm(); err != nil || len(r.PostForm) != 1 || len(r.PostForm["logout_token"]) != 1 {
		h.refuseLogout(w, r, errors.New("malformed logout request"))
		return
	}

	claims, err := oidctoken.VerifyLogoutToken(r.Context(), r.PostForm.Get("logout_token"), provider.Issuer, provider.ClientID, provider.JWKSURL)
	if errors.Is(err, oidctoken.ErrKeysUnavailable) {
		// The token could not be checked, not refused: a 4xx would make the
		// provider drop the logout, and the sessions would outlive it.
		h.deferLogout(w, r, provider, "could not retrieve oidc provider keys", err)
		return
	}
	if err != nil {
		h.refuseLogout(w, r, err)
		return
	}

	revoked, err := h.sessions.RevokeIdentitySessions(r.Context(), provider.Issuer, claims.Subject, claims.ID, claims.IssuedAt.Time, claims.ExpiresAt.Time)
	if errors.Is(err, port.ErrInvalid) {
		// Retrying would never succeed: only an unavailable dependency answers
		// 503.
		h.refuseLogout(w, r, err)
		return
	}
	if err != nil {
		h.deferLogout(w, r, provider, "could not revoke oidc sessions", err)
		return
	}
	if !revoked {
		metrics.OIDCBackchannelLogouts.WithLabelValues(metrics.OIDCLogoutReplayed).Inc()
		slog.WarnContext(r.Context(), "ignoring replayed logout token", slog.String("provider", provider.ID))
	} else {
		metrics.OIDCBackchannelLogouts.WithLabelValues(metrics.OIDCLogoutRevoked).Inc()
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) refuseLogout(w http.ResponseWriter, r *http.Request, err error) {
	metrics.OIDCBackchannelLogouts.WithLabelValues(metrics.OIDCLogoutInvalid).Inc()
	slog.WarnContext(r.Context(), "refusing oidc logout request", slogx.Error(err))
	http.Error(w, "invalid_request", http.StatusBadRequest)
}

// deferLogout answers 503 so that the provider retries the logout later.
func (h *Handler) deferLogout(w http.ResponseWriter, r *http.Request, provider ProviderWithJWKS, msg string, err error) {
	metrics.OIDCBackchannelLogouts.WithLabelValues(metrics.OIDCLogoutUnavailable).Inc()
	slog.ErrorContext(r.Context(), msg, slog.String("provider", provider.ID), slogx.Error(err))
	http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
}

// backchannelProvider returns the provider able to verify a logout token: it
// proves its issuer, and has a client ID and a key set.
func (h *Handler) backchannelProvider(id string) (ProviderWithJWKS, bool) {
	for _, p := range h.providersWithJWKS {
		if p.ID == id {
			return p, p.ProvesIssuer && p.Issuer != "" && p.ClientID != "" && p.JWKSURL != ""
		}
	}
	return ProviderWithJWKS{}, false
}
