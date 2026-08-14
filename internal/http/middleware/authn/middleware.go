package authn

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/pkg/errors"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/common"
	"github.com/xolo-gateway/xolo/internal/metrics"
)

var (
	ErrSkipRequest = errors.New("skip request")
)

type Authenticator interface {
	Authenticate(w http.ResponseWriter, r *http.Request) (*User, error)
}

func Middleware(onUnauthorized func(w http.ResponseWriter, r *http.Request), authenticators ...Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		var fn http.HandlerFunc = func(w http.ResponseWriter, r *http.Request) {
			for i, authenticator := range authenticators {
				slog.Debug("authn middleware: trying", "index", i, "authenticator", fmt.Sprintf("%T", authenticator))
				user, err := authenticator.Authenticate(w, r)
				if err != nil {
					if errors.Is(err, ErrSkipRequest) {
						return
					}

					slog.ErrorContext(r.Context(), "could not authenticate user", slog.Any("error", errors.WithStack(err)))
					common.HandleError(w, r, err)
					return
				}

				if user == nil {
					continue
				}

				ctx := r.Context()

				// An identity stamped with another tenant is treated as if the
				// authenticator had found nothing: a session cookie set on a
				// parent domain, or an API token issued elsewhere, must never
				// authenticate on this tenant. Checked once here rather than in
				// every authenticator, so a new one can not forget it.
				if tenant := httpCtx.Tenant(ctx); tenant != nil && user.TenantID != "" && user.TenantID != string(tenant.ID()) {
					slog.WarnContext(ctx, "rejecting identity authenticated on another tenant",
						slog.String("identityTenantID", user.TenantID),
						slog.String("requestTenantID", string(tenant.ID())))
					continue
				}

				ctx = SetContextUser(ctx, user)

				r = r.WithContext(ctx)

				next.ServeHTTP(w, r)
				return
			}

			metrics.AuthFailures.Inc()
			onUnauthorized(w, r)
		}

		return fn
	}
}
