package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHealthProbes(t *testing.T) {
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})

	failing := func(context.Context) error { return errors.New("database unreachable") }
	passing := func(context.Context) error { return nil }

	cases := []struct {
		name   string
		check  ReadinessCheck
		method string
		path   string
		status int
	}{
		{"liveness ignores the check", failing, http.MethodGet, LivenessPath, http.StatusOK},
		{"readiness passes", passing, http.MethodGet, ReadinessPath, http.StatusOK},
		{"readiness fails with the check", failing, http.MethodGet, ReadinessPath, http.StatusServiceUnavailable},
		{"readiness without check", nil, http.MethodGet, ReadinessPath, http.StatusOK},
		{"head is accepted", passing, http.MethodHead, ReadinessPath, http.StatusOK},
		{"other methods are refused", passing, http.MethodPost, LivenessPath, http.StatusMethodNotAllowed},
		{"other paths reach the mux", failing, http.MethodGet, "/healthz/extra", http.StatusTeapot},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(tc.method, tc.path, nil)

			withHealthProbes(downstream, tc.check).ServeHTTP(recorder, request)

			if recorder.Code != tc.status {
				t.Fatalf("expected status %d, got %d", tc.status, recorder.Code)
			}
		})
	}
}

// A probe addresses the instance by its own address, never a tenant domain:
// it must answer before the middlewares that would reject that host.
func TestHealthProbesSkipMiddlewares(t *testing.T) {
	rejectAll := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		})
	}

	server := NewServer(WithMiddleware(rejectAll))
	handler := server.handler()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://10.0.0.1:3002"+ReadinessPath, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected the probe to bypass the middlewares, got %d", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://10.0.0.1:3002/", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected other paths to go through the middlewares, got %d", recorder.Code)
	}
}

// The check is wired from the option to the served handler, with a deadline.
func TestReadinessCheckWiring(t *testing.T) {
	var deadline time.Duration
	server := NewServer(WithReadinessCheck(func(ctx context.Context) error {
		if d, ok := ctx.Deadline(); ok {
			deadline = time.Until(d)
		}
		return errors.New("database unreachable")
	}))

	recorder := httptest.NewRecorder()
	server.handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, ReadinessPath, nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 through the server, got %d", recorder.Code)
	}
	if deadline <= 0 || deadline > readinessTimeout {
		t.Fatalf("expected a deadline within %s, got %s", readinessTimeout, deadline)
	}
}

// Probes arriving together, or within the period, share one check: an
// unauthenticated flood of /readyz must not become as many database pings.
func TestReadinessCheckIsShared(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	handler := withHealthProbes(http.NotFoundHandler(), func(context.Context) error {
		calls.Add(1)
		<-release
		return nil
	})

	var wg sync.WaitGroup
	codes := make(chan int, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, ReadinessPath, nil))
			codes <- recorder.Code
		}()
	}
	// Let the probes queue behind the first check before it returns.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	close(codes)

	for code := range codes {
		if code != http.StatusOK {
			t.Fatalf("expected 200, got %d", code)
		}
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, ReadinessPath, nil))
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected one check for every probe of the period, got %d", got)
	}
}

func TestReadinessCheckPanicIsAFailure(t *testing.T) {
	handler := withHealthProbes(http.NotFoundHandler(), func(context.Context) error {
		panic("boom")
	})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, ReadinessPath, nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 on a panicking check, got %d", recorder.Code)
	}
}
