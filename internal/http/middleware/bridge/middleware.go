package bridge

import (
	"context"
	"errors"
	"net/http"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/common"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn"
)

// Options configures transactional identity resolution. Managed members must
// exist before login; verified default administrators remain a bootstrap path.
type Options struct {
	ActiveByDefault bool
	AutoCreateUsers bool
	Managed         bool
	DefaultAdmins   []string
}

func Middleware(userStore port.UserStore, emitter port.EventEmitter, opts Options) func(http.Handler) http.Handler {
	resolve := func(ctx context.Context, tid model.TenantID, proof *authn.User) (model.User, error) {
		// An internal API token authenticates an account UUID independently of an
		// external identity declaration. It never provisions or updates a profile.
		if proof.AccountID != "" {
			user, err := userStore.GetUserByID(ctx, model.UserID(proof.AccountID))
			if err != nil {
				return nil, err
			}
			if user.TenantID() != tid {
				return nil, port.ErrNotAllowed
			}
			return user, nil
		}
		transactions, ok := userStore.(port.ProvisioningTransaction)
		if !ok {
			return nil, errors.New("bridge requires transactional identity storage")
		}
		return service.ResolveAuthenticatedIdentity(ctx, transactions, tid, service.AuthenticatedIdentity{
			Provider: proof.Provider, Issuer: proof.Issuer, Subject: proof.Subject, Email: proof.Email, DisplayName: proof.DisplayName, EmailVerified: proof.EmailVerified,
		}, service.LoginPolicy{AutoCreate: opts.AutoCreateUsers, ActiveByDefault: opts.ActiveByDefault, Managed: opts.Managed, DefaultAdmins: opts.DefaultAdmins})
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			proof := authn.ContextUser(ctx)
			if proof == nil {
				common.HandleError(w, r, common.NewHTTPError(http.StatusUnauthorized))
				return
			}
			tenant := httpCtx.Tenant(ctx)
			if tenant == nil {
				common.HandleError(w, r, common.NewHTTPError(http.StatusNotFound))
				return
			}
			user, err := resolve(ctx, tenant.ID(), proof)
			if err != nil {
				if errors.Is(err, port.ErrNotAllowed) || errors.Is(err, port.ErrAlreadyExists) || errors.Is(err, port.ErrResourceDeleted) {
					if emitter != nil {
						emitter.Emit(ctx, model.NewEvent(model.EventSourcePlatform, model.EventTypeAuthLoginFailed,
							model.WithEventSeverity(model.SeverityWarning), model.WithEventMessage("Échec de connexion : identité refusée"),
							model.WithEventAttribute("provider", proof.Provider), model.WithEventAttribute("reason", "identity_refused")))
					}
					status := http.StatusForbidden
					if errors.Is(err, port.ErrAlreadyExists) {
						status = http.StatusConflict
					}
					common.HandleError(w, r, common.NewHTTPError(status))
				} else {
					common.HandleError(w, r, err)
				}
				return
			}
			actor := model.ActorFromContext(ctx)
			actor.UserID = user.ID()
			actor.URI = ""
			ctx = model.WithActor(ctx, actor)
			next.ServeHTTP(w, r.WithContext(httpCtx.SetUser(ctx, user)))
		})
	}
}
