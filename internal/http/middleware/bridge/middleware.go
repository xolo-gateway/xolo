package bridge

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/common"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authz"
)

// Options configures how the bridge turns an authenticated identity into a
// Xolo user.
type Options struct {
	// ActiveByDefault is the initial state of an account created here. An
	// inactive account exists but is refused by the authorization middleware
	// until an administrator activates it.
	ActiveByDefault bool

	// AutoCreateUsers allows an identity unknown to Xolo to get an account on
	// its first successful authentication. When false, only pre-provisioned
	// identities can sign in — DefaultAdmins excepted, so a fresh instance can
	// still be bootstrapped.
	AutoCreateUsers bool

	// DefaultAdmins lists the e-mail addresses that are granted the platform
	// admin role on sign-in.
	DefaultAdmins []string
}

func Middleware(userStore port.UserStore, emitter port.EventEmitter, opts Options) func(http.Handler) http.Handler {
	emitLoginFailed := func(ctx context.Context, authnUser *authn.User, reason string) {
		if emitter == nil || authnUser == nil {
			return
		}
		emitter.Emit(ctx, model.NewEvent(model.EventSourcePlatform, model.EventTypeAuthLoginFailed,
			model.WithEventSeverity(model.SeverityWarning),
			model.WithEventMessage("Échec de connexion: "+reason),
			model.WithEventAttribute("email", authnUser.Email),
			model.WithEventAttribute("provider", authnUser.Provider),
			model.WithEventAttribute("reason", reason),
		))
	}

	return func(h http.Handler) http.Handler {
		var fn http.HandlerFunc = func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			authnUser := authn.ContextUser(ctx)
			if authnUser == nil {
				common.HandleError(w, r, common.NewHTTPError(http.StatusUnauthorized))
				return
			}

			// The tenant middleware runs before this one and answers 404 when it
			// resolves none, so a request reaching here always carries one.
			tenant := httpCtx.Tenant(ctx)
			if tenant == nil {
				common.HandleError(w, r, common.NewHTTPError(http.StatusNotFound))
				return
			}

			isDefaultAdmin := slices.Contains(opts.DefaultAdmins, authnUser.Email)

			user, err := userStore.GetUserByIdentity(ctx, tenant.ID(), authnUser.Provider, authnUser.Subject)
			if err != nil {
				if !errors.Is(err, port.ErrNotFound) {
					common.HandleError(w, r, err)
					return
				}

				// The identity authenticated successfully but Xolo knows
				// nothing about it. Default admins are the exception: they are
				// the only way to bootstrap an instance that has no user yet.
				if !opts.AutoCreateUsers && !isDefaultAdmin {
					emitLoginFailed(ctx, authnUser, "aucun compte ne correspond à cette identité et la création automatique est désactivée")
					common.HandleError(w, r, common.NewError(
						"user account auto-creation is disabled",
						"Aucun compte Xolo n'est associé à cette identité. Contactez un administrateur pour qu'il vous crée un accès.",
						http.StatusForbidden,
					))
					return
				}

				user = model.NewUser(
					tenant.ID(),
					authnUser.Provider, authnUser.Subject, authnUser.Email, authnUser.DisplayName,
					opts.ActiveByDefault || isDefaultAdmin,
					authz.RoleUser,
				)

				if err := userStore.SaveUser(ctx, user); err != nil {
					if errors.Is(err, port.ErrAlreadyExists) {
						emitLoginFailed(ctx, authnUser, "un compte existe déjà avec cette adresse email")
						common.HandleError(w, r, common.NewError(
							err.Error(),
							"Un compte existe déjà avec cette adresse email. Contactez un administrateur pour faire fusionner vos comptes.",
							http.StatusConflict,
						))
						return
					}

					common.HandleError(w, r, err)
					return
				}
			}

			missingRole := len(user.Roles()) == 0
			shouldBeAdmin := isDefaultAdmin && !slices.Contains(user.Roles(), authz.RoleAdmin)

			// Never overwrite a stored value with an empty incoming one: some
			// authenticators (e.g. OAuth2 introspection) resolve an identity
			// without an email or display name.
			changed := (authnUser.DisplayName != "" && user.DisplayName() != authnUser.DisplayName) ||
				(authnUser.Email != "" && user.Email() != authnUser.Email)

			if changed || shouldBeAdmin || missingRole {
				updatable := model.CopyUser(user)
				if authnUser.DisplayName != "" {
					updatable.SetDisplayName(authnUser.DisplayName)
				}
				if authnUser.Email != "" {
					updatable.SetEmail(authnUser.Email)
				}

				if missingRole {
					updatable.SetRoles(authz.RoleUser)
				}

				if shouldBeAdmin {
					newRoles := append(user.Roles(), authz.RoleAdmin)
					updatable.SetRoles(newRoles...)
					updatable.SetActive(true)
				}

				if err := userStore.SaveUser(ctx, updatable); err != nil {
					if errors.Is(err, port.ErrAlreadyExists) {
						common.HandleError(w, r, common.NewError(
							err.Error(),
							"Un compte existe déjà avec cette adresse email. Contactez un administrateur pour faire fusionner vos comptes.",
							http.StatusConflict,
						))
						return
					}

					common.HandleError(w, r, err)
					return
				}

				user = updatable
			}

			ctx = httpCtx.SetUser(ctx, user)
			r = r.WithContext(ctx)

			h.ServeHTTP(w, r)
		}

		return fn
	}
}
