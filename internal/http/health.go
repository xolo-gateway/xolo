package http

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/pkg/errors"
)

// Probe paths answered by every instance without authentication, for load
// balancers and orchestrators (Kubernetes liveness and readiness probes, a
// compose healthcheck). They sit outside every middleware: a probe addresses
// the instance by its own address, which is no tenant's domain, and must not be
// logged, counted or rate limited like user traffic. Not rate limited, the
// readiness check is shared instead (see sharedCheck): a flood of probes costs
// the database one check per readinessTimeout at most.
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
	ready := newSharedCheck(check, readinessTimeout)

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

		if r.URL.Path == ReadinessPath {
			if err := ready.run(r.Context()); err != nil {
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

// sharedCheck runs the readiness check at most once per period: probes
// arriving meanwhile wait for the check in flight or reuse its result.
type sharedCheck struct {
	check  ReadinessCheck
	period time.Duration

	mu      sync.Mutex
	flight  chan struct{}
	err     error
	checked time.Time
}

func newSharedCheck(check ReadinessCheck, period time.Duration) *sharedCheck {
	return &sharedCheck{check: check, period: period}
}

func (s *sharedCheck) run(ctx context.Context) error {
	if s.check == nil {
		return nil
	}

	s.mu.Lock()
	if s.flight == nil && !s.checked.IsZero() && time.Since(s.checked) < s.period {
		err := s.err
		s.mu.Unlock()
		return err
	}
	flight := s.flight
	if flight == nil {
		flight = make(chan struct{})
		s.flight = flight
		s.mu.Unlock()

		// Detached from the probe that started it: the others wait for it.
		err := s.runOnce(context.WithoutCancel(ctx))

		s.mu.Lock()
		s.err, s.checked, s.flight = err, time.Now(), nil
		s.mu.Unlock()
		close(flight)
		return err
	}
	s.mu.Unlock()

	select {
	case <-flight:
	case <-ctx.Done():
		return errors.WithStack(ctx.Err())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// runOnce bounds the check and turns a panic into a failure: the probes run
// outside the recovery middleware.
func (s *sharedCheck) runOnce(ctx context.Context) (err error) {
	ctx, cancel := context.WithTimeout(ctx, readinessTimeout)
	defer cancel()

	defer func() {
		if recovered := recover(); recovered != nil {
			err = errors.Errorf("readiness check panicked: %s", fmt.Sprint(recovered))
		}
	}()

	return s.check(ctx)
}
