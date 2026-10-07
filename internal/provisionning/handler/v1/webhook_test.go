package v1_test

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/adapter/webhook"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/service"
	v1 "github.com/xolo-gateway/xolo/internal/provisionning/handler/v1"
)

const testAESKey = "1111111111111111111111111111111111111111111111111111111111111111"

func testWebhookSecret(b string) string {
	return "whsec_" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat(b, 32)))
}

type receivedWebhook struct {
	id, timestamp, signature string
	body                     []byte
}

// newWebhookEnv serves webhooks delivered to a TLS receiver that records
// every request and answers 204.
func newWebhookEnv(t *testing.T) (*testEnv, *service.WebhookService, *httptest.Server, func() []receivedWebhook) {
	t.Helper()
	var mu sync.Mutex
	var received []receivedWebhook
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		received = append(received, receivedWebhook{r.Header.Get("webhook-id"), r.Header.Get("webhook-timestamp"), r.Header.Get("webhook-signature"), body})
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(receiver.Close)
	roots := x509.NewCertPool()
	roots.AddCert(receiver.Certificate())
	sender, err := webhook.NewSender([]string{receiver.URL}, true, roots)
	require.NoError(t, err)
	t.Cleanup(sender.Close)

	env := newEnv(t)
	// The workers run concurrently: they must share the single connection of
	// the in-memory database.
	sqlDB, err := env.db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	webhooks := service.NewWebhookService(env.store, sender, testAESKey, 2, model.WebhookCapacity{Queue: 100, Subscription: 100}, 20*time.Millisecond)
	provisioning := service.NewProvisioningService(env.store, env.store, env.store, env.store, service.WithProvisioningTransaction(env.store), service.WithProvisioningReader(env.store))
	env.handler = v1.NewHandler(provisioning, testVersion, v1.WithWebhooks(webhooks))
	return env, webhooks, receiver, func() []receivedWebhook {
		mu.Lock()
		defer mu.Unlock()
		return append([]receivedWebhook(nil), received...)
	}
}

func renameTenant(t *testing.T, env *testEnv, name string) {
	t.Helper()
	tenant, err := env.store.GetTenantByID(t.Context(), model.TenantID(env.tenantID))
	require.NoError(t, err)
	require.NoError(t, env.store.SaveTenant(t.Context(), model.UpdateTenant(tenant, model.WithTenantName(name))))
}

func TestWebhookManifest(t *testing.T) {
	env, _, _, _ := newWebhookEnv(t)
	rec := call(t, env.handler, http.MethodGet, "/v1/manifest", nil)
	assertStatus(t, rec, http.StatusOK)
	require.Contains(t, decodeBody(t, rec)["capabilities"], "webhooks")

	disabled := newEnv(t)
	rec = call(t, disabled.handler, http.MethodGet, "/v1/manifest", nil)
	require.NotContains(t, decodeBody(t, rec)["capabilities"], "webhooks")
	assertStatus(t, call(t, disabled.handler, http.MethodGet, disabled.xoloBase+"/webhooks", nil), http.StatusNotFound)
}

func TestWebhookSubscriptionLifecycle(t *testing.T) {
	env, webhooks, receiver, received := newWebhookEnv(t)
	secret, next := testWebhookSecret("a"), testWebhookSecret("b")
	path := env.xoloBase + "/webhooks/" + uuid.NewString()
	input := map[string]any{"destination": receiver.URL + "/in", "events": []string{"tenant.updated.v1"}, "enabled": true, "secrets": []string{secret, next}}

	put := call(t, env.handler, http.MethodPut, path, input)
	assertStatus(t, put, http.StatusOK)
	require.NotContains(t, put.Body.String(), secret)
	require.Equal(t, float64(2), decodeBody(t, put)["secretCount"])
	var row xologorm.WebhookSubscription
	require.NoError(t, env.db.First(&row).Error)
	require.NotEmpty(t, row.EncryptedSecrets)
	require.NotContains(t, row.EncryptedSecrets, strings.TrimPrefix(secret, "whsec_"))
	list := call(t, env.handler, http.MethodGet, env.xoloBase+"/webhooks", nil)
	assertStatus(t, list, http.StatusOK)
	require.Len(t, decodeBody(t, list)["items"], 1)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- webhooks.Run(ctx) }()
	cursor := captureCursor(t, env.handler)
	renameTenant(t, env, "Webhook change")
	require.Eventually(t, func() bool { return len(received()) == 1 }, 5*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool {
		var delivered int64
		env.db.Model(&xologorm.WebhookDelivery{}).Where("state = ?", model.WebhookDelivered).Count(&delivered)
		return delivered == 1
	}, 5*time.Second, 20*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("the worker did not stop")
	}

	// The delivery is the event of the feed, signed with both secrets.
	got := received()[0]
	var event model.CommonEvent
	require.NoError(t, json.Unmarshal(got.body, &event))
	require.Equal(t, "tenant.updated.v1", event.Type)
	require.Equal(t, event.ID, got.id)
	feed := readEvents(t, env.handler, cursor)
	require.Equal(t, feed[len(feed)-1].ID, got.id)
	signature, err := webhook.Sign(got.id, got.timestamp, got.body, []string{secret, next})
	require.NoError(t, err)
	require.Equal(t, signature, got.signature)

	deliveries := call(t, env.handler, http.MethodGet, path+"/deliveries", nil)
	assertStatus(t, deliveries, http.StatusOK)
	require.Contains(t, deliveries.Body.String(), `"diagnostic":"accepted"`)
	require.NotContains(t, deliveries.Body.String(), "Webhook change")
	require.NotContains(t, deliveries.Body.String(), secret)

	// Omitted secrets are kept; a rotation ends with the new secret alone.
	delete(input, "secrets")
	assertStatus(t, call(t, env.handler, http.MethodPut, path, input), http.StatusOK)
	require.Equal(t, float64(2), decodeBody(t, call(t, env.handler, http.MethodGet, path, nil))["secretCount"])
	input["secrets"] = []string{next}
	assertStatus(t, call(t, env.handler, http.MethodPut, path, input), http.StatusOK)
	require.Equal(t, float64(1), decodeBody(t, call(t, env.handler, http.MethodGet, path, nil))["secretCount"])

	assertStatus(t, call(t, env.handler, http.MethodPost, path+"/reset", map[string]any{"acknowledgeLoss": false}), http.StatusBadRequest)
	assertStatus(t, call(t, env.handler, http.MethodPost, path+"/reset", map[string]any{"acknowledgeLoss": true}), http.StatusNoContent)
	assertStatus(t, call(t, env.handler, http.MethodDelete, path, nil), http.StatusNoContent)
	rec := call(t, env.handler, http.MethodGet, path, nil)
	assertStatus(t, rec, http.StatusNotFound)
	assertErrorCode(t, rec, "not_found")
}

func TestWebhookRequestValidation(t *testing.T) {
	env, _, receiver, _ := newWebhookEnv(t)
	secret := testWebhookSecret("a")
	path := env.xoloBase + "/webhooks/" + uuid.NewString()
	valid := func() map[string]any {
		return map[string]any{"destination": receiver.URL + "/in", "events": []string{"*"}, "enabled": true, "secrets": []string{secret}}
	}
	for name, mutate := range map[string]func(map[string]any){
		"destination not allowlisted": func(in map[string]any) { in["destination"] = "https://elsewhere.example.test/in" },
		"plain http":                  func(in map[string]any) { in["destination"] = strings.Replace(receiver.URL, "https", "http", 1) },
		"unknown event":               func(in map[string]any) { in["events"] = []string{"tenant.renamed.v1"} },
		"wildcard with others":        func(in map[string]any) { in["events"] = []string{"*", "tenant.updated.v1"} },
		"duplicate event":             func(in map[string]any) { in["events"] = []string{"tenant.updated.v1", "tenant.updated.v1"} },
		"no events":                   func(in map[string]any) { in["events"] = []string{} },
		"enabled missing":             func(in map[string]any) { delete(in, "enabled") },
		"no secrets on creation":      func(in map[string]any) { delete(in, "secrets") },
		"short secret": func(in map[string]any) {
			in["secrets"] = []string{"whsec_" + base64.StdEncoding.EncodeToString([]byte("short"))}
		},
		"same secret twice": func(in map[string]any) { in["secrets"] = []string{secret, secret} },
		"three secrets": func(in map[string]any) {
			in["secrets"] = []string{secret, testWebhookSecret("b"), testWebhookSecret("c")}
		},
	} {
		input := valid()
		mutate(input)
		rec := call(t, env.handler, http.MethodPut, path, input)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: got %d, want 422 (%s)", name, rec.Code, rec.Body.String())
		}
	}
	input := valid()
	input["unknown"] = true
	assertStatus(t, call(t, env.handler, http.MethodPut, path, input), http.StatusBadRequest)
	assertStatus(t, call(t, env.handler, http.MethodPut, env.xoloBase+"/webhooks/NOT-A-UUID", valid()), http.StatusBadRequest)
	assertStatus(t, call(t, env.handler, http.MethodGet, path+"?limit=1", nil), http.StatusBadRequest)

	rec := call(t, env.handler, http.MethodGet, "/v1/xolo/tenants/"+uuid.NewString()+"/webhooks", nil)
	assertStatus(t, rec, http.StatusNotFound)
	assertErrorCode(t, rec, "parent_not_found")

	// A subscription is invisible from another tenant.
	assertStatus(t, call(t, env.handler, http.MethodPut, path, valid()), http.StatusOK)
	foreign := model.NewTenant("foreign", "Foreign", "")
	require.NoError(t, env.store.CreateTenant(t.Context(), foreign))
	foreignPath := "/v1/xolo/tenants/" + string(foreign.ID()) + "/webhooks/" + strings.TrimPrefix(path, env.xoloBase+"/webhooks/")
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		rec := call(t, env.handler, method, foreignPath, nil)
		assertStatus(t, rec, http.StatusNotFound)
		assertErrorCode(t, rec, "not_found")
	}
	rec = call(t, env.handler, http.MethodPut, foreignPath, valid())
	assertStatus(t, rec, http.StatusConflict)
	assertErrorCode(t, rec, "conflict")

	for range model.WebhookMaxSubscriptionsPerTenant - 1 {
		assertStatus(t, call(t, env.handler, http.MethodPut, env.xoloBase+"/webhooks/"+uuid.NewString(), valid()), http.StatusOK)
	}
	rec = call(t, env.handler, http.MethodPut, env.xoloBase+"/webhooks/"+uuid.NewString(), valid())
	assertStatus(t, rec, http.StatusConflict)
	assertErrorCode(t, rec, "webhook_capacity")
}

// TestWebhookLossDoesNotAffectFeedConsumers pins that webhooks only notify:
// a consumer that missed every delivery still converges from the feed.
func TestWebhookLossDoesNotAffectFeedConsumers(t *testing.T) {
	env, _, _, _ := newWebhookEnv(t)
	consumer := &reconstructionConsumer{t: t, handler: env.handler}
	consumer.rebuild()
	_, err := env.store.PutWebhook(t.Context(), model.TenantID(env.tenantID), model.WebhookID(uuid.NewString()), model.WebhookSettings{Destination: "https://offline.example.test/in", Events: []string{"*"}, Enabled: true, EncryptedSecrets: "unused", SecretCount: 1})
	require.NoError(t, err)
	renameTenant(t, env, "While offline")
	require.NoError(t, env.store.PrepareWebhooks(t.Context(), model.WebhookCapacity{Queue: 10, Subscription: 10}, 100))
	require.NoError(t, env.db.Where("1 = 1").Delete(&xologorm.WebhookDelivery{}).Error)

	if status := consumer.poll(false); status != http.StatusOK {
		t.Fatalf("poll: %d", status)
	}
	path := resourcePath(model.FamilyTenant, model.CommonKey{TenantID: env.tenantID})
	require.Contains(t, consumer.rows[path], "While offline")
}

func captureCursor(t *testing.T, handler http.Handler) string {
	t.Helper()
	rec := call(t, handler, http.MethodGet, "/v1/events/cursor", nil)
	assertStatus(t, rec, http.StatusOK)
	return decodeBody(t, rec)["cursor"].(string)
}

func readEvents(t *testing.T, handler http.Handler, cursor string) []model.CommonEvent {
	t.Helper()
	rec := call(t, handler, http.MethodGet, "/v1/events?cursor="+url.QueryEscape(cursor)+"&limit=1000", nil)
	assertStatus(t, rec, http.StatusOK)
	var page model.CommonEventPage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	return page.Items
}
