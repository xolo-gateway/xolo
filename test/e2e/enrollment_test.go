//go:build e2e

package e2e

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	gormadapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// TestEnrollmentByBearerToken follows a chat front end presenting the ID token
// of each person: an identity Xolo has never seen is an active member by its
// first request, and calls a model, while one no rule matches gets nothing.
func TestEnrollmentByBearerToken(t *testing.T) {
	dir := t.TempDir()
	idp := newFakeIdP(t)

	dsn := filepath.Join(dir, "enrollment.sqlite")
	source, err := openDB(env.dsn)
	require.NoError(t, err)
	require.NoError(t, source.Exec("VACUUM INTO ?", dsn).Error)
	sourceDB, err := source.DB()
	require.NoError(t, err)
	require.NoError(t, sourceDB.Close())

	db, err := openDB(dsn)
	require.NoError(t, err)
	var org gormadapter.Organization
	require.NoError(t, db.First(&org, "slug = ?", "acme").Error)
	require.NoError(t, db.Create(&gormadapter.EnrollmentRule{
		ID: string(model.NewEnrollmentRuleID()), OrgID: org.ID, Name: "Staff", Kind: string(model.EnrollmentKindClaim),
		Claim: "groups", Value: "staff", Role: model.RoleMember, Enabled: true,
	}).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	port, err := freePort()
	require.NoError(t, err)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	prefix := "XOLO_HTTP_AUTHN_OIDC_PROVIDER_" + strings.ToUpper(fakeIdPProvider) + "_"
	config := append(serverEnv(env.pluginsDir, port, baseURL, dsn),
		"XOLO_HTTP_AUTHN_OIDC_PROVIDERS="+fakeIdPProvider,
		prefix+"KEY="+fakeIdPClientID,
		prefix+"SECRET=e2e-secret",
		prefix+"DISCOVERY_URL="+idp.issuer()+"/.well-known/openid-configuration",
		prefix+"LABEL=Fake IdP",
		// Accounts stay inactive unless a rule activates them.
		"XOLO_HTTP_AUTHN_ACTIVE_BY_DEFAULT=false",
	)
	logPath := filepath.Join(dir, "server.log")
	stop, err := launchServer(env.serverBin, logPath, config)
	require.NoError(t, err)
	t.Cleanup(stop)
	require.NoError(t, waitReady(baseURL, 90*time.Second))
	t.Cleanup(func() {
		if t.Failed() {
			if logs, err := os.ReadFile(logPath); err == nil {
				t.Logf("---- enrollment server log ----\n%s", logs)
			}
		}
	})

	token := func(subject string, groups ...string) string {
		now := time.Now()
		return idp.sign(t, jwt.MapClaims{
			"iss": idp.issuer(), "aud": fakeIdPClientID, "sub": subject, "groups": groups,
			"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
			"email": subject + "@example.test", "email_verified": true,
		})
	}
	call := func(method, path, bearer, body string) (int, string) {
		req, err := http.NewRequest(method, baseURL+path, strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		content, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, string(content)
	}

	staff := token("enrolled-subject", "staff", "ops")
	status, body := call(http.MethodGet, "/api/v1/models", staff, "")
	require.Equal(t, http.StatusOK, status, body)
	require.Contains(t, body, modelFast)

	status, body = call(http.MethodPost, "/api/v1/chat/completions", staff,
		`{"model":"`+modelFast+`","messages":[{"role":"user","content":"ping"}]}`)
	require.Equal(t, http.StatusOK, status, body)
	require.Contains(t, body, "Bien reçu")

	status, body = call(http.MethodGet, "/api/v1/models", token("unmatched-subject", "guests"), "")
	require.NotContains(t, body, modelFast, "status %d", status)
}
