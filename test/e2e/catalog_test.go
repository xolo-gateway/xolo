//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// TestCatalogLookup_IsRoutedByTheServer calls the model form's pre-fill
// through the assembled server: a handler test calls the handler directly
// and cannot see a route the composition root forgot.
func TestCatalogLookup_IsRoutedByTheServer(t *testing.T) {
	// The server reads the catalog in the background at startup: the lookup
	// answers 503 until it has.
	client := &http.Client{Timeout: 30 * time.Second}
	var resp *http.Response
	for deadline := time.Now().Add(30 * time.Second); ; {
		req, err := http.NewRequest(http.MethodGet, env.baseURL+"/api/catalog/lookup?id=glm-5.3&provider=openai", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+tokenAppAcmeCI)
		resp, err = client.Do(req)
		if err != nil {
			t.Fatalf("GET /api/catalog/lookup: %v", err)
		}
		if resp.StatusCode != http.StatusServiceUnavailable || time.Now().After(deadline) {
			break
		}
		resp.Body.Close()
		time.Sleep(500 * time.Millisecond)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Version       string `json:"version"`
		ContextWindow int64  `json:"context_window"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("the answer is not the pre-fill JSON: %v", err)
	}
	if body.Version == "" || body.ContextWindow == 0 {
		t.Errorf("pre-fill = %+v, want the catalog's entry for glm-5.3", body)
	}
}
