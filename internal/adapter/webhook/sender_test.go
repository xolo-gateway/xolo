package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

func testSecret(b string) string {
	return "whsec_" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat(b, 32)))
}

// Receiver fixture verifies the exact bytes and time in constant time before
// parsing JSON; only a durable deduplication decision could then cause effects.
func verifyFixture(id, stamp, signature string, body []byte, secrets []string, now time.Time) bool {
	n, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil || n < now.Unix()-300 || n > now.Unix()+300 {
		return false
	}
	matched := false
	for _, secret := range secrets {
		key, err := DecodeSecret(secret)
		if err != nil {
			return false
		}
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(id + "." + stamp + "."))
		mac.Write(body)
		for _, candidate := range strings.Fields(signature) {
			if !strings.HasPrefix(candidate, "v1,") {
				continue
			}
			raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(candidate, "v1,"))
			if err == nil && hmac.Equal(mac.Sum(nil), raw) {
				matched = true
			}
		}
	}
	return matched
}

func TestWebhookSignatures(t *testing.T) {
	now := time.Now()
	stamp := strconv.FormatInt(now.Unix(), 10)
	body := []byte(`{"id":"fixture"}`)
	id := "fixture"
	old, newKey := testSecret("a"), testSecret("b")
	sig, err := Sign(id, stamp, body, []string{old, newKey})
	require.NoError(t, err)
	require.Len(t, strings.Fields(sig), 2)
	require.True(t, verifyFixture(id, stamp, sig, body, []string{old}, now))
	require.True(t, verifyFixture(id, stamp, sig, body, []string{newKey}, now))
	require.False(t, verifyFixture(id, stamp, sig, []byte(`{"id":"fixturf"}`), []string{old}, now))
	require.False(t, verifyFixture(id, stamp, sig, body, []string{testSecret("c")}, now))
	for _, delta := range []int64{-301, 301} {
		s := strconv.FormatInt(now.Unix()+delta, 10)
		signature, err := Sign(id, s, body, []string{old})
		require.NoError(t, err)
		require.False(t, verifyFixture(id, s, signature, body, []string{old}, now))
	}
	rotated, err := Sign(id, stamp, body, []string{newKey})
	require.NoError(t, err)
	require.False(t, verifyFixture(id, stamp, rotated, body, []string{old}, now))
}

func newFixtureSender(t *testing.T, server *httptest.Server) *Sender {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	s, err := NewSender([]string{server.URL}, true, roots)
	require.NoError(t, err)
	t.Cleanup(s.Close)
	return s
}

func TestWebhookHTTPSBoundaries(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/target", 302)
		case "/large":
			w.WriteHeader(200)
			_, _ = io.WriteString(w, strings.Repeat("x", MaxResponseBytes+1))
		case "/slow":
			_, _ = io.Copy(io.Discard, r.Body)
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
			}
		case "/fail":
			w.WriteHeader(503)
		default:
			w.WriteHeader(204)
		}
	}))
	defer server.Close()
	s := newFixtureSender(t, server)
	secret := []string{testSecret("a")}
	job := &model.WebhookJob{EventID: "event", Body: `{"event":"test"}`}
	for _, tc := range []struct {
		path, diagnostic string
		success          bool
	}{
		{"/ok", "accepted", true},
		{"/redirect", "redirect", false},
		// A 2xx acknowledges the delivery, whatever the body.
		{"/large", "accepted", true},
		{"/fail", "http_status", false},
	} {
		job.Destination = server.URL + tc.path
		result := s.SendWebhook(t.Context(), job, secret)
		require.Equal(t, tc.success, result.Success)
		require.Equal(t, tc.diagnostic, result.Diagnostic)
	}
	mu.Lock()
	require.Equal(t, 4, hits)
	mu.Unlock()
	job.Destination = server.URL + "/slow"
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	require.Equal(t, "transport_error", s.SendWebhook(ctx, job, secret).Diagnostic)
	require.Less(t, time.Since(start), time.Second)
	untrusted, err := NewSender([]string{server.URL}, true, nil)
	require.NoError(t, err)
	defer untrusted.Close()
	job.Destination = server.URL
	require.Equal(t, "transport_error", untrusted.SendWebhook(t.Context(), job, secret).Diagnostic)
	blocked, err := NewSender([]string{server.URL}, false, nil)
	require.NoError(t, err)
	defer blocked.Close()
	require.Equal(t, "destination_rejected", blocked.SendWebhook(t.Context(), job, secret).Diagnostic)
	for _, raw := range []string{"http://example.test", "https://other.example/path", server.URL + "?token=secret", "https://name:secret@example.test"} {
		require.Error(t, s.ValidateDestination(raw))
	}
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "::1", "fc00::1", "169.254.169.254", "100.100.100.200", "::ffff:127.0.0.1"} {
		require.False(t, allowedAddress(netip.MustParseAddr(ip), false), ip)
	}
	// Special-use ranges stay refused even with private networks allowed.
	for _, ip := range []string{"169.254.169.254", "fe80::1", "0.0.0.0", "100.100.100.200", "192.88.99.1", "100::1", "fec0::1", "2001:db8::1", "224.0.0.1"} {
		require.False(t, allowedAddress(netip.MustParseAddr(ip), true), ip)
	}
	require.True(t, allowedAddress(netip.MustParseAddr("10.0.0.1"), true))
	require.True(t, allowedAddress(netip.MustParseAddr("93.184.215.14"), false))
}

func TestWebhookLostAcknowledgementAndRotation(t *testing.T) {
	secret := testSecret("a")
	newKey := testSecret("b")
	var mu sync.Mutex
	seen := map[string]bool{}
	effects := 0
	var bodies, stamps, signatures []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		id, stamp, sig := r.Header.Get("webhook-id"), r.Header.Get("webhook-timestamp"), r.Header.Get("webhook-signature")
		require.Equal(t, "POST", r.Method)
		require.Equal(t, "application/cloudevents+json", r.Header.Get("Content-Type"))
		require.True(t, verifyFixture(id, stamp, sig, body, []string{newKey}, time.Now()))
		var event model.CommonEvent
		require.NoError(t, json.Unmarshal(body, &event))
		require.Equal(t, event.ID, id)
		key := event.Source + " " + id
		if !seen[key] {
			seen[key] = true
			effects++
		}
		bodies = append(bodies, string(body))
		stamps = append(stamps, stamp)
		signatures = append(signatures, sig)
		if len(bodies) == 1 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	s := newFixtureSender(t, server)
	job := &model.WebhookJob{EventID: "2ed07e0a-d163-4ab4-a36f-ebda3e06e51a", Destination: server.URL, Body: `{"source":"urn:fixture","id":"2ed07e0a-d163-4ab4-a36f-ebda3e06e51a"}`}
	require.False(t, s.SendWebhook(t.Context(), job, []string{secret, newKey}).Success)
	// The timestamp has a one second resolution.
	time.Sleep(time.Second)
	require.True(t, s.SendWebhook(t.Context(), job, []string{newKey}).Success)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, effects)
	require.Equal(t, bodies[0], bodies[1])
	require.NotEqual(t, stamps[0], stamps[1])
	require.NotEqual(t, signatures[0], signatures[1])
}

func TestWebhookSenderRefusesUnsafeAllowlists(t *testing.T) {
	for _, origins := range [][]string{nil, {"http://hooks.example.test"}, {"https://hooks.example.test/path"}, {"https://*.example.test"}} {
		_, err := NewSender(origins, false, nil)
		require.Error(t, err, origins)
	}
	for _, secrets := range [][]string{nil, {testSecret("a"), testSecret("b"), testSecret("c")}, {"whsec_short"}} {
		_, err := Sign("id", "1", []byte("{}"), secrets)
		require.Error(t, err)
	}
}
