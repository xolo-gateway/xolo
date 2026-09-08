package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// fakeUIHost implémente pluginsdk.HostClient avec un store en mémoire.
type fakeUIHost struct {
	secrets map[string]string
	emit    pluginsdk.Event
}

func newFakeUIHost() *fakeUIHost {
	return &fakeUIHost{secrets: map[string]string{}}
}

func (h *fakeUIHost) GetConfig(_ context.Context, _, _ string) (string, error) {
	return "{}", nil
}
func (h *fakeUIHost) SaveConfig(_ context.Context, _, _, _ string) error { return nil }
func (h *fakeUIHost) ListModels(_ context.Context, _ string) ([]*proto.ModelInfo, error) {
	return nil, nil
}
func (h *fakeUIHost) GetSecret(_ context.Context, _, _, nodeID, key string) (string, bool, error) {
	v, ok := h.secrets[nodeID+":"+key]
	return v, ok, nil
}
func (h *fakeUIHost) SetSecret(_ context.Context, _, _, nodeID, key, value string) error {
	h.secrets[nodeID+":"+key] = value
	return nil
}
func (h *fakeUIHost) DeleteSecret(_ context.Context, _, _, nodeID, key string) error {
	delete(h.secrets, nodeID+":"+key)
	return nil
}
func (h *fakeUIHost) EmitEvent(_ context.Context, e pluginsdk.Event) error {
	h.emit = e
	return nil
}
func (h *fakeUIHost) ChatCompletion(_ context.Context, _ *proto.HostChatCompletionRequest) (*proto.HostChatCompletionResponse, error) {
	return nil, nil
}

var _ pluginsdk.HostClient = (*fakeUIHost)(nil)

const uiHexKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// uiRequest construit une requête HTTP pré-injectée avec le host client et
// le nom du plugin dans le contexte (comme le fait ServeWithUI en production).
func uiRequest(method, target, body, orgID, nodeID string, host pluginsdk.HostClient) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	if orgID != "" {
		r.Header.Set("X-Xolo-Org-Id", orgID)
	}
	if nodeID != "" {
		r.Header.Set("X-Xolo-Node-Id", nodeID)
	}
	ctx := pluginsdk.ContextWithHostClientForTest(r.Context(), host)
	ctx = pluginsdk.ContextWithPluginNameForTest(ctx, "pseudonymizer")
	return r.WithContext(ctx)
}

func TestHandleSaveHashKey_Success(t *testing.T) {
	host := newFakeUIHost()
	handler := newUIHandler(&Plugin{})

	req := uiRequest(http.MethodPost, "/api/secrets/hash_key",
		"hash_key="+uiHexKey, "org-1", "node-1", host)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected redirect, got %d (%s)", rec.Code, rec.Body.String())
	}
	if got := host.secrets["node-1:hash_key"]; got != uiHexKey {
		t.Errorf("expected stored secret %q, got %q", uiHexKey, got)
	}
}

func TestHandleSaveHashKey_Invalid(t *testing.T) {
	host := newFakeUIHost()
	handler := newUIHandler(&Plugin{})

	req := uiRequest(http.MethodPost, "/api/secrets/hash_key",
		"hash_key=too-short", "org-1", "node-1", host)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", rec.Code, rec.Body.String())
	}
	if _, ok := host.secrets["node-1:hash_key"]; ok {
		t.Errorf("invalid key should not be stored")
	}
}

func TestPageRendersConfigFields(t *testing.T) {
	pd := uiPageData{
		BasePath:  "/",
		Config:    defaultConfig(),
		Languages: []string{"fr", "en"},
	}
	pd.Config.MinRunes = 2

	var out strings.Builder
	if err := page(pd).Render(context.Background(), &out); err != nil {
		t.Fatalf("render page: %v", err)
	}

	html := out.String()
	for _, name := range []string{
		"min_runes", "siren_contextual", "verification", "verification_strict",
		"process_attachments", "unsupported_attachments", "max_attachment_bytes", "max_attachment_chars",
	} {
		if !strings.Contains(html, `name="`+name+`"`) {
			t.Errorf("field %q missing from the rendered form", name)
		}
	}
	if !strings.Contains(html, `value="2"`) {
		t.Errorf("min_runes value not rendered")
	}
}

func TestConfigFromForm_PreservesFieldsAbsentFromTheForm(t *testing.T) {
	base := defaultConfig()
	base.SkipTypes = []string{"MISC"}
	base.Blocklist = map[string][]string{"PER": {"Monsieur"}}
	base.HashScope = "equipe-rh"
	base.VerificationOnLeak = "block"

	r := uiRequest(http.MethodPost, "/api/config",
		"language=fr&fallback_language=fr&strategy=tag&min_runes=2&siren_contextual=on&verification=on",
		"org-1", "node-1", newFakeUIHost())
	if err := r.ParseForm(); err != nil {
		t.Fatalf("ParseForm: %v", err)
	}

	cfg := configFromForm(r, base)

	if len(cfg.SkipTypes) != 1 || cfg.SkipTypes[0] != "MISC" {
		t.Errorf("SkipTypes = %v, want [MISC]", cfg.SkipTypes)
	}
	if len(cfg.Blocklist["PER"]) != 1 {
		t.Errorf("Blocklist = %v, want the base one", cfg.Blocklist)
	}
	if cfg.HashScope != "equipe-rh" {
		t.Errorf("HashScope = %q, want equipe-rh", cfg.HashScope)
	}
	if cfg.VerificationOnLeak != "block" {
		t.Errorf("VerificationOnLeak = %q, want block", cfg.VerificationOnLeak)
	}
	if cfg.MinRunes != 2 {
		t.Errorf("MinRunes = %d, want 2", cfg.MinRunes)
	}
	if !cfg.SirenContextual {
		t.Errorf("SirenContextual = false, want true")
	}
	if !cfg.Verification {
		t.Errorf("Verification = false, want true")
	}
}

func TestConfigFromForm_UncheckedBoxesAndClearedNumbers(t *testing.T) {
	base := defaultConfig()
	base.Verification = true
	base.SirenContextual = true
	base.MinRunes = 3
	base.MaxTokens = 5
	base.MinConfidence = 0.5

	// Neither an unchecked box nor an emptied number field is submitted.
	r := uiRequest(http.MethodPost, "/api/config",
		"language=fr&fallback_language=fr&strategy=tag", "org-1", "node-1", newFakeUIHost())
	if err := r.ParseForm(); err != nil {
		t.Fatalf("ParseForm: %v", err)
	}

	cfg := configFromForm(r, base)

	if cfg.Verification || cfg.SirenContextual {
		t.Errorf("unchecked boxes kept their base value: %+v", cfg)
	}
	if cfg.MinRunes != 0 || cfg.MaxTokens != 0 || cfg.MinConfidence != 0 {
		t.Errorf("cleared numbers kept their base value: min_runes=%d max_tokens=%d min_confidence=%v",
			cfg.MinRunes, cfg.MaxTokens, cfg.MinConfidence)
	}
	// Attachment limits are guardrails, not thresholds to disable: an empty
	// field falls back to the default rather than lifting the limit.
	if cfg.MaxAttachmentBytes != defaultMaxAttachmentBytes || cfg.MaxAttachmentChars != defaultMaxAttachmentChars {
		t.Errorf("cleared attachment limits did not fall back to the defaults: bytes=%d chars=%d",
			cfg.MaxAttachmentBytes, cfg.MaxAttachmentChars)
	}
	// An absent policy must land on the safe side, never on an empty value the
	// request loop would read as "do not block".
	if cfg.UnsupportedAttachments != "block" {
		t.Errorf("UnsupportedAttachments = %q, want block", cfg.UnsupportedAttachments)
	}
}

func TestHandleDeleteHashKey(t *testing.T) {
	host := newFakeUIHost()
	host.secrets["node-1:hash_key"] = uiHexKey
	handler := newUIHandler(&Plugin{})

	req := uiRequest(http.MethodPost, "/api/secrets/hash_key/delete",
		"", "org-1", "node-1", host)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected redirect, got %d (%s)", rec.Code, rec.Body.String())
	}
	if _, ok := host.secrets["node-1:hash_key"]; ok {
		t.Errorf("expected secret to be deleted")
	}
}
