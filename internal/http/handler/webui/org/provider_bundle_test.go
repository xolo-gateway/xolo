package org

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/xolo-gateway/xolo/internal/core/service"
	stdhtml "html"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

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

func TestProviderBundle_AnswersInvalidSettingsWithThePage(t *testing.T) {
	h, s, tenant := importFixture(t, "https://example.test/v1", "sk")
	store := &bundleStore{modelFormStore: s}
	h.providerStore = store
	// A name too long for a provider: the bundle and its preview hold
	// nothing wrong, the provider validation every write path applies
	// refuses it.
	doc := `{"kind":"xolo.provider","version":"1","provider":{"type":"openai","currency":"USD"},"models":[]}`
	fields := url.Values{"step": {"apply"}, "bundle": {doc}, "name": {strings.Repeat("P", 1000)}, "api_key": {"k"}}
	rr := httptest.NewRecorder()
	h.importProviderBundle(rr, bundleRequest(t, s, tenant, "", bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(stdhtml.UnescapeString(rr.Body.String()), "rien n'a été écrit") || len(store.providers) != 0 {
		t.Error("the page should say nothing was written, and nothing should be")
	}
}

func TestProviderBundle_RefusesAtThePreviewAPlanThePlanEditorRefuses(t *testing.T) {
	h, s, tenant := importFixture(t, "https://example.test/v1", "sk")
	// Model budgets on a provider-wide limit: the bundle parses, the plan
	// editor refuses it.
	doc := `{"kind":"xolo.provider","version":"1","provider":{"type":"openai","currency":"USD","billing_mode":"subscription",` +
		`"subscription_plan":{"label":"P","constraints":[{"kind":"concurrency","label":"c","max_concurrent":2,"model_budgets":{"a":{}}}]}},"models":[]}`
	fields := url.Values{"step": {"preview"}, "bundle": {doc}}
	rr := httptest.NewRecorder()
	h.importProviderBundle(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(stdhtml.UnescapeString(rr.Body.String()), "n'est pas un bundle de fournisseur") {
		t.Errorf("status = %d, want the bundle refused at the preview rather than at the apply", rr.Code)
	}
}

func TestProviderBundle_RefusesAtThePreviewARetryTheProviderFormRefuses(t *testing.T) {
	h, s, tenant := importFixture(t, "https://example.test/v1", "sk")
	doc := `{"kind":"xolo.provider","version":"1","provider":{"type":"openai","currency":"USD",` +
		`"retry":{"enabled":true,"max_attempts":500,"delay":1000000000}},"models":[]}`
	fields := url.Values{"step": {"preview"}, "bundle": {doc}}
	rr := httptest.NewRecorder()
	h.importProviderBundle(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(stdhtml.UnescapeString(rr.Body.String()), "retry_config") {
		t.Errorf("status = %d, want 500 attempts refused at the preview, naming the setting", rr.Code)
	}
}

// fixedRates serves the exchange rates it holds, as "FROM>TO".
type fixedRates map[string]float64

func (f fixedRates) FetchRates(_ context.Context, base string) (map[string]float64, error) {
	rates := map[string]float64{}
	for pair, rate := range f {
		if from, to, _ := strings.Cut(pair, ">"); from == base {
			rates[to] = rate
		}
	}
	return rates, nil
}

type memoryRates struct{ rates map[string]model.ExchangeRate }

func (m *memoryRates) GetRate(_ context.Context, from, to string) (*model.ExchangeRate, error) {
	if r, ok := m.rates[from+">"+to]; ok {
		return &r, nil
	}
	return nil, errors.New("no rate")
}

func (m *memoryRates) UpsertRate(_ context.Context, r model.ExchangeRate) error {
	m.rates[r.FromCurrency+">"+r.ToCurrency] = r
	return nil
}

func (m *memoryRates) ListRates(context.Context) ([]model.ExchangeRate, error) { return nil, nil }

func TestProviderBundle_ConvertsABundleInAnotherCurrency(t *testing.T) {
	h, s, tenant := importFixture(t, "https://example.test/v1", "sk")
	h.exchangeRateService = service.NewExchangeRateService(fixedRates{"EUR>USD": 2}, &memoryRates{rates: map[string]model.ExchangeRate{}}, time.Hour)
	doc := `{"kind":"xolo.provider","version":"1","provider":{"type":"openai","currency":"EUR"},` +
		`"models":[{"real_model":"m","proxy_name":"m","enabled":true,"prices":{"input":1,"output":3}}]}`

	fields := url.Values{"step": {"apply"}, "bundle": {doc}, "create": {"0"}}
	rr := httptest.NewRecorder()
	h.importProviderBundle(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	if len(s.created) != 1 {
		t.Fatalf("created %d models, want 1", len(s.created))
	}
	if m := s.created[0]; m.PromptCostPer1KTokens() != 2000 || m.CompletionCostPer1KTokens() != 6000 {
		t.Errorf("prices = %d / %d microcents per 1K, want the EUR prices converted to USD at 2", m.PromptCostPer1KTokens(), m.CompletionCostPer1KTokens())
	}
}

func TestProviderBundle_RefusesABundleItCannotConvert(t *testing.T) {
	h, s, tenant := importFixture(t, "https://example.test/v1", "sk")
	doc := `{"kind":"xolo.provider","version":"1","provider":{"type":"openai","currency":"EUR"},"models":[]}`
	fields := url.Values{"step": {"preview"}, "bundle": {doc}}
	rr := httptest.NewRecorder()
	h.importProviderBundle(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want the import refused rather than EUR prices under USD", rr.Code)
	}
}

func TestProviderBundle_PreviewPostsBackTheConvertedBundle(t *testing.T) {
	h, s, tenant := importFixture(t, "https://example.test/v1", "sk")
	h.exchangeRateService = service.NewExchangeRateService(fixedRates{"EUR>USD": 2}, &memoryRates{rates: map[string]model.ExchangeRate{}}, time.Hour)
	doc := `{"kind":"xolo.provider","version":"1","provider":{"type":"openai","currency":"EUR","omitted_request_headers":["Api-Key"]},` +
		`"models":[{"real_model":"m","proxy_name":"m","enabled":true,"prices":{"input":1,"output":3}}]}`
	fields := url.Values{"step": {"preview"}, "bundle": {doc}}
	rr := httptest.NewRecorder()
	h.importProviderBundle(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	page := rr.Body.String()
	i := strings.Index(page, `name="bundle" value="`)
	if i < 0 {
		t.Fatal("no bundle in the page")
	}
	raw := page[i+len(`name="bundle" value="`):]
	posted, err := model.ParseProviderBundle([]byte(stdhtml.UnescapeString(raw[:strings.Index(raw, `"`)])))
	if err != nil {
		t.Fatal(err)
	}
	if posted.Provider.Currency != "USD" || posted.Models[0].Prices.Output != 6 {
		t.Errorf("posted bundle = %s %v, want the dry run's converted prices", posted.Provider.Currency, posted.Models[0].Prices)
	}
	if !strings.Contains(stdhtml.UnescapeString(page), "Api-Key") {
		t.Error("the preview should name the header to configure again")
	}
}

func TestProviderBundle_ReportsARefusedTypeChangeNextToWrittenModels(t *testing.T) {
	h, s, tenant := importFixture(t, "https://example.test/v1", "sk")
	e := model.NewLLMModel(s.provider.ID(), s.org.ID(), "e", "embed-1", "", 10, 0)
	e.SetKind(model.ModelKindEmbeddings)
	s.created = append(s.created, e)
	// The bundle moves the provider to an API without embeddings, turns e
	// into a completion model and adds a model: the operator selects the
	// type change and the new model, not the kind change e needs.
	bundle := model.NewProviderBundle(s.provider, s.created, false, time.Now())
	bundle.Provider.Type = "anthropic"
	bundle.Models[0].Kind = model.ModelKindCompletion
	bundle.Models = append(bundle.Models, model.ProviderBundleModel{RealModel: "chat-1", ProxyName: "chat", Enabled: true, Prices: model.ModelPrices{Input: 1, Output: 2}})
	doc, _ := json.Marshal(bundle)

	fields := url.Values{"step": {"apply"}, "bundle": {string(doc)}, "setting": {service.BundleFieldType}, "create": {"1"}}
	rr := httptest.NewRecorder()
	h.importProviderBundle(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want the page rather than a success redirect", rr.Code)
	}
	if !strings.Contains(stdhtml.UnescapeString(rr.Body.String()), "Le type du fournisseur n'a pas été changé") {
		t.Error("the page should say the provider type was not changed")
	}
	// The store does not implement SaveProvider: writing the refused type
	// change would have panicked.
	if len(s.created) != 2 {
		t.Errorf("created %d models, want the selected one written next to the refusal", len(s.created)-1)
	}
}

func TestProviderBundle_SaysNothingWasWrittenWhenTheOnlyChangeIsRefused(t *testing.T) {
	h, s, tenant := importFixture(t, "https://example.test/v1", "sk")
	e := model.NewLLMModel(s.provider.ID(), s.org.ID(), "e", "embed-1", "", 10, 0)
	e.SetKind(model.ModelKindEmbeddings)
	s.created = append(s.created, e)
	bundle := model.NewProviderBundle(s.provider, s.created, false, time.Now())
	bundle.Provider.Type = "anthropic"
	bundle.Models[0].Kind = model.ModelKindCompletion
	doc, _ := json.Marshal(bundle)

	fields := url.Values{"step": {"apply"}, "bundle": {string(doc)}, "setting": {service.BundleFieldType}}
	rr := httptest.NewRecorder()
	h.importProviderBundle(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	page := stdhtml.UnescapeString(rr.Body.String())
	if rr.Code != http.StatusOK || !strings.Contains(page, "Rien n'a été écrit") || !strings.Contains(page, "Le type du fournisseur n'a pas été changé") {
		t.Errorf("status = %d, want the page saying nothing was written and why", rr.Code)
	}
}

func TestProviderBundle_ReportsAFailedUpdateAsAFailureNotAConflict(t *testing.T) {
	h, s, tenant := importFixture(t, "https://example.test/v1", "sk", [2]string{"chat", "chat-1"})
	s.saveErr = errors.New("store unavailable")
	s.provider.(*model.BaseProvider).SetRequestHeaders(&model.ProviderRequestHeaders{Headers: map[string]string{"X-Api-Token": "secret"}})
	bundle := model.NewProviderBundle(s.provider, s.created, false, time.Now())
	bundle.Models[0].Description = "A chat model"
	doc, _ := json.Marshal(bundle)

	fields := url.Values{"step": {"apply"}, "bundle": {string(doc)}, "field": {"0:" + service.BundleFieldDescription}}
	rr := httptest.NewRecorder()
	h.importProviderBundle(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	page := stdhtml.UnescapeString(rr.Body.String())
	if rr.Code != http.StatusOK || !strings.Contains(page, "chat : l'enregistrement a échoué, réessayez.") {
		t.Errorf("status = %d, want the update of chat reported as a failure to retry", rr.Code)
	}
	if strings.Contains(page, "Un modèle s'appelle déjà « chat »") {
		t.Error("the failed update is reported as a name conflict: chat holds its own name")
	}
	if !strings.Contains(page, "exportés, leur nom évoquant un identifiant : X-Api-Token") {
		t.Error("the report page should still name the header left out of the bundle")
	}
}

func TestProviderBundle_AsksForTheCurrencyOfAProviderWithoutOne(t *testing.T) {
	h, s, tenant := importFixture(t, "https://example.test/v1", "sk")
	s.provider.(*model.BaseProvider).SetCurrency("")
	doc := `{"kind":"xolo.provider","version":"1","provider":{"type":"openai","currency":"EUR"},"models":[]}`
	fields := url.Values{"step": {"preview"}, "bundle": {doc}}
	rr := httptest.NewRecorder()
	h.importProviderBundle(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(stdhtml.UnescapeString(rr.Body.String()), "Renseignez la devise du fournisseur") {
		t.Errorf("status = %d, want the currency asked for rather than a rate to no currency", rr.Code)
	}
}

func TestProviderBundle_RefusesAtThePreviewAPriceTheConversionTakesOutOfBounds(t *testing.T) {
	h, s, tenant := importFixture(t, "https://example.test/v1", "sk")
	h.exchangeRateService = service.NewExchangeRateService(fixedRates{"EUR>USD": 10}, &memoryRates{rates: map[string]model.ExchangeRate{}}, time.Hour)
	doc := `{"kind":"xolo.provider","version":"1","provider":{"type":"openai","currency":"EUR"},` +
		`"models":[{"real_model":"m","proxy_name":"m","enabled":true,"prices":{"input":500000,"output":1}}]}`
	fields := url.Values{"step": {"preview"}, "bundle": {doc}}
	rr := httptest.NewRecorder()
	h.importProviderBundle(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(stdhtml.UnescapeString(rr.Body.String()), "Converti en USD") {
		t.Errorf("status = %d, want 5000000 USD refused at the preview, the conversion named", rr.Code)
	}
}
