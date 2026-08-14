package token

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/slogx"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/common"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn/token/component"
	"github.com/pkg/errors"

	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
)

func (h *Handler) getLoginPage(w http.ResponseWriter, r *http.Request) {
	vmodel := component.LoginPageVModel{
		AllowedOrigins: []string{},
	}
	loginPage := component.LoginPage(vmodel)
	templ.Handler(loginPage).ServeHTTP(w, r)
}

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}

	token := r.FormValue("token")
	if token == "" {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	user, err := h.getUserFromToken(ctx, token)
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			if err := h.clearSession(w, r); err != nil {
				slog.ErrorContext(ctx, "could not clear user session", slogx.Error(err))
			}

			common.HandleError(w, r, common.NewError("forbidden", "Votre jeton d'authentification est invalide ou expiré.", http.StatusForbidden))
			return
		}

		slog.ErrorContext(ctx, "could not retrieve user from token token", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if err := h.storeSessionUser(w, r, user); err != nil {
		slog.ErrorContext(r.Context(), "could not store session user", slog.Any("error", errors.WithStack(err)))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	baseURL := httpCtx.BaseURL(ctx)

	http.Redirect(w, r, baseURL.String(), http.StatusSeeOther)
}

func (h *Handler) getUserFromToken(ctx context.Context, token string) (*authn.User, error) {
	authToken, err := h.userStore.FindAuthToken(ctx, token)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	if app := authToken.Application(); app != nil {
		if !app.Active() {
			return nil, errors.WithStack(port.ErrNotFound)
		}

		org, err := h.orgStore.GetOrgByID(ctx, authToken.OrgID())
		if err != nil {
			return nil, errors.WithStack(err)
		}

		// Provider/Subject are what permission resolution keys off to map the
		// request back to the application and its org roles — see
		// memberships.newPermissionResolver.
		return &authn.User{
			Provider:    model.ApplicationProvider,
			Subject:     string(app.ID()),
			DisplayName: app.Name(),
			OrgID:       string(authToken.OrgID()),
			TokenID:     string(authToken.ID()),
			TenantID:    string(org.TenantID()),
		}, nil
	}

	if authToken.Owner() == nil {
		return nil, errors.WithStack(port.ErrNotFound)
	}

	user, err := h.userStore.GetUserByID(ctx, authToken.Owner().ID())
	if err != nil {
		return nil, errors.WithStack(err)
	}

	// A deactivated user must not be able to authenticate, even with a token that
	// has not yet expired. Surface it as ErrNotFound so callers follow the same
	// "invalid token" path as an unknown token.
	if !user.Active() {
		return nil, errors.WithStack(port.ErrNotFound)
	}

	return &authn.User{
		Email:       user.Email(),
		Provider:    user.Provider(),
		Subject:     user.Subject(),
		DisplayName: user.DisplayName(),
		OrgID:       string(authToken.OrgID()),
		TokenID:     string(authToken.ID()),
		TenantID:    string(user.TenantID()),
	}, nil
}
