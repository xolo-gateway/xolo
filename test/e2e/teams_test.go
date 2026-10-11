//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
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

// A member outside the team can not charge it. A team the caller is not in
// is answered like a team that does not exist, so that no member can list the
// teams of the organization.
func TestTeams_NotAMember(t *testing.T) {
	// Carol, a plain member, leaves the team for the time of the test.
	db, err := openDB(env.dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	left := db.Where("team_id = ? AND membership_id = ?", teamAcmeAnalysts, membershipCarolAcme).Delete(&gormadapter.TeamMember{})
	if left.Error != nil || left.RowsAffected != 1 {
		t.Fatalf("leave the team: %v, %d row(s)", left.Error, left.RowsAffected)
	}
	t.Cleanup(func() {
		defer sqlDB.Close()
		if err := joinSeededTeam(db); err != nil {
			t.Errorf("rejoin the team: %v", err)
		}
	})

	res := chat(t, tokenCarol, modelAnalysts, "Bonjour")
	if res.Status == http.StatusOK {
		t.Fatalf("status = 200, want a refusal: %s", res.Body)
	}
	unknown := chat(t, tokenCarol, "acme/no-such-team/gpt-4o-mini", "Bonjour")
	if res.Status != unknown.Status {
		t.Fatalf("status = %d, want %d as for an unknown team: %s", res.Status, unknown.Status, res.Body)
	}
	if got, want := errorKind(t, res.Body), errorKind(t, unknown.Body); got != want {
		t.Fatalf("error = %q, want %q as for an unknown team", got, want)
	}

	// The refusal is about the team: the organization-wide access still
	// reaches the model.
	if res := chat(t, tokenCarol, "acme/gpt-4o-mini", "Bonjour"); res.Status != http.StatusOK {
		t.Fatalf("short name: status = %d, want 200: %s", res.Status, res.Body)
	}
}

// errorKind returns the code and type of an OpenAI-style error body: the
// message names the model, the rest must not tell two refusals apart.
func errorKind(t *testing.T, body string) string {
	t.Helper()
	var payload struct {
		Error struct {
			Code any    `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode error body %q: %v", body, err)
	}
	return fmt.Sprintf("%v/%s", payload.Error.Code, payload.Error.Type)
}
