package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
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
