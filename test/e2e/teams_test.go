//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	gormadapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
)

// Values pinned by cmd/seed for the teams scenarios.
const (
	teamAcmeAnalysts    = "team-acme-analysts"
	membershipCarolAcme = "mbr-carol-acme"
	modelAnalysts       = "acme/analystes/gpt-4o-mini"
)

// listModels returns the model ids GET /api/v1/models lists for token.
func listModels(t *testing.T, token string) []string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, env.baseURL+"/api/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
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
	ids := make([]string, 0, len(body.Data))
	for _, m := range body.Data {
		ids = append(ids, m.ID)
	}
	return ids
}

func contains(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// A member of a team names the team in the model name: "<org>/<team>/<model>".
// The name travels through an OpenAI client like any other, is listed by
// /v1/models, and the request is charged to the team.
func TestTeams_QualifiedModelName(t *testing.T) {
	ids := listModels(t, tokenCarol)
	if !contains(ids, modelAnalysts) {
		t.Fatalf("the team model is not listed under its qualified name: %v", ids)
	}
	if !contains(ids, "acme/gpt-4o-mini") {
		t.Fatalf("the organization-wide access keeps the short name: %v", ids)
	}

	res := chat(t, tokenCarol, modelAnalysts, "Bonjour l'équipe")
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Status, res.Body)
	}

	db, err := openDB(env.dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	var record gormadapter.UsageRecord
	if err := db.Where("team_id = ?", teamAcmeAnalysts).Order("created_at DESC").First(&record).Error; err != nil {
		t.Fatalf("no usage record charged to the team: %v", err)
	}
	if record.ProxyModelName != "acme/gpt-4o-mini" {
		t.Fatalf("the usage record keeps the organization-level name, got %q", record.ProxyModelName)
	}
}

// A member outside the team can not charge it.
func TestTeams_NotAMember(t *testing.T) {
	res := chat(t, tokenAlice, modelAnalysts, "Bonjour")
	if res.Status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", res.Status, res.Body)
	}
}
