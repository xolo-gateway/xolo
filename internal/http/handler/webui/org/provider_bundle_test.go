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

// fixedRates serves one exchange rate.
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

func TestCatalogImport_ConvertsPricesIntoTheProvidersCurrency(t *testing.T) {
	h, s, tenant := importFixture(t, "https://opencode.ai/inference/go/v1", "sk")
	s.provider = model.NewProvider(s.org.ID(), "OpenCode Go", "openai", "https://opencode.ai/inference/go/v1", s.provider.APIKey(), "EUR")
	h.SetCatalog(catalog.NewStaticSource(catalog.Snapshot()))
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
	snapshot := catalog.Snapshot()
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
	h.SetCatalog(catalog.NewStaticSource(catalog.Snapshot()))

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
