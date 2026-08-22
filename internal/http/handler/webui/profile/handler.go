package profile

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/slogx"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/crypto"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/profile/component"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authz"
)

type pluginManagerIface interface {
	HTTPPort(pluginName string) uint32
}

type Handler struct {
	mux             *http.ServeMux
	userStore       port.UserStore
	orgStore        port.OrgStore
	inviteStore     port.InviteStore
	personalVMStore port.PersonalVirtualModelStore
	secretStore     port.SecretStore
	pluginManager   pluginManagerIface
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func NewHandler(userStore port.UserStore, orgStore port.OrgStore, inviteStore port.InviteStore, personalVMStore port.PersonalVirtualModelStore, secretStore port.SecretStore, pluginManager pluginManagerIface) *Handler {
	h := &Handler{
		mux:             http.NewServeMux(),
		userStore:       userStore,
		orgStore:        orgStore,
		inviteStore:     inviteStore,
		personalVMStore: personalVMStore,
		secretStore:     secretStore,
		pluginManager:   pluginManager,
	}

	// Require authentication for all profile routes
	assertUser := authz.Middleware(http.HandlerFunc(h.getForbiddenPage), authz.OneOf(authz.Has(authz.RoleUser), authz.Has(authz.RoleAdmin)))

	h.mux.Handle("GET /", assertUser(http.HandlerFunc(h.getProfilePage)))
	h.mux.Handle("GET /tokens", assertUser(http.HandlerFunc(h.getTokensPage)))
	h.mux.Handle("POST /tokens", assertUser(http.HandlerFunc(h.createToken)))
	h.mux.Handle("DELETE /tokens/{tokenID}", assertUser(http.HandlerFunc(h.deleteToken)))
	h.mux.Handle("GET /invitations", assertUser(http.HandlerFunc(h.getInvitationsPage)))
	// Plugin UI proxy (personal context — no org required)
	// Both GET and POST are registered explicitly to avoid a ServeMux conflict
	// with the "GET /" catch-all pattern.
	pluginUIHandler := assertUser(http.HandlerFunc(h.servePersonalPluginUI))
	h.mux.Handle("GET /plugins/{pluginName}/ui/{uiPath...}", pluginUIHandler)
	h.mux.Handle("POST /plugins/{pluginName}/ui/{uiPath...}", pluginUIHandler)
	// Personal virtual models
	h.mux.Handle("GET /personal-models", assertUser(http.HandlerFunc(h.getPersonalModelsPage)))
	h.mux.Handle("GET /personal-models/new", assertUser(http.HandlerFunc(h.getNewPersonalModelPage)))
	h.mux.Handle("POST /personal-models", assertUser(http.HandlerFunc(h.createPersonalModel)))
	h.mux.Handle("GET /personal-models/{vmID}/edit", assertUser(http.HandlerFunc(h.getEditPersonalModelPage)))
	h.mux.Handle("POST /personal-models/{vmID}/edit", assertUser(http.HandlerFunc(h.updatePersonalModel)))
	h.mux.Handle("DELETE /personal-models/{vmID}", assertUser(http.HandlerFunc(h.deletePersonalModel)))
	h.mux.Handle("GET /personal-models/{vmID}/pipeline", assertUser(http.HandlerFunc(h.getPersonalPipelineEditorPage)))

	return h
}

func (h *Handler) getProfilePage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)

	vmodel := component.ProfilePageVModel{
		User: user,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "profile",
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: "Espace personnel", Href: "/usage"},
				{Label: "Profil", Href: ""},
			},
			Context: common.ContextPersonal,
		},
	}

	profilePage := component.ProfilePage(vmodel)
	templ.Handler(profilePage).ServeHTTP(w, r)
}

func (h *Handler) getTokensPage(w http.ResponseWriter, r *http.Request) {
	h.renderTokensPage(w, r, "")
}

// renderTokensPage renders the API keys page. createdToken carries the
// clear-text value of a key that was just created, and is the only moment it is
// ever displayed. It is passed in rather than read from the query string: a
// redirect would leak the key into access logs, browser history and Referer
// headers of every third party the page loads.
func (h *Handler) renderTokensPage(w http.ResponseWriter, r *http.Request, createdToken string) {
	ctx := r.Context()
	user := httpCtx.User(ctx)

	// Fetch user's auth tokens
	tokens, err := h.userStore.GetUserAuthTokens(ctx, user.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not fetch user auth tokens", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	// Fetch user's org memberships
	memberships, err := h.orgStore.GetUserMemberships(ctx, user.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not fetch user memberships", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	deletedToken := r.URL.Query().Get("token_deleted")

	vmodel := component.TokensPageVModel{
		AuthTokens:     tokens,
		OrgMemberships: memberships,
		CreatedToken:   createdToken,
		DeletedToken:   deletedToken != "",
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "tokens",
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: "Espace personnel", Href: "/usage"},
				{Label: "Profil", Href: "/profile/"},
				{Label: "Jetons API", Href: ""},
			},
			Context: common.ContextPersonal,
		},
	}

	templ.Handler(component.TokensPage(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) createToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	label := r.FormValue("label")
	if label == "" {
		http.Error(w, "Le nom du jeton est requis", http.StatusBadRequest)
		return
	}

	orgID := model.OrgID(r.FormValue("org_id"))
	if orgID == "" {
		http.Error(w, "L'organisation est requise", http.StatusBadRequest)
		return
	}

	var expiresAt *time.Time
	if expiresStr := r.FormValue("expires_at"); expiresStr != "" {
		t, err := time.Parse("2006-01-02", expiresStr)
		if err == nil {
			expiresAt = &t
		}
	}

	// Generate secure token
	tokenValue, err := crypto.GenerateSecureToken()
	if err != nil {
		slog.ErrorContext(ctx, "could not generate secure token", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	authToken := model.NewAuthToken(user, orgID, label, tokenValue, expiresAt)
	if err := h.userStore.CreateAuthToken(ctx, authToken); err != nil {
		slog.ErrorContext(ctx, "could not create auth token", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	h.renderTokensPage(w, r, tokenValue)
}

func (h *Handler) deleteToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	tokenID := r.PathValue("tokenID")
	if tokenID == "" {
		http.Error(w, "Token ID is required", http.StatusBadRequest)
		return
	}

	// Verify the token belongs to the current user
	tokens, err := h.userStore.GetUserAuthTokens(ctx, user.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not fetch user auth tokens", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	// Check if token belongs to user
	found := false
	for _, token := range tokens {
		if string(token.ID()) == tokenID {
			found = true
			break
		}
	}

	if !found {
		http.Error(w, "Token not found", http.StatusNotFound)
		return
	}

	// Delete the token
	if err := h.userStore.DeleteAuthToken(ctx, model.AuthTokenID(tokenID)); err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "Token not found", http.StatusNotFound)
			return
		}
		slog.ErrorContext(ctx, "could not delete auth token", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	// Redirect back to tokens page
	http.Redirect(w, r, "/profile/tokens?token_deleted=1", http.StatusSeeOther)
}

func (h *Handler) getInvitationsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)

	invites, err := h.inviteStore.ListPendingInvitesForEmail(ctx, user.Email())
	if err != nil {
		slog.ErrorContext(ctx, "could not fetch invitations", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	vmodel := component.InvitationsPageVModel{
		Invites: invites,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "profile",
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: "Espace personnel", Href: "/usage"},
				{Label: "Profil", Href: "/profile/"},
				{Label: "Invitations", Href: ""},
			},
			Context: common.ContextPersonal,
		},
	}

	templ.Handler(component.InvitationsPage(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) getForbiddenPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	vmodel := common.ErrorPageVModel{
		Message: "L'accès à cette page ne vous est pas autorisé. Veuillez contacter l'administrateur.",
		Links: []common.LinkItem{
			{
				URL:   string(common.BaseURL(ctx, common.WithPath("/auth/oidc/logout"))),
				Label: "Se déconnecter",
			},
		},
	}

	forbiddenPage := common.ErrorPage(vmodel)

	w.WriteHeader(http.StatusForbidden)
	templ.Handler(forbiddenPage).ServeHTTP(w, r)
}

var _ http.Handler = &Handler{}
