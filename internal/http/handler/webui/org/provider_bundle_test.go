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
	"os"
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

func TestCatalogImport_PreviewsTheCatalogProviderAsABundle(t *testing.T) {
	h, s, tenant := importFixture(t, "https://opencode.ai/inference/go/v1", "sk")
	h.SetCatalog(catalog.NewStaticSource(testCatalogRelease(t)))

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

func TestCatalogImport_ConvertsPricesIntoTheProvidersCurrency(t *testing.T) {
	h, s, tenant := importFixture(t, "https://opencode.ai/inference/go/v1", "sk")
	s.provider = model.NewProvider(s.org.ID(), "OpenCode Go", "openai", "https://opencode.ai/inference/go/v1", s.provider.APIKey(), "EUR")
	h.SetCatalog(catalog.NewStaticSource(testCatalogRelease(t)))
	h.exchangeRateService = service.NewExchangeRateService(fixedRates{"USD>EUR": 0.5}, &memoryRates{rates: map[string]model.ExchangeRate{}}, time.Hour)

	fields := url.Values{"catalog_provider": {"opencode-go"}, "plan": {"go"}}
	rr := httptest.NewRecorder()
	h.previewCatalogImport(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	// The attribute value is read escaped: its JSON quotes are entities.
	page := rr.Body.String()
	i := strings.Index(page, `name="bundle" value="`)
	if i < 0 {
		t.Fatal("no bundle in the page")
	}
	raw := page[i+len(`name="bundle" value="`):]
	raw = stdhtml.UnescapeString(raw[:strings.Index(raw, `"`)])
	bundle, err := model.ParseProviderBundle([]byte(raw))
	if err != nil {
		t.Fatalf("bundle: %v", err)
	}
	if bundle.Provider.Currency != "EUR" {
		t.Errorf("currency = %q, want the provider's", bundle.Provider.Currency)
	}
	snapshot := testCatalogRelease(t)
	original, _ := snapshot.Bundle("opencode-go", "go")
	if got, want := bundle.Models[0].Prices.Output, original.Models[0].Prices.Output/2; got != model.PricePerMillion(model.MicrocentsPer1K(want)) {
		t.Errorf("output price = %v, want %v converted at 0.5", got, want)
	}
	// The plan's per-model value budgets are converted too.
	converted, before := bundle.Provider.SubscriptionPlan.Constraints[0], original.Provider.SubscriptionPlan.Constraints[0]
	checked := 0
	for id, b := range before.ModelBudgets {
		if b.ValueBudget == nil {
			continue
		}
		got := converted.ModelBudgets[id].ValueBudget
		if got == nil || *got != *b.ValueBudget/2 {
			t.Errorf("budget of %s = %v, want %d converted at 0.5", id, got, *b.ValueBudget/2)
		}
		checked++
	}
	if checked == 0 {
		t.Error("the snapshot plan has no value budget to check")
	}
}

func TestCatalogImport_RefusesWithoutAnExchangeRate(t *testing.T) {
	h, s, tenant := importFixture(t, "https://opencode.ai/inference/go/v1", "sk")
	s.provider = model.NewProvider(s.org.ID(), "OpenCode Go", "openai", "https://opencode.ai/inference/go/v1", s.provider.APIKey(), "EUR")
	h.SetCatalog(catalog.NewStaticSource(testCatalogRelease(t)))

	fields := url.Values{"catalog_provider": {"opencode-go"}, "plan": {"none"}}
	rr := httptest.NewRecorder()
	h.previewCatalogImport(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want the import refused rather than prices left in USD", rr.Code)
	}
}

func TestConvertBundleCurrency_ConvertsConstraintBudgetsToo(t *testing.T) {
	h := &Handler{exchangeRateService: service.NewExchangeRateService(fixedRates{"USD>EUR": 0.5}, &memoryRates{rates: map[string]model.ExchangeRate{}}, time.Hour)}
	budget := int64(10_000_000)
	bundle := model.ProviderBundle{Provider: model.ProviderBundleSettings{Currency: "USD", SubscriptionPlan: &model.SubscriptionPlan{
		Constraints: []model.PlanConstraint{{Kind: model.ConstraintRollingWindow, Label: "5h", ValueBudget: &budget}},
	}}}
	if err := h.convertBundleCurrency(context.Background(), &bundle, "EUR"); err != nil {
		t.Fatal(err)
	}
	if got := bundle.Provider.SubscriptionPlan.Constraints[0].ValueBudget; got == nil || *got != 5_000_000 {
		t.Errorf("constraint budget = %v, want 5000000 microcents", got)
	}
	if budget != 10_000_000 {
		t.Error("the conversion changed the source budget in place")
	}
}

func TestCatalogImport_TellsAMalformedReleaseFromAnUnknownProvider(t *testing.T) {
	h, s, tenant := importFixture(t, "https://acme.test/v1", "sk")
	broken := &catalog.Catalog{Kind: catalog.Kind, Format: catalog.Format, Version: "v-broken", Providers: []catalog.Provider{{
		ID: "acme", Name: "Acme", Currency: "USD",
		Plans: []catalog.Plan{{ID: "pro", Name: "Pro", Constraints: []catalog.Constraint{{Kind: "rolling_window", Label: "5h", Duration: "five hours"}}}},
	}}}
	h.SetCatalog(catalog.NewStaticSource(broken))

	for plan, want := range map[string]string{"pro": "v-broken", "gold": "pas dans le catalogue"} {
		fields := url.Values{"catalog_provider": {"acme"}, "plan": {plan}}
		rr := httptest.NewRecorder()
		h.previewCatalogImport(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
		page := stdhtml.UnescapeString(rr.Body.String())
		if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(page, want) {
			t.Errorf("plan %s: status %d, want 422 mentioning %q", plan, rr.Code, want)
		}
		if strings.Contains(page, `name="bundle_file"`) || !strings.Contains(page, `name="catalog_provider"`) {
			t.Errorf("plan %s: the refusal should be said on the catalog page, where the operator chooses again", plan)
		}
	}
}

// testCatalogRelease is the release of the catalog kept for the tests.
func testCatalogRelease(t *testing.T) *catalog.Catalog {
	t.Helper()
	data, err := os.ReadFile("../../../../catalog/testdata/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := catalog.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCatalogImport_IsNotOfferedWhenTheCatalogIsDisabled(t *testing.T) {
	h, s, tenant := importFixture(t, "https://opencode.ai/inference/go/v1", "sk")
	rr := httptest.NewRecorder()
	h.getCatalogImportPage(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(""), "text/plain"))
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 with no catalog", rr.Code)
	}
}

func TestCatalogImport_SaysWhenTheCatalogCannotBeRead(t *testing.T) {
	h, s, tenant := importFixture(t, "https://opencode.ai/inference/go/v1", "sk")
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer down.Close()
	source := catalog.NewSource(down.URL, time.Hour)
	source.Refresh(context.Background())
	h.SetCatalog(source)

	rr := httptest.NewRecorder()
	h.getCatalogImportPage(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(""), "text/plain"))
	page := stdhtml.UnescapeString(rr.Body.String())
	if rr.Code != http.StatusServiceUnavailable || !strings.Contains(page, "ne peut pas être lu") {
		t.Errorf("status = %d, want 503 and the page saying the catalog cannot be read", rr.Code)
	}
	if strings.Contains(page, `name="bundle_file"`) {
		t.Error("the catalog page should say it, not the bundle file import page")
	}

	fields := url.Values{"catalog_provider": {"opencode-go"}, "plan": {"none"}}
	rr = httptest.NewRecorder()
	h.previewCatalogImport(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	if page := stdhtml.UnescapeString(rr.Body.String()); rr.Code != http.StatusServiceUnavailable || strings.Contains(page, `name="bundle_file"`) || !strings.Contains(page, "ne peut pas être lu") {
		t.Errorf("posted: status = %d, want 503 said on the catalog page", rr.Code)
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

func TestCatalogImport_RefusesAReleaseWhoseBundleDoesNotValidate(t *testing.T) {
	h, s, tenant := importFixture(t, "https://acme.test/v1", "sk")
	p := catalog.Provider{ID: "acme", Name: "Acme", Currency: "USD", Offerings: []catalog.Offering{{Model: "glm", ModelID: "glm", Prices: catalog.Prices{Input: 1, Output: 2}}}}
	p.Xolo.Type, p.Xolo.BaseURL = "no-such-type", "https://acme.test/v1"
	h.SetCatalog(catalog.NewStaticSource(&catalog.Catalog{Kind: catalog.Kind, Format: catalog.Format, Version: "v-invalid", Providers: []catalog.Provider{p}}))

	fields := url.Values{"catalog_provider": {"acme"}, "plan": {"none"}}
	rr := httptest.NewRecorder()
	h.previewCatalogImport(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	if page := stdhtml.UnescapeString(rr.Body.String()); rr.Code != http.StatusUnprocessableEntity || !strings.Contains(page, "v-invalid") || !strings.Contains(page, "no-such-type") {
		t.Errorf("status %d, want 422 naming the release and the entry at fault rather than a dry run the apply would refuse", rr.Code)
	}
}

func TestCatalogImport_RefusesAProviderWithoutCurrencyAtThePreview(t *testing.T) {
	h, s, tenant := importFixture(t, "https://acme.test/v1", "sk")
	s.provider.(*model.BaseProvider).SetCurrency("")
	p := catalog.Provider{ID: "acme", Name: "Acme", Currency: "USD", Offerings: []catalog.Offering{{Model: "glm", ModelID: "glm", Prices: catalog.Prices{Input: 1, Output: 2}}}}
	p.Xolo.Type, p.Xolo.BaseURL = "openai", "https://acme.test/v1"
	h.SetCatalog(catalog.NewStaticSource(&catalog.Catalog{Kind: catalog.Kind, Format: catalog.Format, Version: "v1", Providers: []catalog.Provider{p}}))

	fields := url.Values{"catalog_provider": {"acme"}, "plan": {"none"}}
	rr := httptest.NewRecorder()
	h.previewCatalogImport(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(stdhtml.UnescapeString(rr.Body.String()), "Renseignez la devise du fournisseur") {
		t.Errorf("status %d, want 422 asking for the currency at the preview rather than a dry run the apply refuses", rr.Code)
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

func TestCatalogImport_RefusesAReleasePlanThePlanEditorRefuses(t *testing.T) {
	h, s, tenant := importFixture(t, "https://acme.test/v1", "sk")
	p := catalog.Provider{ID: "acme", Name: "Acme", Currency: "USD",
		Offerings: []catalog.Offering{{Model: "glm", ModelID: "glm", Prices: catalog.Prices{Input: 1, Output: 2}}},
		// A window of over ten years, which the plan editor refuses.
		Plans: []catalog.Plan{{ID: "pro", Name: "Pro", Constraints: []catalog.Constraint{{Kind: "rolling_window", Label: "forever", Duration: "100000h"}}}}}
	p.Xolo.Type, p.Xolo.BaseURL = "openai", "https://acme.test/v1"
	h.SetCatalog(catalog.NewStaticSource(&catalog.Catalog{Kind: catalog.Kind, Format: catalog.Format, Version: "v-long", Providers: []catalog.Provider{p}}))

	fields := url.Values{"catalog_provider": {"acme"}, "plan": {"pro"}}
	rr := httptest.NewRecorder()
	h.previewCatalogImport(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(stdhtml.UnescapeString(rr.Body.String()), "v-long") {
		t.Errorf("status %d, want 422 naming the release rather than a dry run the apply would refuse", rr.Code)
	}
}

func TestCatalogImport_KeepsTheEndpointsOfAnotherProviderOut(t *testing.T) {
	h, s, tenant := importFixture(t, "https://acme.test/v1", "sk")
	beta := catalog.Provider{ID: "beta", Name: "Beta", Currency: "USD", Offerings: []catalog.Offering{
		{Model: "glm", ModelID: "glm", Prices: catalog.Prices{Input: 1, Output: 2}},
		{Model: "haiku", ModelID: "haiku", APIType: "anthropic", BaseURL: "https://beta.test/anthropic/v1", Prices: catalog.Prices{Input: 1, Output: 5}},
	}}
	beta.Xolo.Type, beta.Xolo.BaseURL = "openrouter", "https://beta.test/v1"
	h.SetCatalog(catalog.NewStaticSource(&catalog.Catalog{Kind: catalog.Kind, Format: catalog.Format, Version: "v1", Providers: []catalog.Provider{beta}}))

	fields := url.Values{"catalog_provider": {"beta"}, "plan": {"none"}}
	rr := httptest.NewRecorder()
	h.previewCatalogImport(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	page := stdhtml.UnescapeString(rr.Body.String())
	if rr.Code != http.StatusOK || strings.Contains(page, "beta.test") || strings.Contains(page, "openrouter") {
		t.Errorf("status %d: the dry run offers Beta's endpoints or API to a provider at another address", rr.Code)
	}
	if !strings.Contains(page, "n'est pas à l'adresse de") {
		t.Error("the page should say Beta's endpoints are left out")
	}
}

func TestCatalogImport_KeepsOutTheEndpointsOfAProviderWithAnOfferingAtTheTarget(t *testing.T) {
	h, s, tenant := importFixture(t, "https://acme.test/v1", "sk")
	// Beta serves one model at the target's address, its own endpoint is
	// elsewhere: the target is not Beta.
	beta := catalog.Provider{ID: "beta", Name: "Beta", Currency: "USD", Offerings: []catalog.Offering{
		{Model: "glm", ModelID: "glm", APIType: "openai", BaseURL: "https://acme.test/v1", Prices: catalog.Prices{Input: 1, Output: 2}},
		{Model: "haiku", ModelID: "haiku", Prices: catalog.Prices{Input: 1, Output: 5}},
	}}
	beta.Xolo.Type, beta.Xolo.BaseURL = "openrouter", "https://beta.test/v1"
	h.SetCatalog(catalog.NewStaticSource(&catalog.Catalog{Kind: catalog.Kind, Format: catalog.Format, Version: "v1", Providers: []catalog.Provider{beta}}))

	fields := url.Values{"catalog_provider": {"beta"}, "plan": {"none"}}
	rr := httptest.NewRecorder()
	h.previewCatalogImport(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	if page := stdhtml.UnescapeString(rr.Body.String()); rr.Code != http.StatusOK || strings.Contains(page, "beta.test") || strings.Contains(page, "openrouter") {
		t.Errorf("status %d: Beta's endpoint or API is offered to the provider at acme.test", rr.Code)
	}
}

func TestCatalogImport_LeavesOutTheModelsTheTargetAPIDoesNotServe(t *testing.T) {
	h, s, tenant := importFixture(t, "https://acme.test/v1", "sk")
	s.provider.(*model.BaseProvider).SetType("anthropic")
	beta := catalog.Provider{ID: "beta", Name: "Beta", Currency: "USD", Offerings: []catalog.Offering{
		{Model: "chat", ModelID: "chat", Prices: catalog.Prices{Input: 1, Output: 2}},
		{Model: "embed", ModelID: "embed", Prices: catalog.Prices{Input: 1}},
	}, Plans: []catalog.Plan{{ID: "pro", Name: "Pro", Constraints: []catalog.Constraint{{
		Kind: "rolling_window", Label: "5h", Duration: "5h", Scope: "model",
		ModelBudgets: map[string]catalog.Budget{"chat": {}, "embed": {}},
	}}}}}
	beta.Xolo.Type, beta.Xolo.BaseURL = "openai", "https://beta.test/v1"
	h.SetCatalog(catalog.NewStaticSource(&catalog.Catalog{Kind: catalog.Kind, Format: catalog.Format, Version: "v1",
		Models:    []catalog.Model{{ID: "chat", Name: "Chat"}, {ID: "embed", Name: "Embed", Kind: "embeddings"}},
		Providers: []catalog.Provider{beta}}))

	fields := url.Values{"catalog_provider": {"beta"}, "plan": {"pro"}}
	rr := httptest.NewRecorder()
	h.previewCatalogImport(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	page := stdhtml.UnescapeString(rr.Body.String())
	if strings.Contains(page, `"embed"`) || !strings.Contains(page, "Leurs budgets des contraintes par modèle sont retirés") {
		t.Error("the plan posted back keeps a budget for the embeddings model left out, or does not say it goes")
	}
	if rr.Code != http.StatusOK || !strings.Contains(page, "Modèles laissés de côté, que l'API anthropic ne sert pas ici : 1.") {
		t.Errorf("status %d, want the dry run with the embeddings model left out and said so", rr.Code)
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

func TestCatalogImport_DecidesTheAPIOfAnotherProvidersOfferingsOneByOne(t *testing.T) {
	h, s, tenant := importFixture(t, "https://acme.test/v1", "sk")
	beta := catalog.Provider{ID: "beta", Name: "Beta", Currency: "USD", Offerings: []catalog.Offering{
		{Model: "glm", ModelID: "glm", Prices: catalog.Prices{Input: 1, Output: 2}},
		// Served at the target's address through the Anthropic API.
		{Model: "haiku", ModelID: "haiku", APIType: "anthropic", BaseURL: "https://acme.test/v1", Prices: catalog.Prices{Input: 1, Output: 5}},
		// Served elsewhere through the Gemini API only.
		{Model: "argon", ModelID: "argon", APIType: "gemini", BaseURL: "https://beta.test/gemini", Prices: catalog.Prices{Input: 1, Output: 4}},
	}}
	beta.Xolo.Type, beta.Xolo.BaseURL = "openai", "https://beta.test/v1"
	h.SetCatalog(catalog.NewStaticSource(&catalog.Catalog{Kind: catalog.Kind, Format: catalog.Format, Version: "v1", Providers: []catalog.Provider{beta}}))

	fields := url.Values{"catalog_provider": {"beta"}, "plan": {"none"}}
	rr := httptest.NewRecorder()
	h.previewCatalogImport(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	page := stdhtml.UnescapeString(rr.Body.String())
	if rr.Code != http.StatusOK || strings.Contains(page, "beta.test") || strings.Contains(page, `"real_model":"argon"`) {
		t.Errorf("status %d: argon, only served through Gemini at Beta's, should be left out", rr.Code)
	}
	if !strings.Contains(page, `"api_type":"anthropic"`) || !strings.Contains(page, "que l'API openai ne sert pas ici : 1.") {
		t.Error("haiku should keep the Anthropic API served at the provider's address, and argon be counted out")
	}
}

func TestCatalogImport_KeepsTheCatalogProvidersAPIForAnOfferingAtTheTarget(t *testing.T) {
	// The provider is declared openai, at an address where Beta serves its
	// own Anthropic API: the model takes that API, as the pre-fill gives it.
	h, s, tenant := importFixture(t, "https://acme.test/v1", "sk")
	beta := catalog.Provider{ID: "beta", Name: "Beta", Currency: "USD", Offerings: []catalog.Offering{
		{Model: "haiku", ModelID: "haiku", APIType: "anthropic", BaseURL: "https://acme.test/v1", Prices: catalog.Prices{Input: 1, Output: 5}},
	}}
	beta.Xolo.Type, beta.Xolo.BaseURL = "anthropic", "https://beta.test/v1"
	h.SetCatalog(catalog.NewStaticSource(&catalog.Catalog{Kind: catalog.Kind, Format: catalog.Format, Version: "v1", Providers: []catalog.Provider{beta}}))

	fields := url.Values{"catalog_provider": {"beta"}, "plan": {"none"}}
	rr := httptest.NewRecorder()
	h.previewCatalogImport(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	if page := stdhtml.UnescapeString(rr.Body.String()); rr.Code != http.StatusOK || !strings.Contains(page, `"api_type":"anthropic"`) {
		t.Errorf("status %d, want haiku on the Anthropic API served at the provider's address", rr.Code)
	}
}

func TestCatalogImport_KeepsTheSharedBudgetsOfTheModelsItLeavesOut(t *testing.T) {
	h, s, tenant := importFixture(t, "https://acme.test/v1", "sk")
	s.provider.(*model.BaseProvider).SetType("anthropic")
	tokens := func(v int64) *int64 { return &v }
	beta := catalog.Provider{ID: "beta", Name: "Beta", Currency: "USD", Offerings: []catalog.Offering{
		{Model: "chat", ModelID: "chat", Prices: catalog.Prices{Input: 1, Output: 2}},
		{Model: "embed", ModelID: "embed", Prices: catalog.Prices{Input: 1}},
	}, Plans: []catalog.Plan{{ID: "pro", Name: "Pro", Constraints: []catalog.Constraint{{
		Kind: "rolling_window", Label: "5h", Duration: "5h", Scope: "shared", TokenBudget: tokens(1000),
		ModelBudgets: map[string]catalog.Budget{"chat": {TokenBudget: tokens(1000)}, "embed": {TokenBudget: tokens(10)}},
	}}}}}
	beta.Xolo.Type, beta.Xolo.BaseURL = "openai", "https://beta.test/v1"
	h.SetCatalog(catalog.NewStaticSource(&catalog.Catalog{Kind: catalog.Kind, Format: catalog.Format, Version: "v1",
		Models:    []catalog.Model{{ID: "chat", Name: "Chat"}, {ID: "embed", Name: "Embed", Kind: "embeddings"}},
		Providers: []catalog.Provider{beta}}))

	fields := url.Values{"catalog_provider": {"beta"}, "plan": {"pro"}}
	rr := httptest.NewRecorder()
	h.previewCatalogImport(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	// The smallest budget weighs the models the plan does not list.
	page := stdhtml.UnescapeString(rr.Body.String())
	if rr.Code != http.StatusOK || !strings.Contains(page, `"embed":{"token_budget":10}`) {
		t.Errorf("status %d, want the shared budget of embed kept in the plan posted back", rr.Code)
	}
	if strings.Contains(page, "Leurs budgets des contraintes par modèle sont retirés") {
		t.Error("the page says budgets were removed, none was")
	}
}

func TestCatalogImport_KeepsTheSettingsTheCatalogDoesNotDescribe(t *testing.T) {
	h, s, tenant := importFixture(t, "https://acme.test/v1", "sk")
	target := s.provider.(*model.BaseProvider)
	target.SetCloudTier(2)
	target.SetRetryConfig(&model.RetryConfig{Enabled: true, MaxAttempts: 7, Delay: time.Second})
	target.SetBillingMode(model.BillingModeSubscription)
	acme := catalog.Provider{ID: "acme", Name: "Acme", Currency: "USD", Offerings: []catalog.Offering{
		{Model: "glm", ModelID: "glm", Prices: catalog.Prices{Input: 1, Output: 2}},
	}}
	acme.Xolo.Type, acme.Xolo.BaseURL = "openai", "https://acme.test/v1"
	h.SetCatalog(catalog.NewStaticSource(&catalog.Catalog{Kind: catalog.Kind, Format: catalog.Format, Version: "v1", Providers: []catalog.Provider{acme}}))

	fields := url.Values{"catalog_provider": {"acme"}, "plan": {"none"}}
	rr := httptest.NewRecorder()
	h.previewCatalogImport(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	page := stdhtml.UnescapeString(rr.Body.String())
	for _, kept := range []string{`"cloud_tier":2`, `"max_attempts":7`, `"billing_mode":"subscription"`} {
		if !strings.Contains(page, kept) {
			t.Errorf("status %d: the bundle posted back lacks %s, which the catalog does not describe", rr.Code, kept)
		}
	}
}

func TestCatalogImport_NamesTheBudgetThatNamesNoOffering(t *testing.T) {
	h, s, tenant := importFixture(t, "https://acme.test/v1", "sk")
	acme := catalog.Provider{ID: "acme", Name: "Acme", Currency: "USD",
		Offerings: []catalog.Offering{{Model: "glm", ModelID: "glm", Prices: catalog.Prices{Input: 1, Output: 2}}},
		Plans: []catalog.Plan{{ID: "pro", Name: "Pro", Constraints: []catalog.Constraint{{
			Kind: "rolling_window", Label: "5h", Duration: "5h", Scope: "model", ModelBudgets: map[string]catalog.Budget{"ghost": {}},
		}}}}}
	acme.Xolo.Type, acme.Xolo.BaseURL = "openai", "https://acme.test/v1"
	h.SetCatalog(catalog.NewStaticSource(&catalog.Catalog{Kind: catalog.Kind, Format: catalog.Format, Version: "v-ghost", Providers: []catalog.Provider{acme}}))

	fields := url.Values{"catalog_provider": {"acme"}, "plan": {"pro"}}
	rr := httptest.NewRecorder()
	h.previewCatalogImport(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	if page := stdhtml.UnescapeString(rr.Body.String()); rr.Code != http.StatusUnprocessableEntity || !strings.Contains(page, "v-ghost") || !strings.Contains(page, `"ghost"`) {
		t.Errorf("status %d, want 422 naming the release and the budget at fault", rr.Code)
	}
}
