package org

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/catalog"
	"github.com/xolo-gateway/xolo/internal/core/model"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
)

// bundleStore is a modelFormStore that also creates providers, as the
// import of a new provider does.
type bundleStore struct {
	*modelFormStore
	providers []model.Provider
}

func (s *bundleStore) CreateProvider(_ context.Context, p model.Provider) error {
	s.providers = append(s.providers, p)
	return nil
}

func bundleRequest(t *testing.T, s *modelFormStore, tenant model.Tenant, providerID string, body *bytes.Buffer, contentType string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", body)
	req.Header.Set("Content-Type", contentType)
	ctx := httpCtx.SetBaseURL(httpCtx.SetTenant(req.Context(), tenant), "http://xolo.test")
	ctx = httpCtx.SetUser(ctx, model.NewUser(tenant.ID(), "test", "admin", "admin@example.test", "Admin", true, model.PlatformRoleAdmin))
	req = req.WithContext(ctx)
	req.SetPathValue("orgSlug", s.org.Slug())
	if providerID != "" {
		req.SetPathValue("providerID", providerID)
	}
	return req
}

func TestProviderBundle_ExportsThenImportsIntoANewProvider(t *testing.T) {
	h, s, tenant := importFixture(t, "https://opencode.ai/zen/go/v1", "sk-secret-go")
	glm := model.NewLLMModel(s.provider.ID(), s.org.ID(), "glm", "glm-5", "GLM 5", 600, 2200)
	glm.SetAPIType("anthropic")
	s.created = append(s.created, glm)

	// Export.
	req := importRequest(t, s, tenant, http.MethodGet, nil)
	rr := httptest.NewRecorder()
	h.exportProviderBundle(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("export status = %d: %s", rr.Code, rr.Body.String())
	}
	exported := rr.Body.Bytes()
	if bytes.Contains(exported, []byte(s.provider.APIKey())) || bytes.Contains(exported, []byte(string(s.provider.ID()))) {
		t.Fatal("the export carries the API key or the provider ID")
	}
	if !strings.Contains(rr.Header().Get("Content-Disposition"), `filename="opencode-go.provider.json"`) {
		t.Errorf("content disposition = %q", rr.Header().Get("Content-Disposition"))
	}

	// The same organization stands for the other one: its model names are
	// freed so that the import is not refused on them.
	s.created = nil
	store := &bundleStore{modelFormStore: s}
	h.providerStore = store

	// Preview: the uploaded bundle, nothing written.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("step", "preview")
	part, _ := mw.CreateFormFile("bundle_file", "opencode-go.provider.json")
	_, _ = part.Write(exported)
	_ = mw.Close()
	rr = httptest.NewRecorder()
	h.importProviderBundle(rr, bundleRequest(t, s, tenant, "", &body, mw.FormDataContentType()))
	if rr.Code != http.StatusOK {
		t.Fatalf("preview status = %d: %s", rr.Code, rr.Body.String())
	}
	if len(store.providers) != 0 || len(s.created) != 0 {
		t.Fatal("the preview wrote to the store")
	}
	if page := rr.Body.String(); !strings.Contains(page, `name="create" value="0"`) || !strings.Contains(page, `name="api_key"`) {
		t.Fatalf("the preview should offer the model and ask for the API key: %s", page)
	}

	// Apply.
	fields := url.Values{"step": {"apply"}, "bundle": {string(exported)}, "create": {"0"}, "name": {"OpenCode"}, "api_key": {"sk-other"}}
	rr = httptest.NewRecorder()
	h.importProviderBundle(rr, bundleRequest(t, s, tenant, "", bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("apply status = %d: %s", rr.Code, rr.Body.String())
	}
	if len(store.providers) != 1 || len(s.created) != 1 {
		t.Fatalf("created %d providers and %d models, want one of each", len(store.providers), len(s.created))
	}
	p, m := store.providers[0], s.created[0]
	if p.Name() != "OpenCode" || p.APIKey() == "sk-other" || p.APIKey() == "" {
		t.Errorf("provider %q with key %q, want the given name and an encrypted key", p.Name(), p.APIKey())
	}
	if m.ProviderID() != p.ID() || m.ProxyName() != "glm" || m.APIType() != "anthropic" || m.CompletionCostPer1KTokens() != 2200 {
		t.Errorf("imported model = %s/%s api %q cost %d", m.ProxyName(), m.RealModel(), m.APIType(), m.CompletionCostPer1KTokens())
	}
}

func TestProviderBundle_RefusesAFileThatIsNoBundle(t *testing.T) {
	h, s, tenant := importFixture(t, "https://example.test/v1", "sk")
	doc, _ := json.Marshal(map[string]string{"kind": "xolo.pipeline"})
	fields := url.Values{"step": {"apply"}, "bundle": {string(doc)}}
	rr := httptest.NewRecorder()
	h.importProviderBundle(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rr.Code)
	}
	if len(s.saved) != 0 || len(s.created) != 0 {
		t.Error("an invalid bundle wrote to the store")
	}
}

func TestProviderBundle_SaysWhenNothingWasApplied(t *testing.T) {
	h, s, tenant := importFixture(t, "https://example.test/v1", "sk")
	s.created = append(s.created, model.NewLLMModel(s.provider.ID(), s.org.ID(), "glm", "glm-5", "", 600, 2200))
	doc, _ := json.Marshal(model.NewProviderBundle(s.provider, s.created, false, time.Now()))

	fields := url.Values{"step": {"apply"}, "bundle": {string(doc)}}
	rr := httptest.NewRecorder()
	h.importProviderBundle(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want the page rather than a success redirect", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Rien n&#39;a été appliqué") {
		t.Error("the page should say nothing was applied")
	}
}

func TestCatalogImport_PreviewsTheCatalogProviderAsABundle(t *testing.T) {
	h, s, tenant := importFixture(t, "https://opencode.ai/inference/go/v1", "sk")
	h.SetCatalog(catalog.NewSource("", 0))

	fields := url.Values{"catalog_provider": {"opencode-go"}, "plan": {"go"}}
	rr := httptest.NewRecorder()
	h.previewCatalogImport(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	page := rr.Body.String()
	if !strings.Contains(page, `name="create"`) || !strings.Contains(page, `name="billing"`) || !strings.Contains(page, `name="bundle"`) {
		t.Error("the preview should offer the models, the plan apart, and carry the bundle to apply")
	}
	if len(s.created) != 0 {
		t.Error("the preview wrote to the store")
	}
}
