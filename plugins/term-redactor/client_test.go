package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPTermFetcher_DecodesTermsAndSendsAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"uuid":"u1","name":"Jean Dupont","category":"client"},
			{"uuid":"u2","name":"Résidence du Parc"}
		]`))
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.APIURL = srv.URL
	cfg.APIAuthHeaderName = "Authorization"

	f := newHTTPTermFetcher()
	terms, err := f.FetchTerms(context.Background(), cfg, "Bearer s3cr3t")
	if err != nil {
		t.Fatalf("FetchTerms: %v", err)
	}

	if gotAuth != "Bearer s3cr3t" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer s3cr3t")
	}
	if len(terms) != 2 {
		t.Fatalf("terms = %v, want 2 entries", terms)
	}
	if terms[0].UUID != "u1" || terms[0].Name != "Jean Dupont" || terms[0].Category != "client" {
		t.Errorf("terms[0] = %+v, unexpected", terms[0])
	}
	if terms[1].UUID != "u2" || terms[1].Name != "Résidence du Parc" || terms[1].Category != "" {
		t.Errorf("terms[1] = %+v, unexpected", terms[1])
	}
}

func TestHTTPTermFetcher_NoAuthValue_NoHeaderSent(t *testing.T) {
	headerSeen := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			headerSeen = true
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.APIURL = srv.URL

	f := newHTTPTermFetcher()
	if _, err := f.FetchTerms(context.Background(), cfg, ""); err != nil {
		t.Fatalf("FetchTerms: %v", err)
	}
	if headerSeen {
		t.Error("expected no Authorization header when authValue is empty")
	}
}

func TestHTTPTermFetcher_NonOKStatus_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.APIURL = srv.URL

	f := newHTTPTermFetcher()
	if _, err := f.FetchTerms(context.Background(), cfg, ""); err == nil {
		t.Fatal("expected an error on a non-200 response")
	}
}

func TestHTTPTermFetcher_MissingAPIURL_ReturnsError(t *testing.T) {
	f := newHTTPTermFetcher()
	if _, err := f.FetchTerms(context.Background(), defaultConfig(), ""); err == nil {
		t.Fatal("expected an error when api_url is not configured")
	}
}

func TestHTTPTermFetcher_TimesOutOnSlowServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.APIURL = srv.URL
	cfg.HTTPTimeoutSeconds = 0 // forces the minimum: passed through as < 1s below

	f := newHTTPTermFetcher()
	// Override via a context that's already tighter than the server's delay,
	// since HTTPTimeoutSeconds only accepts whole seconds.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := f.FetchTerms(ctx, cfg, "")
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") && !strings.Contains(err.Error(), "Client.Timeout") {
		t.Errorf("expected a timeout-flavoured error, got: %v", err)
	}
}
