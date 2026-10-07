package bridge

import (
	"context"
	"net/http"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/common"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn"
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
	// identities can sign in, with three exceptions: DefaultAdmins, so a fresh
	// instance can still be bootstrapped; applications, whose shadow user
	// follows the application's lifecycle; and an address named by a pending
	// targeted invitation, which stands for pre-provisioning.
	AutoCreateUsers bool

	// DefaultAdmins lists the e-mail addresses that are granted the platform
	// admin role on sign-in.
	DefaultAdmins []string

	// Transactions opens the transaction that creates, links or updates an
	// account. Defaults to the user store when it can open one.
	Transactions port.ProvisioningTransaction
}

func Middleware(userStore port.UserStore, inviteStore port.InviteStore, emitter port.EventEmitter, opts Options) func(http.Handler) http.Handler {
	transactions := opts.Transactions
	if transactions == nil {
		transactions, _ = userStore.(port.ProvisioningTransaction)
	}
	resolver := service.NewIdentityResolver(userStore, inviteStore, transactions)
	policy := service.LoginPolicy{
		AutoCreate:      opts.AutoCreateUsers,
		ActiveByDefault: opts.ActiveByDefault,
		DefaultAdmins:   opts.DefaultAdmins,
	}

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

			var (
				user model.User
				err  error
			)
			switch {
			case authnUser.AccountID != "":
				// An API token designates its owner: nothing to resolve, nothing
				// to provision.
				user, err = userStore.GetUserByID(ctx, model.UserID(authnUser.AccountID))
				if err == nil && user.TenantID() != tenant.ID() {
					err = errors.WithStack(port.ErrNotFound)
				}
				if errors.Is(err, port.ErrNotFound) {
					emitLoginFailed(ctx, authnUser, "le propriétaire du jeton est introuvable ou appartient à un autre tenant")
					common.HandleError(w, r, common.NewHTTPError(http.StatusUnauthorized))
					return
				}
			case authnUser.Provider == "" || authnUser.Subject == "":
				// Without both, the identity would designate every account no
				// sign-in is linked to.
				emitLoginFailed(ctx, authnUser, "identité sans fournisseur ou sans sujet")
				common.HandleError(w, r, common.NewHTTPError(http.StatusUnauthorized))
				return
			default:
				user, err = resolver.Resolve(ctx, tenant.ID(), service.AuthenticatedIdentity{
					Provider:      authnUser.Provider,
					Subject:       authnUser.Subject,
					Issuer:        authnUser.Issuer,
					Email:         authnUser.Email,
					EmailVerified: authnUser.EmailVerified,
					DisplayName:   authnUser.DisplayName,
				}, policy)
			}
			if err != nil {
				switch {
				case errors.Is(err, service.ErrAccountCreationDisabled):
					emitLoginFailed(ctx, authnUser, "aucun compte ne correspond à cette identité et la création automatique est désactivée")
					common.HandleError(w, r, common.NewError(
						"user account auto-creation is disabled",
						"Aucun compte Xolo n'est associé à cette identité. Contactez un administrateur pour qu'il vous crée un accès.",
						http.StatusForbidden,
					))
				case errors.Is(err, port.ErrEmailTaken):
					emitLoginFailed(ctx, authnUser, "un compte existe déjà avec cette adresse email")
					common.HandleError(w, r, common.NewError(
						"email already used",
						"Un compte existe déjà avec cette adresse email. Contactez un administrateur pour faire fusionner vos comptes.",
						http.StatusConflict,
					))
				case errors.Is(err, port.ErrAlreadyExists):
					// ErrIdentityConflict, or an identity or sign-in already
					// bound to another account: nothing to do with the email.
					emitLoginFailed(ctx, authnUser, "cette identité est déjà rattachée à un autre compte ou ne désigne aucun compte sans ambiguïté")
					common.HandleError(w, r, common.NewError(
						"identity conflict",
						"Cette identité ne peut pas être rattachée à votre compte Xolo. Contactez un administrateur.",
						http.StatusConflict,
					))
				default:
					common.HandleError(w, r, err)
				}
				return
			}

			ctx = httpCtx.SetUser(ctx, user)
			r = r.WithContext(ctx)

			h.ServeHTTP(w, r)
		}

		return fn
	}
}
