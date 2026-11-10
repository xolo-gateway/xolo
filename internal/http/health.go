package http

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/pkg/errors"
)

// Probe paths answered by every instance without authentication, for load
// balancers and orchestrators (Kubernetes liveness and readiness probes, a
// compose healthcheck). They sit outside every middleware: a probe addresses
// the instance by its own address, which is no tenant's domain, and must not be
// logged, counted or rate limited like user traffic.
const (
	LivenessPath  = "/healthz"
	ReadinessPath = "/readyz"
)

// readinessTimeout bounds a readiness check so a probe never outlives the
// orchestrator's own timeout, which defaults to one second on Kubernetes.
const readinessTimeout = time.Second

// ReadinessCheck reports whether the instance can serve traffic, typically by
// reaching its database. A nil check makes readiness equal to liveness.
type ReadinessCheck func(ctx context.Context) error

func WithReadinessCheck(check ReadinessCheck) OptionFunc {
	return func(opts *Options) {
		opts.ReadinessCheck = check
	}
}

// withHealthProbes answers the probe paths and hands everything else to next.
// Liveness only says the process serves HTTP: restarting an instance because
// its database is down would not bring the database back. Readiness runs the
// check, so an instance that lost its database leaves the load balancer's pool
// until it reaches it again.
func withHealthProbes(next http.Handler, check ReadinessCheck) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != LivenessPath && r.URL.Path != ReadinessPath {
			next.ServeHTTP(w, r)
			return
		}

		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Cache-Control", "no-store")

		if r.URL.Path == ReadinessPath && check != nil {
			ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
			defer cancel()

			if err := check(ctx); err != nil {
				slog.WarnContext(r.Context(), "readiness check failed", slog.Any("error", errors.WithStack(err)))
				http.Error(w, "not ready", http.StatusServiceUnavailable)
				return
			}
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte("ok\n"))
		}
	})
}
