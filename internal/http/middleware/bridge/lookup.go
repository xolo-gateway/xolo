package bridge

import (
	"net/http"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/common"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn"
)

// Lookup sets the existing user of the identity an authentication middleware
// put in the context, read only: it
// never creates, links nor updates an account, and lets the request through
// without a user when none matches. It suits a route that only needs to know
// who is signed in, such as the language switch, whose own write rereads the
// account: a cached user is enough to designate it.
func Lookup(userStore port.UserStore) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			authnUser := authn.ContextUser(ctx)
			tenant := httpCtx.Tenant(ctx)
			if authnUser == nil || tenant == nil {
				h.ServeHTTP(w, r)
				return
			}

			var (
				user model.User
				err  error
			)
			switch {
			case authnUser.AccountID != "":
				user, err = userStore.GetUserByID(ctx, model.UserID(authnUser.AccountID))
				if err == nil && user.TenantID() != tenant.ID() {
					user, err = nil, errors.WithStack(port.ErrNotFound)
				}
			case authnUser.Provider != "" && authnUser.Subject != "":
				user, err = userStore.GetUserByIdentity(ctx, tenant.ID(), authnUser.Provider, authnUser.Subject)
			}
			if err != nil && !errors.Is(err, port.ErrNotFound) {
				common.HandleError(w, r, err)
				return
			}
			if user != nil {
				r = r.WithContext(httpCtx.SetUser(ctx, user))
			}
			h.ServeHTTP(w, r)
		})
	}
}
