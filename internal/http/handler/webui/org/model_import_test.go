package org

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/crypto"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
)

const importTestSecretKey = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

// openAIModelList serves an OpenAI-compatible GET /v1/models, checking the
// provider's key.
func openAIModelList(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-go" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

// importFixture is an organization with one openai provider whose API is
// served by baseURL, plus the models already declared.
func importFixture(t *testing.T, baseURL, apiKey string, declared ...[2]string) (*Handler, *modelFormStore, model.Tenant) {
	t.Helper()
	ciphertext, err := crypto.Encrypt(importTestSecretKey, apiKey)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	tenant := model.NewTenant("a", "A", "")
	org := model.NewOrganization(tenant.ID(), "acme", "Acme", "")
	p := model.NewProvider(org.ID(), "OpenCode Go", "openai", baseURL, ciphertext, "USD")
	s := &modelFormStore{org: org, provider: p}
	for _, d := range declared {
		s.created = append(s.created, model.NewLLMModel(p.ID(), org.ID(), d[0], d[1], "", 0, 0))
	}
	return &Handler{orgStore: s, providerStore: s, secretKey: importTestSecretKey}, s, tenant
}

func importRequest(t *testing.T, s *modelFormStore, tenant model.Tenant, method string, fields url.Values) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, "/", strings.NewReader(fields.Encode()))
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	ctx := httpCtx.SetBaseURL(httpCtx.SetTenant(req.Context(), tenant), "http://xolo.test")
	ctx = httpCtx.SetUser(ctx, model.NewUser(tenant.ID(), "test", "admin", "admin@example.test", "Admin", true, model.PlatformRoleAdmin))
	req = req.WithContext(ctx)
	req.SetPathValue("orgSlug", s.org.Slug())
	req.SetPathValue("providerID", string(s.provider.ID()))
	return req
}

const goModels = `{"object":"list","data":[
	{"id":"qwen3.8-max","object":"model","created":0,"owned_by":"qwen"},
	{"id":"glm-5.3","object":"model","created":0,"owned_by":"zai"}
]}`

func TestImportModelsPage_ListsTheProviderModels(t *testing.T) {
	upstream := openAIModelList(t, goModels)
	h, s, tenant := importFixture(t, upstream.URL+"/v1", "sk-go", [2]string{"qwen", "qwen3.8-max"})

	rr := httptest.NewRecorder()
	h.getImportModelsPage(rr, importRequest(t, s, tenant, http.MethodGet, nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `value="glm-5.3"`) {
		t.Errorf("glm-5.3 must be selectable")
	}
	if strings.Contains(body, `value="qwen3.8-max"`) || !strings.Contains(body, "déjà déclaré : qwen") {
		t.Errorf("qwen3.8-max is already declared and must not be selectable")
	}
}

func TestImportModelsPage_ShowsADiscoveryFailure(t *testing.T) {
	upstream := openAIModelList(t, goModels)
	h, s, tenant := importFixture(t, upstream.URL+"/v1", "sk-wrong")

	rr := httptest.NewRecorder()
	h.getImportModelsPage(rr, importRequest(t, s, tenant, http.MethodGet, nil))

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "n&#39;a pas pu lister ses modèles") {
		t.Errorf("the page must explain the failure: %s", rr.Body.String())
	}
}

func TestImportModels_CreatesTheSelectionWithThePrefix(t *testing.T) {
	upstream := openAIModelList(t, goModels)
	h, s, tenant := importFixture(t, upstream.URL+"/v1", "sk-go")

	rr := httptest.NewRecorder()
	h.importModels(rr, importRequest(t, s, tenant, http.MethodPost, url.Values{
		"proxy_prefix": {" go-"},
		"models":       {"glm-5.3", "qwen3.8-max"},
	}))

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want a redirect: %s", rr.Code, rr.Body.String())
	}
	if len(s.created) != 2 {
		t.Fatalf("created %d models, want 2", len(s.created))
	}
	for _, m := range s.created {
		if m.ProxyName() != "go-"+m.RealModel() {
			t.Errorf("proxy name %q for %q, want the prefix", m.ProxyName(), m.RealModel())
		}
		if m.PromptCostPer1KTokens() != 0 || m.CompletionCostPer1KTokens() != 0 || m.APIType() != "" || m.BaseURL() != "" {
			t.Errorf("%s must start at zero cost on the provider's API", m.RealModel())
		}
		if m.Enabled() {
			t.Errorf("%s must start disabled until its costs are filled", m.RealModel())
		}
		if m.ContextWindow() != 0 || m.OutputWindow() != 0 {
			t.Errorf("%s: windows the provider does not report must stay empty, got ctx %d, out %d", m.RealModel(), m.ContextWindow(), m.OutputWindow())
		}
	}
}

func TestImportModels_ReportsWhatItLeftOut(t *testing.T) {
	upstream := openAIModelList(t, goModels)
	// glm-5.3 is free on this provider, but its proxy name is taken.
	h, s, tenant := importFixture(t, upstream.URL+"/v1", "sk-go", [2]string{"glm-5.3", "other-model"})

	rr := httptest.NewRecorder()
	h.importModels(rr, importRequest(t, s, tenant, http.MethodPost, url.Values{
		"models": {"glm-5.3", "qwen3.8-max", "gone-model"},
	}))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{"1 modèle importé", "2 modèles laissés de côté", "s&#39;appelle déjà « glm-5.3 »", "gone-model : le fournisseur ne le liste plus"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in the report", want)
		}
	}
	if len(s.created) != 2 || s.created[1].RealModel() != "qwen3.8-max" {
		t.Errorf("only qwen3.8-max must be created, got %d models", len(s.created))
	}
}

func TestImportModels_RequiresASelection(t *testing.T) {
	upstream := openAIModelList(t, goModels)
	h, s, tenant := importFixture(t, upstream.URL+"/v1", "sk-go")

	rr := httptest.NewRecorder()
	h.importModels(rr, importRequest(t, s, tenant, http.MethodPost, url.Values{}))

	if rr.Code != http.StatusUnprocessableEntity || len(s.created) != 0 {
		t.Fatalf("status = %d, created %d", rr.Code, len(s.created))
	}
}

// A partial import reports on the list it already holds: asking the
// provider again could fail and hide the models just created.
func TestImportModels_ReportsWithoutAskingTheProviderAgain(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls > 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(goModels))
	}))
	t.Cleanup(upstream.Close)
	h, s, tenant := importFixture(t, upstream.URL+"/v1", "sk-go", [2]string{"glm-5.3", "elsewhere"})

	rr := httptest.NewRecorder()
	h.importModels(rr, importRequest(t, s, tenant, http.MethodPost, url.Values{"models": {"glm-5.3", "qwen3.8-max"}}))

	if rr.Code != http.StatusOK || calls != 1 {
		t.Fatalf("status = %d after %d calls to the provider, want 200 after 1", rr.Code, calls)
	}
	body := rr.Body.String()
	for _, want := range []string{"1 modèle importé", "s&#39;appelle déjà « glm-5.3 »", "déjà déclaré : qwen3.8-max"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in the report", want)
		}
	}
}

// The limits a provider reports are what the import saves typing: they must
// reach the created model.
func TestImportModels_FillsTheReportedWindows(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"z-ai/glm-5.3","name":"Z.ai: GLM 5.3","context_length":1000000,
			"top_provider":{"context_length":1000000,"max_completion_tokens":131072,"is_moderated":false}}]}`))
	}))
	t.Cleanup(upstream.Close)
	h, s, tenant := importFixture(t, upstream.URL+"/api/v1", "sk-or")
	openrouter := model.NewProvider(s.org.ID(), "OpenRouter", "openrouter", upstream.URL+"/api/v1", s.provider.APIKey(), "USD")
	s.provider = openrouter

	rr := httptest.NewRecorder()
	h.importModels(rr, importRequest(t, s, tenant, http.MethodPost, url.Values{"models": {"z-ai/glm-5.3"}}))

	if rr.Code != http.StatusSeeOther || len(s.created) != 1 {
		t.Fatalf("status = %d, created %d: %s", rr.Code, len(s.created), rr.Body.String())
	}
	if m := s.created[0]; m.ContextWindow() != 1000000 || m.OutputWindow() != 131072 || m.Description() != "Z.ai: GLM 5.3" {
		t.Errorf("model = ctx %d, out %d, description %q", m.ContextWindow(), m.OutputWindow(), m.Description())
	}
}

func TestImportModels_ReportsAFailedCreation(t *testing.T) {
	upstream := openAIModelList(t, goModels)
	h, s, tenant := importFixture(t, upstream.URL+"/v1", "sk-go")
	s.createErr = errors.New("database is locked")

	rr := httptest.NewRecorder()
	h.importModels(rr, importRequest(t, s, tenant, http.MethodPost, url.Values{"models": {"glm-5.3"}}))

	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "glm-5.3 : l&#39;enregistrement a échoué") {
		t.Fatalf("status = %d, the failure must be reported: %s", rr.Code, rr.Body.String())
	}
}

// A name taken by a model of another real model must show on the page, not
// only once the import refuses it.
func TestImportModelsPage_ShowsTheProxyNameAndItsConflict(t *testing.T) {
	upstream := openAIModelList(t, goModels)
	h, s, tenant := importFixture(t, upstream.URL+"/v1", "sk-go", [2]string{"glm-5.3", "other-model"})

	rr := httptest.NewRecorder()
	h.getImportModelsPage(rr, importRequest(t, s, tenant, http.MethodGet, nil))

	body := rr.Body.String()
	if rr.Code != http.StatusOK || !strings.Contains(body, `<span class="font-mono text-xs">qwen3.8-max</span>`) {
		t.Fatalf("status = %d, the proxy name of qwen3.8-max must show: %s", rr.Code, body)
	}
	if !strings.Contains(body, "Un modèle s&#39;appelle déjà « glm-5.3 »") {
		t.Errorf("the conflict on glm-5.3 must show before the import")
	}
}

func TestPreviewImportNames_SwapsTheNamesOfTheRows(t *testing.T) {
	h, s, tenant := importFixture(t, "http://unused.test/v1", "sk-go", [2]string{"go-glm-5.3", "other-model"})

	rr := httptest.NewRecorder()
	h.previewImportNames(rr, importRequest(t, s, tenant, http.MethodPost, url.Values{
		"proxy_prefix": {"go-"},
		"candidate":    {"", "glm-5.3", "qwen3.8-max"},
	}))

	body := rr.Body.String()
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, body)
	}
	for _, want := range []string{`id="import-name-1"`, `id="import-name-2"`, `hx-swap-oob="true"`, ">go-qwen3.8-max<", "Un modèle s&#39;appelle déjà « go-glm-5.3 »"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in the preview: %s", want, body)
		}
	}
	if strings.Contains(body, `id="import-name-0"`) {
		t.Errorf("a declared row has no name to preview")
	}
}

// The form's own mistake is reported even when the provider fails too.
func TestImportModels_ValidatesTheFormFirst(t *testing.T) {
	upstream := openAIModelList(t, goModels)
	h, s, tenant := importFixture(t, upstream.URL+"/v1", "sk-wrong")

	rr := httptest.NewRecorder()
	h.importModels(rr, importRequest(t, s, tenant, http.MethodPost, url.Values{"proxy_prefix": {"team/"}, "models": {"glm-5.3"}}))

	if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), "Le préfixe ne peut pas contenir") {
		t.Fatalf("status = %d, the prefix must be refused: %s", rr.Code, rr.Body.String())
	}
}

func TestImportModels_IgnoresARepeatedModel(t *testing.T) {
	upstream := openAIModelList(t, goModels)
	h, s, tenant := importFixture(t, upstream.URL+"/v1", "sk-go")

	rr := httptest.NewRecorder()
	h.importModels(rr, importRequest(t, s, tenant, http.MethodPost, url.Values{"models": {"glm-5.3", "glm-5.3"}}))

	if rr.Code != http.StatusSeeOther || len(s.created) != 1 {
		t.Fatalf("status = %d, created %d: %s", rr.Code, len(s.created), rr.Body.String())
	}
}

func TestImportModels_RefusesAPrefixWithASlash(t *testing.T) {
	upstream := openAIModelList(t, goModels)
	h, s, tenant := importFixture(t, upstream.URL+"/v1", "sk-go")

	rr := httptest.NewRecorder()
	h.importModels(rr, importRequest(t, s, tenant, http.MethodPost, url.Values{"proxy_prefix": {"team/"}, "models": {"glm-5.3"}}))

	if rr.Code != http.StatusUnprocessableEntity || len(s.created) != 0 {
		t.Fatalf("status = %d, created %d", rr.Code, len(s.created))
	}
	// The selection survives the refusal.
	checked := regexp.MustCompile(`\schecked(\s|/|$)`)
	for _, tag := range strings.Split(rr.Body.String(), "<input")[1:] {
		tag, _, _ = strings.Cut(tag, ">")
		if !strings.Contains(tag, `name="models"`) {
			continue
		}
		if strings.Contains(tag, `value="glm-5.3"`) && !checked.MatchString(tag) {
			t.Errorf("glm-5.3 must stay checked: %s", tag)
		}
		if strings.Contains(tag, `value="qwen3.8-max"`) && checked.MatchString(tag) {
			t.Errorf("qwen3.8-max was not selected: %s", tag)
		}
	}
}
