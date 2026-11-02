package org

import (
	"bytes"
	"context"
	"encoding/json"
	stdhtml "html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/catalog"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/core/service"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
)

// memoryCatalogOrigins is a port.CatalogOriginStore in memory.
type memoryCatalogOrigins struct {
	origins map[model.LLMModelID]model.CatalogOrigin
}

func (s *memoryCatalogOrigins) GetCatalogOrigin(_ context.Context, id model.LLMModelID) (*model.CatalogOrigin, error) {
	o, ok := s.origins[id]
	if !ok {
		return nil, nil
	}
	return &o, nil
}

func (s *memoryCatalogOrigins) ListCatalogTrackedModels(context.Context) ([]model.CatalogTrackedModel, error) {
	return nil, nil
}

func (s *memoryCatalogOrigins) SaveCatalogOrigin(_ context.Context, id model.LLMModelID, o *model.CatalogOrigin) error {
	if o == nil {
		delete(s.origins, id)
		return nil
	}
	s.origins[id] = *o
	return nil
}

func (s *memoryCatalogOrigins) UpdateCatalogOrigin(ctx context.Context, id model.LLMModelID, o model.CatalogOrigin) error {
	if _, ok := s.origins[id]; !ok {
		return port.ErrNotFound
	}
	return s.SaveCatalogOrigin(ctx, id, &o)
}

// hiddenValue reads the value of the hidden field name of page.
func hiddenValue(t *testing.T, page, name string) string {
	t.Helper()
	marker := `name="` + name + `" value="`
	i := strings.Index(page, marker)
	if i < 0 {
		t.Fatalf("no %s field in the page", name)
	}
	v := page[i+len(marker):]
	return stdhtml.UnescapeString(v[:strings.Index(v, `"`)])
}

func TestCatalogImport_RecordsTheOriginOfTheModelsItWrites(t *testing.T) {
	h, s, tenant := importFixture(t, "https://opencode.ai/inference/go/v1", "sk")
	release := testCatalogRelease(t)
	h.SetCatalog(catalog.NewStaticSource(release))
	origins := &memoryCatalogOrigins{origins: map[model.LLMModelID]model.CatalogOrigin{}}
	h.SetCatalogOrigins(origins)

	rr := httptest.NewRecorder()
	fields := url.Values{"catalog_provider": {"opencode-go"}, "plan": {"none"}}
	h.previewCatalogImport(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	if rr.Code != http.StatusOK {
		t.Fatalf("preview status = %d: %s", rr.Code, rr.Body.String())
	}
	page := rr.Body.String()
	bundle, err := model.ParseProviderBundle([]byte(hiddenValue(t, page, "bundle")))
	if err != nil {
		t.Fatal(err)
	}

	// Only the first model is created.
	apply := url.Values{"step": {"apply"}, "bundle": {hiddenValue(t, page, "bundle")}, "catalog_import": {hiddenValue(t, page, "catalog_import")}, "create": {"0"}}
	rr = httptest.NewRecorder()
	h.importProviderBundle(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(apply.Encode()), "application/x-www-form-urlencoded"))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("apply status = %d: %s", rr.Code, rr.Body.String())
	}
	if len(s.created) != 1 {
		t.Fatalf("created %d models, want 1", len(s.created))
	}
	if len(origins.origins) != 1 {
		t.Fatalf("recorded %d origins, want the created model's only", len(origins.origins))
	}
	origin, ok := origins.origins[s.created[0].ID()]
	if !ok {
		t.Fatal("the created model has no origin")
	}
	tb, err := release.BundleFor("opencode-go", "", s.provider)
	if err != nil {
		t.Fatal(err)
	}
	want := tb.Bundle.Models[0]
	if origin.Provider != "opencode-go" || origin.Version != release.Version || origin.Model != want.RealModel || origin.Currency != "USD" {
		t.Errorf("origin = %s %s %s %s, want the catalog entry", origin.Provider, origin.Version, origin.Model, origin.Currency)
	}
	if origin.Catalog.Prices != want.Prices || origin.Applied.Prices != bundle.Models[0].Prices {
		t.Errorf("origin prices = %v / %v, want the catalog's", origin.Catalog.Prices, origin.Applied.Prices)
	}
}

// catalogModelFixture is a handler with one model that follows the catalog:
// the operator narrowed its context window.
func catalogModelFixture(t *testing.T) (*Handler, *modelFormStore, *memoryCatalogOrigins, model.LLMModel, func(method string, fields url.Values) *http.Request) {
	t.Helper()
	h, s, tenant := importFixture(t, "https://api.example.com/v1", "sk")
	catalogValues := model.ProviderBundleModel{RealModel: "large-latest", ProxyName: "large-latest", Enabled: true, Prices: model.ModelPrices{Input: 2, Output: 6}, ContextWindow: 128_000, ActiveParams: 40_000_000_000}
	local := catalogValues
	local.ContextWindow = 64_000
	m := model.NewLLMModelFromBundle(model.NewLLMModelID(), s.provider.ID(), s.org.ID(), s.provider.CreatedAt(), local)
	s.created = append(s.created, m)
	origins := &memoryCatalogOrigins{origins: map[model.LLMModelID]model.CatalogOrigin{m.ID(): {
		Provider: "example", Model: "large-latest", Version: "v0.1.0", Currency: "USD",
		Catalog: catalogValues, Applied: catalogValues, Estimated: []string{"active_params", "context_window"},
	}}}
	h.SetCatalogOrigins(origins)
	request := func(method string, fields url.Values) *http.Request {
		req := importRequest(t, s, tenant, method, fields)
		req.SetPathValue("modelID", string(m.ID()))
		return req.WithContext(httpCtx.SetPermissionResolver(req.Context(), func(context.Context, model.OrgID) (rbac.PermissionSet, error) {
			return rbac.OwnerPermissionSet(), nil
		}))
	}
	return h, s, origins, m, request
}

func TestModelForm_ShowsTheCatalogFollowUp(t *testing.T) {
	h, _, _, m, request := catalogModelFixture(t)
	rr := httptest.NewRecorder()
	h.getEditModelPage(rr, request(http.MethodGet, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	page := stdhtml.UnescapeString(rr.Body.String())
	if !strings.Contains(page, "Suivi du catalogue") || !strings.Contains(page, "example, large-latest, release v0.1.0") {
		t.Error("the form should say which catalog entry the model follows")
	}
	accept := "/models/" + string(m.ID()) + "/catalog/accept"
	if !strings.Contains(page, accept) || !strings.Contains(page, `name="field" value="context_window"`) {
		t.Error("the form should offer the catalog's window, which the operator changed")
	}
	if strings.Contains(page, `name="field" value="prompt_cost"`) {
		t.Error("the prices are the catalog's: nothing to offer")
	}
	// The active parameters hold the catalog's estimate; the window the
	// operator changed does not any more.
	if n := strings.Count(page, ">estimé<"); n != 1 {
		t.Errorf("%d estimated badges, want the active parameters' only", n)
	}
	if !strings.Contains(page, "/catalog/detach") {
		t.Error("the form should offer to stop following the catalog")
	}
}

func TestModelForm_OffersNoCatalogActionToAReader(t *testing.T) {
	h, _, _, _, request := catalogModelFixture(t)
	req := request(http.MethodGet, nil)
	req = req.WithContext(httpCtx.SetPermissionResolver(req.Context(), func(context.Context, model.OrgID) (rbac.PermissionSet, error) {
		return rbac.NewPermissionSet([]string{string(rbac.PermProvidersRead)}, nil), nil
	}))
	rr := httptest.NewRecorder()
	h.getEditModelPage(rr, req)
	page := stdhtml.UnescapeString(rr.Body.String())
	if !strings.Contains(page, "Suivi du catalogue") || strings.Contains(page, "/catalog/accept") || strings.Contains(page, "/catalog/detach") {
		t.Error("a reader sees the follow-up, without its actions")
	}
}

func TestModelForm_ShowsNoCatalogFollowUpForAModelWithoutOrigin(t *testing.T) {
	h, _, origins, m, request := catalogModelFixture(t)
	delete(origins.origins, m.ID())
	rr := httptest.NewRecorder()
	h.getEditModelPage(rr, request(http.MethodGet, nil))
	if page := stdhtml.UnescapeString(rr.Body.String()); strings.Contains(page, "Suivi du catalogue") || strings.Contains(page, ">estimé<") {
		t.Error("a model imported from no catalog shows no follow-up")
	}
}

func TestAcceptCatalogValue_WritesTheCatalogValueOfTheField(t *testing.T) {
	h, s, _, _, request := catalogModelFixture(t)
	rr := httptest.NewRecorder()
	h.acceptCatalogValue(rr, request(http.MethodPost, url.Values{"field": {"context_window"}}))
	if rr.Code != http.StatusSeeOther || !strings.HasSuffix(rr.Header().Get("Location"), "/edit?success=catalog_accepted") {
		t.Fatalf("status = %d, location %q: %s", rr.Code, rr.Header().Get("Location"), rr.Body.String())
	}
	if len(s.saved) != 1 || s.saved[0].ContextWindow() != 128_000 {
		t.Fatalf("saved %d models, want the catalog's window written", len(s.saved))
	}
}

func TestAcceptCatalogValue_RefusesAFieldTheCatalogDoesNotGive(t *testing.T) {
	h, s, _, _, request := catalogModelFixture(t)
	rr := httptest.NewRecorder()
	h.acceptCatalogValue(rr, request(http.MethodPost, url.Values{"field": {service.BundleFieldProxyName}}))
	if rr.Code != http.StatusUnprocessableEntity || len(s.saved) != 0 {
		t.Fatalf("status = %d with %d writes, want the form refused and nothing written", rr.Code, len(s.saved))
	}
}

func TestDetachCatalogOrigin_ForgetsTheOrigin(t *testing.T) {
	h, s, origins, m, request := catalogModelFixture(t)
	rr := httptest.NewRecorder()
	h.detachCatalogOrigin(rr, request(http.MethodPost, nil))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	if _, ok := origins.origins[m.ID()]; ok || len(s.saved) != 0 {
		t.Fatal("the origin should be forgotten, the model left as it is")
	}
	// A model without origin answers like a missing one.
	rr = httptest.NewRecorder()
	h.acceptCatalogValue(rr, request(http.MethodPost, url.Values{"field": {"context_window"}}))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("accept on a detached model = %d, want 404", rr.Code)
	}
}

var _ port.CatalogOriginStore = (*memoryCatalogOrigins)(nil)

func TestCatalogImport_PostsTheCatalogPricesUnconverted(t *testing.T) {
	h, s, tenant := importFixture(t, "https://opencode.ai/inference/go/v1", "sk")
	s.provider = model.NewProvider(s.org.ID(), "OpenCode Go", "openai", "https://opencode.ai/inference/go/v1", s.provider.APIKey(), "EUR")
	release := testCatalogRelease(t)
	h.SetCatalog(catalog.NewStaticSource(release))
	h.exchangeRateService = service.NewExchangeRateService(fixedRates{"USD>EUR": 0.5}, &memoryRates{rates: map[string]model.ExchangeRate{}}, time.Hour)

	fields := url.Values{"catalog_provider": {"opencode-go"}, "plan": {"none"}}
	rr := httptest.NewRecorder()
	h.previewCatalogImport(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	var ci service.CatalogImport
	if err := json.Unmarshal([]byte(hiddenValue(t, rr.Body.String(), "catalog_import")), &ci); err != nil {
		t.Fatal(err)
	}
	want, _ := release.Bundle("opencode-go", "")
	if ci.Currency != "USD" || len(ci.Models) == 0 || ci.Models[0].Prices != want.Models[0].Prices {
		t.Fatalf("catalog import = %s %v, want the release's USD prices", ci.Currency, ci.Models[0].Prices)
	}
}

// catalogPreview posts the catalog import form for opencode-go with prefix.
func catalogPreview(t *testing.T, h *Handler, s *modelFormStore, tenant model.Tenant, prefix string) *httptest.ResponseRecorder {
	t.Helper()
	fields := url.Values{"catalog_provider": {"opencode-go"}, "plan": {"none"}, "proxy_prefix": {prefix}}
	rr := httptest.NewRecorder()
	h.previewCatalogImport(rr, bundleRequest(t, s, tenant, string(s.provider.ID()), bytes.NewBufferString(fields.Encode()), "application/x-www-form-urlencoded"))
	return rr
}

func TestCatalogImport_PutsThePrefixBeforeEveryProxyName(t *testing.T) {
	declared := [][2]string{{"opencode-glm-5.2", "glm-5.2"}, {"opencode-deepseek-v4-pro", "deepseek-v4-pro"}}
	h, s, tenant := importFixture(t, "https://opencode.ai/inference/go/v1", "sk", declared...)
	h.SetCatalog(catalog.NewStaticSource(testCatalogRelease(t)))

	// Without the prefix, the models the provider already has are offered
	// a rename to the catalog's bare names.
	rr := catalogPreview(t, h, s, tenant, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), ":proxy_name\"") {
		t.Fatal("without a prefix, the preview should offer to rename the existing models")
	}

	rr = catalogPreview(t, h, s, tenant, " opencode- ")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	page := rr.Body.String()
	bundle, err := model.ParseProviderBundle([]byte(hiddenValue(t, page, "bundle")))
	if err != nil {
		t.Fatal(err)
	}
	for _, bm := range bundle.Models {
		if bm.ProxyName != "opencode-"+bm.RealModel {
			t.Errorf("proxy name = %q, want the prefix before %q", bm.ProxyName, bm.RealModel)
		}
	}
	if strings.Contains(page, ":proxy_name\"") {
		t.Error("with the provider's prefix, no existing model should be offered a rename")
	}
	// The origin keeps the catalog's own names.
	var ci service.CatalogImport
	if err := json.Unmarshal([]byte(hiddenValue(t, page, "catalog_import")), &ci); err != nil {
		t.Fatal(err)
	}
	if ci.Models[0].ProxyName != ci.Models[0].RealModel {
		t.Errorf("origin proxy name = %q, want the catalog's %q", ci.Models[0].ProxyName, ci.Models[0].RealModel)
	}
}

func TestCatalogImport_RefusesAPrefixWithASlash(t *testing.T) {
	h, s, tenant := importFixture(t, "https://opencode.ai/inference/go/v1", "sk")
	h.SetCatalog(catalog.NewStaticSource(testCatalogRelease(t)))
	rr := catalogPreview(t, h, s, tenant, "team/")
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want the prefix refused", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `value="team/"`) {
		t.Error("the catalog page should keep the prefix posted")
	}
}

func TestCatalogImportPage_OffersThePrefixMostModelsShare(t *testing.T) {
	cases := []struct {
		name     string
		declared [][2]string
		want     string
	}{
		{"most models", [][2]string{{"opencode-glm-5.2", "glm-5.2"}, {"opencode-kimi-k3", "kimi-k3"}, {"fast", "glm-5.3"}}, "opencode-"},
		{"half only", [][2]string{{"opencode-glm-5.2", "glm-5.2"}, {"glm-5.3", "glm-5.3"}}, ""},
		{"no dash", [][2]string{{"oc_glm-5.2", "glm-5.2"}, {"oc_kimi-k3", "kimi-k3"}}, ""},
		{"no model", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, s, tenant := importFixture(t, "https://opencode.ai/inference/go/v1", "sk", tc.declared...)
			h.SetCatalog(catalog.NewStaticSource(testCatalogRelease(t)))
			rr := httptest.NewRecorder()
			h.getCatalogImportPage(rr, importRequest(t, s, tenant, http.MethodGet, nil))
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
			}
			if got := hiddenOrInputValue(t, rr.Body.String(), "proxy_prefix"); got != tc.want {
				t.Errorf("prefix = %q, want %q", got, tc.want)
			}
		})
	}
}

// hiddenOrInputValue reads the value of the first field name of page, empty
// when the field carries none.
func hiddenOrInputValue(t *testing.T, page, name string) string {
	t.Helper()
	i := strings.Index(page, `name="`+name+`"`)
	if i < 0 {
		t.Fatalf("no %s field in the page", name)
	}
	tag := page[i:]
	tag = tag[:strings.Index(tag, ">")]
	j := strings.Index(tag, `value="`)
	if j < 0 {
		return ""
	}
	v := tag[j+len(`value="`):]
	return stdhtml.UnescapeString(v[:strings.Index(v, `"`)])
}
