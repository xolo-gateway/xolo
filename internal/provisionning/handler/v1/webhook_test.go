package v1_test

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/adapter/webhook"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

func TestWebhookExtensionAndWorker(t *testing.T) {
	var mu sync.Mutex
	var received []byte
	var signature string
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		received = body
		signature = r.Header.Get("webhook-signature")
		mu.Unlock()
		w.WriteHeader(204)
	}))
	defer receiver.Close()
	roots := x509.NewCertPool()
	roots.AddCert(receiver.Certificate())
	sender, err := webhook.NewSender([]string{receiver.URL}, true, roots)
	require.NoError(t, err)
	defer sender.Close()
	h, db, base := newTestHandler(t)
	store := adapter.NewStore(db)
	svc := service.NewWebhookService(store, sender, strings.Repeat("1", 64), 2, 100, 20*time.Millisecond)
	h.WithWebhooks(svc)
	secret := "whsec_" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 32)))
	next := "whsec_" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("b", 32)))
	path := base + "/webhooks/" + string(model.NewTenantID())
	path = strings.Replace(path, "/v1/", "/v1/xolo/", 1)
	input := map[string]any{"destination": receiver.URL + "/receive", "events": []string{"*"}, "enabled": true, "secrets": []string{secret}}
	put := call(t, h, "PUT", path, input)
	assertCode(t, put, 200, "")
	require.NotContains(t, put.Body.String(), secret)
	require.NotContains(t, put.Body.String(), "EncryptedSecrets")
	var row adapter.WebhookSubscription
	require.NoError(t, db.First(&row).Error)
	require.NotContains(t, row.EncryptedSecrets, secret)
	require.NotEmpty(t, row.EncryptedSecrets)
	discovery := call(t, h, "GET", "/v1/xolo/extensions", nil)
	assertCode(t, discovery, 200, "")
	require.NotContains(t, discovery.Body.String(), secret)
	require.Contains(t, discovery.Body.String(), "webhooks")
	manifest := call(t, h, "GET", "/v1/manifest", nil)
	require.NotContains(t, manifest.Body.String(), "webhook")
	input["secrets"] = []string{secret, next}
	assertCode(t, call(t, h, "PUT", path, input), 200, "")
	get := call(t, h, "GET", path, nil)
	require.Contains(t, get.Body.String(), `"secret_count":2`)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()
	t.Cleanup(cancel)
	assertCode(t, call(t, h, "PUT", base, `{"slug":"default","name":"Webhook change","status":"active"}`), 200, "")
	require.Eventually(t, func() bool { stats, err := svc.Stats(t.Context()); return err == nil && stats.Delivered == 1 }, 3*time.Second, 20*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
	mu.Lock()
	var event model.CommonEvent
	require.NoError(t, json.Unmarshal(received, &event))
	require.Equal(t, "tenant.updated.v1", event.Type)
	require.Len(t, strings.Fields(signature), 2)
	mu.Unlock()
	diag := call(t, h, "GET", path+"/deliveries", nil)
	assertCode(t, diag, 200, "")
	require.Contains(t, diag.Body.String(), "accepted")
	require.NotContains(t, diag.Body.String(), secret)
	require.NotContains(t, diag.Body.String(), "Webhook change")
	delete(input, "secrets")
	assertCode(t, call(t, h, "PUT", path, input), 200, "")
	require.Contains(t, call(t, h, "GET", path, nil).Body.String(), `"secret_count":2`)
	input["secrets"] = []string{next}
	assertCode(t, call(t, h, "PUT", path, input), 200, "")
	require.Contains(t, call(t, h, "GET", path, nil).Body.String(), `"secret_count":1`)
	input["destination"] = "https://not-allowed.example"
	assertCode(t, call(t, h, "PUT", path, input), 400, "invalid_representation")
	foreignBase := "/v1/tenants/" + string(model.NewTenantID())
	assertCode(t, call(t, h, "PUT", foreignBase, `{"slug":"foreign-hook","name":"Other","status":"active"}`), 200, "")
	foreignPath := strings.Replace(foreignBase, "/v1/", "/v1/xolo/", 1) + "/webhooks/" + row.ID
	assertCode(t, call(t, h, "GET", foreignPath, nil), 404, "not_found")
	assertCode(t, call(t, h, "DELETE", foreignPath, nil), 404, "not_found")
	assertCode(t, call(t, h, "POST", path+"/reset", `{"acknowledge_loss":false}`), 400, "invalid_representation")
	assertCode(t, call(t, h, "POST", path+"/reset", `{"acknowledge_loss":true}`), 204, "")
	assertCode(t, call(t, h, "DELETE", path, nil), 204, "")
}
func TestWebhookLossDoesNotAdvanceConsumer(t *testing.T) {
	h, db, base := newTestHandler(t)
	consumer := newConsumer(h, filepath.Join(t.TempDir(), "state.json"))
	require.NoError(t, consumer.rebuild())
	checkpoint := consumer.state.Cursor
	store := adapter.NewStore(db)
	tid := strings.TrimPrefix(base, "/v1/tenants/")
	_, err := store.PutWebhook(t.Context(), tid, string(model.NewTenantID()), model.WebhookSettings{Destination: "https://offline.example/hooks", Events: []string{"*"}, Enabled: true, EncryptedSecrets: "unused", SecretCount: 1})
	require.NoError(t, err)
	// Simulate one hour without the console. All materialized notifications are
	// lost, while the consumer's only durable position remains the feed cursor.
	oldNow := db.Config.NowFunc
	db.Config.NowFunc = func() time.Time { return oldNow().Add(time.Hour) }
	assertCode(t, call(t, h, "PUT", base, `{"slug":"default","name":"While offline","status":"active"}`), 200, "")
	require.NoError(t, store.PrepareWebhooks(t.Context(), 100))
	require.NoError(t, db.Where("1 = 1").Delete(&adapter.WebhookDelivery{}).Error)
	db.Config.NowFunc = oldNow
	require.Equal(t, checkpoint, consumer.state.Cursor)
	// Startup/reconnection polling converges with no webhook receipt.
	resumed := newConsumer(h, consumer.path)
	require.NoError(t, resumed.resume())
	require.NoError(t, resumed.poll())
	require.Contains(t, string(resumed.state.Rows[base].Representation), "While offline")
	assertCode(t, call(t, h, "PUT", base, `{"slug":"default","name":"Periodic polling","status":"active"}`), 200, "")
	require.NoError(t, resumed.poll())
	require.Contains(t, string(resumed.state.Rows[base].Representation), "Periodic polling")
	// If retention expires while disconnected, full generation recovery is required.
	resumed.state.Cursor = checkpoint
	require.NoError(t, store.PurgeCommonEvents(t.Context(), time.Now().UTC().Add(2*time.Hour)))
	require.ErrorContains(t, resumed.poll(), "410")
	require.NoError(t, resumed.rebuild())
	require.Contains(t, string(resumed.state.Rows[base].Representation), "Periodic polling")
}
