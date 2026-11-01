package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// fakeUIHost implémente pluginsdk.HostClient avec un store en mémoire.
type fakeUIHost struct {
	secrets map[[4]string]string
	emit    pluginsdk.Event
}

func newFakeUIHost() *fakeUIHost {
	return &fakeUIHost{secrets: map[[4]string]string{}}
}

func (h *fakeUIHost) GetConfig(_ context.Context, _, _ string) (string, error) {
	return "{}", nil
}
func (h *fakeUIHost) SaveConfig(_ context.Context, _, _, _ string) error { return nil }
func (h *fakeUIHost) ListModels(_ context.Context, _ string) ([]*proto.ModelInfo, error) {
	return nil, nil
}
func (h *fakeUIHost) GetSecret(_ context.Context, scopeID, pluginName, nodeID, key string) (string, bool, error) {
	v, ok := h.secrets[[4]string{scopeID, pluginName, nodeID, key}]
	return v, ok, nil
}
func (h *fakeUIHost) SetSecret(_ context.Context, scopeID, pluginName, nodeID, key, value string) error {
	h.secrets[[4]string{scopeID, pluginName, nodeID, key}] = value
	return nil
}
func (h *fakeUIHost) DeleteSecret(_ context.Context, scopeID, pluginName, nodeID, key string) error {
	delete(h.secrets, [4]string{scopeID, pluginName, nodeID, key})
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
		r.Header.Set("X-Xolo-Secret-Scope-Id", orgID)
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
	if got := host.secrets[[4]string{"org-1", "pseudonymizer", "node-1", "hash_key"}]; got != uiHexKey {
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
	if _, ok := host.secrets[[4]string{"org-1", "pseudonymizer", "node-1", "hash_key"}]; ok {
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
		"engine", "bias", "model_dir", "model_url",
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
	// A bias of 0 is not "no limit" but the setting that leaks the most: an
	// emptied field goes back to the default.
	if cfg.Bias != defaultBias {
		t.Errorf("cleared bias = %v, want the default %v", cfg.Bias, defaultBias)
	}
	if cfg.Engine != engineCRF {
		t.Errorf("a form without engine gives %q, want crf", cfg.Engine)
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
	host.secrets[[4]string{"org-1", "pseudonymizer", "node-1", "hash_key"}] = uiHexKey
	handler := newUIHandler(&Plugin{})

	req := uiRequest(http.MethodPost, "/api/secrets/hash_key/delete",
		"", "org-1", "node-1", host)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected redirect, got %d (%s)", rec.Code, rec.Body.String())
	}
	if _, ok := host.secrets[[4]string{"org-1", "pseudonymizer", "node-1", "hash_key"}]; ok {
		t.Errorf("expected secret to be deleted")
	}
}

func TestConfigFromForm_EngineAndBias(t *testing.T) {
	for _, tt := range []struct {
		form   string
		engine string
		bias   float64
	}{
		{"engine=indecis&bias=3", engineIndecis, 3},
		{"engine=indecis&bias=0", engineIndecis, 0},
		{"engine=crf&bias=10", engineCRF, 10},
		{"engine=spacy&bias=11", engineCRF, defaultBias},
		{"bias=-1", engineCRF, defaultBias},
		{"bias=abc", engineCRF, defaultBias},
	} {
		r := uiRequest(http.MethodPost, "/api/config", "strategy=tag&"+tt.form, "org-1", "node-1", newFakeUIHost())
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		cfg := configFromForm(r, defaultConfig())
		if cfg.Engine != tt.engine || cfg.Bias != tt.bias {
			t.Errorf("%s: engine %q, bias %v; want %q, %v", tt.form, cfg.Engine, cfg.Bias, tt.engine, tt.bias)
		}
	}
}

// The page of the indecis engine shows its model and does not consult the
// manifest of the CRF models.
func TestPageRendersIndecisModel(t *testing.T) {
	cfg := defaultConfig()
	cfg.Engine = engineIndecis
	cfg.CacheDir = t.TempDir()
	pd := uiPageData{BasePath: "/", Config: cfg, Indecis: indecisStatusOf(context.Background(), cfg)}

	var out strings.Builder
	if err := page(pd).Render(context.Background(), &out); err != nil {
		t.Fatalf("render page: %v", err)
	}
	html := out.String()
	if !strings.Contains(html, modelName) || !strings.Contains(html, cfg.CacheDir) {
		t.Errorf("the indecis model and its path are not shown")
	}
	if !strings.Contains(html, `<option value="indecis" selected`) {
		t.Errorf("the indecis engine is not selected")
	}
}

// The page offers each type of the engine, checked unless skipped, each with
// its label: a type without one would show its catalog key.
func TestPageRendersEntityTypes(t *testing.T) {
	for _, engine := range []string{engineCRF, engineIndecis} {
		cfg := defaultConfig()
		cfg.Engine = engine
		cfg.SkipTypes = []string{"LOC"}
		pd := uiPageData{BasePath: "/", Config: cfg}

		var out strings.Builder
		if err := page(pd).Render(context.Background(), &out); err != nil {
			t.Fatalf("render page: %v", err)
		}
		html := out.String()
		for _, typ := range engineTypes(engine) {
			if !strings.Contains(html, `name="types_shown" value="`+string(typ)+`"`) {
				t.Errorf("%s: type %s not offered", engine, typ)
			}
		}
		if strings.Contains(html, "plugin.pseudonymizer.type.") {
			t.Errorf("%s: a type has no label", engine)
		}
		if !strings.Contains(html, `<input checked id="type_PHONE"`) || !strings.Contains(html, `<input id="type_LOC"`) {
			t.Errorf("%s: PHONE must be checked, LOC, skipped, unchecked", engine)
		}
	}
}

func TestConfigFromForm_EntityTypes(t *testing.T) {
	base := defaultConfig()
	base.SkipTypes = []string{"DATETIME"}
	r := uiRequest(http.MethodPost, "/api/config",
		"strategy=tag&types_shown=PER&types_shown=LOC&types_shown=EMAIL&types=PER&types=EMAIL", "org-1", "node-1", newFakeUIHost())
	if err := r.ParseForm(); err != nil {
		t.Fatalf("ParseForm: %v", err)
	}
	if got := configFromForm(r, base).SkipTypes; !slices.Equal(got, []string{"DATETIME", "LOC"}) {
		t.Errorf("SkipTypes = %v, want [DATETIME LOC]", got)
	}

	// A form without the types keeps them as they were.
	r = uiRequest(http.MethodPost, "/api/config", "strategy=tag", "org-1", "node-1", newFakeUIHost())
	if err := r.ParseForm(); err != nil {
		t.Fatalf("ParseForm: %v", err)
	}
	if got := configFromForm(r, base).SkipTypes; !slices.Equal(got, []string{"DATETIME"}) {
		t.Errorf("SkipTypes = %v, want [DATETIME]", got)
	}
}
