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
	req, err := http.NewRequest(http.MethodGet, env.baseURL+"/api/catalog/lookup?id=glm-5.3&provider=openai", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tokenAppAcmeCI)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /api/catalog/lookup: %v", err)
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
