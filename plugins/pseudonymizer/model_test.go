package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeRelease serves files as a release would and swaps modelFiles for their
// sizes and digests. It returns the number of requests it served.
func fakeRelease(t *testing.T, files map[string]string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	saved := modelFiles
	t.Cleanup(func() { modelFiles = saved })
	modelFiles = nil
	for name, content := range files {
		sum := sha256.Sum256([]byte(content))
		modelFiles = append(modelFiles, modelFile{name, int64(len(content)), hex.EncodeToString(sum[:])})
	}
	var served atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
		content, ok := files[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(content))
	}))
	t.Cleanup(srv.Close)
	return srv, &served
}

func TestModelSource_FetchesAndChecksTheRelease(t *testing.T) {
	srv, served := fakeRelease(t, map[string]string{"config.json": "{}", "model.safetensors": "weights"})
	cfg := defaultConfig()
	cfg.CacheDir = t.TempDir()
	cfg.ModelURL = srv.URL
	src, err := modelSourceFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if src.cached() {
		t.Fatalf("an empty cache reads as cached")
	}
	if err := src.fetch(context.Background()); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !src.cached() {
		t.Fatalf("the fetched model is not cached")
	}
	if b, _ := os.ReadFile(filepath.Join(src.path(), "model.safetensors")); string(b) != "weights" {
		t.Errorf("model.safetensors = %q", b)
	}
	if err := src.fetch(context.Background()); err != nil || served.Load() != 2 {
		t.Errorf("a cached model was downloaded again: %v, %d requests", err, served.Load())
	}
}

// A file altered on the way, or by a mirror, never reaches the cache.
func TestModelSource_RejectsAnAlteredFile(t *testing.T) {
	srv, _ := fakeRelease(t, map[string]string{"model.safetensors": "weights"})
	modelFiles[0].sha256 = strings.Repeat("0", 64)
	cfg := defaultConfig()
	cfg.CacheDir = t.TempDir()
	cfg.ModelURL = srv.URL
	src, _ := modelSourceFromConfig(cfg)

	err := src.fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("fetch = %v, want a digest mismatch", err)
	}
	entries, _ := os.ReadDir(src.path())
	if len(entries) != 0 {
		t.Errorf("the altered download was left in the cache: %v", entries)
	}
}

func TestModelSource_Offline(t *testing.T) {
	srv, served := fakeRelease(t, map[string]string{"model.safetensors": "weights"})
	cfg := defaultConfig()
	cfg.CacheDir = t.TempDir()
	cfg.ModelURL = srv.URL
	cfg.Offline = true
	src, _ := modelSourceFromConfig(cfg)

	if err := src.fetch(context.Background()); !errors.Is(err, errModelMissing) {
		t.Fatalf("fetch = %v, want errModelMissing", err)
	}
	if served.Load() != 0 {
		t.Errorf("offline mode downloaded %d files", served.Load())
	}
}

// A directory given by the operator is used as is: nothing is downloaded nor
// checked, offline or not.
func TestModelSource_LocalDirectory(t *testing.T) {
	cfg := defaultConfig()
	cfg.ModelDir = t.TempDir()
	cfg.Offline = true
	src, err := modelSourceFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if src.path() != cfg.ModelDir || !src.cached() {
		t.Fatalf("path = %q, cached = %v; want the directory, cached", src.path(), src.cached())
	}
	if err := src.fetch(context.Background()); err != nil {
		t.Errorf("fetch = %v, want nothing to do", err)
	}
}

// A failed load is not kept: the next request tries again, and finds the
// model once it can be downloaded.
func TestModels_RetriesAFailedLoad(t *testing.T) {
	cfg := defaultConfig()
	cfg.CacheDir = t.TempDir()
	cfg.Offline = true
	src, _ := modelSourceFromConfig(cfg)

	var m models
	if _, _, err := m.get(context.Background(), src, defaultBias); !errors.Is(err, errModelMissing) {
		t.Fatalf("get = %v, want errModelMissing", err)
	}
	m.mu.Lock()
	left := len(m.loading)
	m.mu.Unlock()
	if left != 0 {
		t.Errorf("the failed load was kept: %d entries", left)
	}
}

// A cached file of the right size but other bytes is downloaded again: a
// size check alone would fail every load and never repair the cache.
func TestModelSource_RepairsAnAlteredCachedFile(t *testing.T) {
	srv, served := fakeRelease(t, map[string]string{"model.safetensors": "weights"})
	cfg := defaultConfig()
	cfg.CacheDir = t.TempDir()
	cfg.ModelURL = srv.URL
	src, _ := modelSourceFromConfig(cfg)
	if err := os.MkdirAll(src.path(), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(src.path(), "model.safetensors")
	if err := os.WriteFile(path, []byte("WEIGHTS"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := src.fetch(context.Background()); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "weights" || served.Load() != 1 {
		t.Errorf("model.safetensors = %q after %d downloads, want it downloaded again", b, served.Load())
	}
}
