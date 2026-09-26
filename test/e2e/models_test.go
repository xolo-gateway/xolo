//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestApplicationToken_ListsOrgModels is the regression test for
// https://github.com/xolo-gateway/xolo/issues/48.
//
// Before the fix, an application's shadow user had no org membership, so
// GET /api/v1/models returned 200 {"data": []}. The application token
// happily proxied against the same models — only the catalogue was empty.
//
// This test exercises the full chain that #48 lived in:
//
//   - token authenticator (`internal/http/middleware/authn/token/login.go`)
//     stamps OrgID on authn.User for application tokens;
//   - bridge middleware (`internal/http/middleware/bridge/middleware.go`)
//     creates the application's shadow user;
//   - memberships middleware installs the application-aware resolver;
//   - handleModels (`internal/http/handler/api/models.go`) scopes by
//     authn.User.OrgID.
//
// A regression that drops OrgID from getUserFromToken, or that skips the
// authn-OrgID fast path, fails this test even though it might still pass
// hand-rolled unit tests that build the request context directly.
func TestApplicationToken_ListsOrgModels(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, env.baseURL+"/api/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tokenAppAcmeCI)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /api/v1/models: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(body.Data) == 0 {
		t.Fatalf("regression of #48: GET /api/v1/models with application token returned an empty catalogue")
	}

	// The application's org is acme; the catalog should list at least one
	// acme-scoped model. We do not pin a specific name because the seed
	// evolves, but the org prefix must match.
	foundAcme := false
	for _, m := range body.Data {
		if strings.HasPrefix(m.ID, "acme/") {
			foundAcme = true
			break
		}
	}
	if !foundAcme {
		t.Fatalf("expected at least one 'acme/...' model in the catalogue, got %v", body.Data)
	}
}
