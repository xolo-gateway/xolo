//go:build e2e

package e2e

import (
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormadapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/adapter/webhook"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// TestWebhookDelivery starts a server with webhooks enabled on a snapshot of
// the seeded database, subscribes a TLS receiver through the provisioning
// listener and checks that a change is delivered, signed, as the event of the
// feed.
func TestWebhookDelivery(t *testing.T) {
	dir := t.TempDir()

	type delivery struct {
		id, timestamp, signature string
		body                     []byte
	}
	var mu sync.Mutex
	var received []delivery
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		received = append(received, delivery{r.Header.Get("webhook-id"), r.Header.Get("webhook-timestamp"), r.Header.Get("webhook-signature"), body})
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(receiver.Close)
	caPath := filepath.Join(dir, "receiver-ca.pem")
	require.NoError(t, os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: receiver.Certificate().Raw}), 0o600))

	dsn := filepath.Join(dir, "webhooks.sqlite")
	db, err := openDB(env.dsn)
	require.NoError(t, err)
	require.NoError(t, db.Exec("VACUUM INTO ?", dsn).Error)
	var tenant gormadapter.Tenant
	require.NoError(t, db.First(&tenant, "slug = ?", model.DefaultTenantSlug).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	port, err := freePort()
	require.NoError(t, err)
	provisioningEnv, provisioning, err := configureProvisioning(dir)
	require.NoError(t, err)
	config := append(serverEnv(env.pluginsDir, port, fmt.Sprintf("http://127.0.0.1:%d", port), dsn), provisioningEnv...)
	config = append(config,
		"XOLO_WEBHOOKS_ENABLED=true",
		"XOLO_WEBHOOKS_ALLOWED_ORIGINS="+receiver.URL,
		// The receiver listens on the loopback.
		"XOLO_WEBHOOKS_ALLOW_PRIVATE_NETWORKS=true",
		"XOLO_WEBHOOKS_TLS_CA_FILE="+caPath,
		"XOLO_WEBHOOKS_POLL_INTERVAL=100ms",
	)
	logPath := filepath.Join(dir, "server.log")
	stop, err := launchServer(env.serverBin, logPath, config)
	require.NoError(t, err)
	t.Cleanup(stop)
	t.Cleanup(func() {
		if t.Failed() {
			if logs, err := os.ReadFile(logPath); err == nil {
				t.Logf("---- webhook server log ----\n%s", logs)
			}
		}
	})

	require.Eventually(t, func() bool {
		resp, err := provisioning.client.Get(provisioning.url + "/v1/manifest")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var manifest struct {
			Capabilities []string `json:"capabilities"`
		}
		return resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&manifest) == nil && strings.Contains(strings.Join(manifest.Capabilities, " "), "webhooks")
	}, 90*time.Second, 250*time.Millisecond, "provisioning listener not ready")

	secret := "whsec_" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("e", 32)))
	hook := "/v1/xolo/tenants/" + tenant.ID + "/webhooks/" + uuid.NewString()
	status, raw := provisioning.provision(t, http.MethodPut, hook, newRequestID(), map[string]any{
		"destination": receiver.URL + "/xolo", "events": []string{"tenant.updated.v1"}, "enabled": true, "secrets": []string{secret},
	})
	require.Equal(t, http.StatusOK, status, string(raw))
	require.NotContains(t, string(raw), secret)

	status, raw = provisioning.provision(t, http.MethodGet, "/v1/events/cursor", "", nil)
	require.Equal(t, http.StatusOK, status, string(raw))
	var cursor struct {
		Cursor string `json:"cursor"`
	}
	require.NoError(t, json.Unmarshal(raw, &cursor))

	requestID := newRequestID()
	status, raw = provisioning.provision(t, http.MethodPut, "/v1/tenants/"+tenant.ID, requestID, map[string]any{"slug": model.DefaultTenantSlug, "name": "Webhook E2E", "status": "active"})
	require.Equal(t, http.StatusOK, status, string(raw))

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) > 0
	}, 30*time.Second, 100*time.Millisecond, "no webhook received")
	mu.Lock()
	got := received[0]
	mu.Unlock()

	status, raw = provisioning.provision(t, http.MethodGet, "/v1/events?cursor="+url.QueryEscape(cursor.Cursor), "", nil)
	require.Equal(t, http.StatusOK, status, string(raw))
	var page model.CommonEventPage
	require.NoError(t, json.Unmarshal(raw, &page))
	require.Len(t, page.Items, 1)
	var event model.CommonEvent
	require.NoError(t, json.Unmarshal(got.body, &event))
	require.Equal(t, page.Items[0], event, "the delivery is the event of the feed")
	require.Equal(t, event.ID, got.id)
	require.Equal(t, requestID, event.RequestID)
	signature, err := webhook.Sign(got.id, got.timestamp, got.body, []string{secret})
	require.NoError(t, err)
	require.Equal(t, signature, got.signature)

	require.Eventually(t, func() bool {
		status, raw := provisioning.provision(t, http.MethodGet, hook+"/deliveries", "", nil)
		return status == http.StatusOK && strings.Contains(string(raw), `"state":"delivered"`)
	}, 10*time.Second, 100*time.Millisecond, "the delivery is not recorded")
}
