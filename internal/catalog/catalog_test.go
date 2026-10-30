package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// testdataCatalog is a release of the catalog kept for the tests: Xolo
// embeds none.
func testdataCatalog(t *testing.T) *Catalog {
	t.Helper()
	data, err := os.ReadFile("testdata/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRelease_ConvertsEveryProviderIntoAValidBundle(t *testing.T) {
	c := testdataCatalog(t)
	if len(c.Providers) == 0 || len(c.Models) == 0 {
		t.Fatal("the test release is empty")
	}
	for _, p := range c.Providers {
		plans := []string{""}
		for _, plan := range p.Plans {
			plans = append(plans, plan.ID)
		}
		for _, planID := range plans {
			bundle, err := c.Bundle(p.ID, planID)
			if err != nil {
				t.Fatalf("%s/%s: %v", p.ID, planID, err)
			}
			if err := bundle.Validate(); err != nil {
				t.Errorf("%s/%s: the bundle does not validate: %v", p.ID, planID, err)
			}
			if len(bundle.Models) != len(p.Offerings) {
				t.Errorf("%s: %d models for %d offerings", p.ID, len(bundle.Models), len(p.Offerings))
			}
		}
	}
}

func TestBundle_TakesTheOfferingAndTheModelFigures(t *testing.T) {
	c := testCatalog()
	bundle, err := c.Bundle("acme", "pro")
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Models) != 2 {
		t.Fatalf("%d models, want 2", len(bundle.Models))
	}
	glm := bundle.Models[0]
	if glm.RealModel != "glm-5" || glm.Prices.Input != 0.6 || glm.Prices.CachedInput != 0.11 || glm.Prices.Output != 2.2 {
		t.Errorf("glm-5 = %+v", glm)
	}
	if glm.APIType != "anthropic" || glm.BaseURL != "https://acme.test/anthropic" {
		t.Errorf("glm-5 API = %q %q, want its own API", glm.APIType, glm.BaseURL)
	}
	if glm.ContextWindow != 128_000 || glm.OutputWindow != 32_000 || glm.ActiveParams != 40_000_000_000 || !glm.Capabilities.Tools {
		t.Errorf("glm-5 figures = ctx %d out %d active %d caps %+v; want the offering's window over the model's", glm.ContextWindow, glm.OutputWindow, glm.ActiveParams, glm.Capabilities)
	}
	if kimi := bundle.Models[1]; kimi.APIType != "" || kimi.BaseURL != "" {
		t.Errorf("kimi = %q %q, want the provider's own API left implicit", kimi.APIType, kimi.BaseURL)
	}

	plan := bundle.Provider.SubscriptionPlan
	if bundle.Provider.BillingMode != model.BillingModeSubscription || plan == nil || len(plan.Constraints) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	pc := plan.Constraints[0]
	if pc.Duration.Duration() != 5*time.Hour || pc.Scope != model.PlanScopeModel {
		t.Errorf("constraint = %+v", pc)
	}
	if b := pc.ModelBudgets["glm-5"]; b.ValueBudget == nil || *b.ValueBudget != 12_000_000 {
		t.Errorf("glm-5 budget = %+v, want $12 in microcents", b)
	}
	if b, ok := pc.ModelBudgets["kimi"]; !ok || b.ValueBudget != nil || b.TokenBudget != nil {
		t.Errorf("kimi budget = %+v (%v), want listed with no budget: unlimited", b, ok)
	}
}

func TestLookup_PrefersTheOfferingOfTheSameProvider(t *testing.T) {
	c := testCatalog()
	pre, ok := c.Lookup("glm-5", "https://other.test/v1", "openai")
	if !ok || pre.Provider == nil || pre.Provider.ID != "other" {
		t.Fatalf("lookup = %+v, want the offering of the provider at that URL", pre)
	}
	pre, ok = c.Lookup("glm-5", "", "")
	if !ok || pre.Offering == nil || pre.Model == nil || pre.Model.ID != "glm-5" {
		t.Fatalf("lookup = %+v, want some offering of glm-5", pre)
	}
	if _, ok := c.Lookup("unknown", "", ""); ok {
		t.Error("an unknown model was found")
	}
}

func TestParse_RefusesAProviderWithoutCurrency(t *testing.T) {
	doc := `{"kind":"xolo.catalog","format":"1","version":"v1","models":[],"providers":[{"id":"p","name":"P","offerings":[]}]}`
	if _, err := Parse([]byte(doc)); err == nil {
		t.Error("a provider without currency was accepted")
	}
}

func TestParse_LeavesOutAProviderInAnUnsupportedCurrency(t *testing.T) {
	doc := `{"kind":"xolo.catalog","format":"1","version":"v1","models":[],"providers":[{"id":"p","name":"P","currency":"CNY","offerings":[]},{"id":"q","name":"Q","currency":"USD","offerings":[]}]}`
	c, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Providers) != 1 || c.Providers[0].ID != "q" {
		t.Errorf("providers = %+v, want the USD one only", c.Providers)
	}
}

func TestSource_IsUnavailableUntilAReleaseIsRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	s := NewSource(server.URL, time.Hour)
	s.Refresh(context.Background())
	if c, err := s.Catalog(context.Background()); c != nil || !errors.Is(err, ErrUnavailable) {
		t.Errorf("catalog = %v, %v; want unavailable, with no embedded copy to fall back on", c, err)
	}
}

func TestSource_ServesTheFetchedReleaseAndKeepsItWhenTheNextFetchFails(t *testing.T) {
	var up atomic.Bool
	up.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !up.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"kind":"xolo.catalog","format":"1","version":"v2026.11.01","models":[],"providers":[]}`))
	}))
	defer server.Close()
	s := NewSource(server.URL, 0)
	// A first call never waits for the network: the catalog is not read yet.
	if _, err := s.Catalog(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want unavailable before the first release is read", err)
	}
	s.Refresh(context.Background())
	if c, err := s.Catalog(context.Background()); err != nil || c.Version != "v2026.11.01" {
		t.Fatalf("catalog = %v, %v; want the fetched release", c, err)
	}
	up.Store(false)
	s.Refresh(context.Background())
	if c, err := s.Catalog(context.Background()); err != nil || c.Version != "v2026.11.01" {
		t.Errorf("catalog = %v, %v; want the last release kept", c, err)
	}
}

func testCatalog() *Catalog {
	budget := 12.0
	return &Catalog{
		Kind: Kind, Format: Format, Version: "test",
		Models: []Model{
			{ID: "glm-5", Name: "GLM 5", Vendor: "Z.ai",
				ContextWindow: &Count{Value: 200_000}, OutputWindow: &Count{Value: 32_000},
				Capabilities: &Capabilities{Tools: true},
				Params:       &Params{Active: &Count{Value: 40_000_000_000}}},
			{ID: "kimi", Name: "Kimi", Vendor: "Moonshot"},
		},
		Providers: []Provider{
			func() Provider {
				p := Provider{ID: "acme", Name: "Acme", Currency: "USD",
					Plans: []Plan{{ID: "pro", Name: "Pro", Constraints: []Constraint{{
						Kind: "rolling_window", Label: "5h", Duration: "5h", Scope: "model",
						ModelBudgets: map[string]Budget{"glm-5": {ValueBudget: &budget}, "kimi": {}},
					}}}},
					Offerings: []Offering{
						{Model: "glm-5", ModelID: "glm-5", APIType: "anthropic", BaseURL: "https://acme.test/anthropic",
							ContextWindow: &Count{Value: 128_000}, Prices: Prices{Input: 0.6, CachedInput: 0.11, Output: 2.2}},
						{Model: "kimi", ModelID: "kimi", APIType: "openai", BaseURL: "https://acme.test/v1", Prices: Prices{Input: 1, Output: 2}},
					}}
				p.Xolo.Type, p.Xolo.BaseURL = "openai", "https://acme.test/v1"
				return p
			}(),
			func() Provider {
				p := Provider{ID: "other", Name: "Other", Currency: "USD",
					Offerings: []Offering{{Model: "glm-5", ModelID: "glm-5", Prices: Prices{Input: 1, Output: 3}}}}
				p.Xolo.Type, p.Xolo.BaseURL = "openai", "https://other.test/v1"
				return p
			}(),
		},
	}
}

func TestBundle_LeavesOutAnOfferingItCannotReach(t *testing.T) {
	c := testCatalog()
	p := c.Provider("acme")
	p.Offerings = append(p.Offerings, Offering{Model: "kimi", ModelID: "kimi-anthropic", APIType: "anthropic", Prices: Prices{Input: 1, Output: 2}})
	bundle, err := c.Bundle("acme", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range bundle.Models {
		if m.RealModel == "kimi-anthropic" {
			t.Error("an offering through another API without its base URL was imported at the provider's endpoint")
		}
	}
}

func TestBundle_KeysPlanBudgetsByTheProvidersModelID(t *testing.T) {
	// The catalog keys budgets by the offering's model_id, which Xolo's
	// plans match against the model's real model.
	c := testCatalog()
	p := c.Provider("acme")
	p.Offerings = append(p.Offerings, Offering{Model: "kimi", ModelID: "kimi-free", Prices: Prices{}})
	p.Plans[0].Constraints[0].ModelBudgets["kimi-free"] = Budget{}
	bundle, err := c.Bundle("acme", "pro")
	if err != nil {
		t.Fatal(err)
	}
	constraint := bundle.Provider.SubscriptionPlan.Constraints[0]
	for _, m := range bundle.Models {
		if m.RealModel != "kimi-free" {
			continue
		}
		if _, ok := constraint.ModelBudgets[m.RealModel]; !ok {
			t.Errorf("no budget for %q, the real model the enforcer looks up", m.RealModel)
		}
	}
}

func TestPlan_ConvertsASharedWindow(t *testing.T) {
	budget, model60 := 60.0, 120.0
	plan := Plan{ID: "go", Name: "Go", Constraints: []Constraint{{
		Kind: "rolling_window", Label: "month", Duration: "720h", Scope: "shared", ValueBudget: &budget,
		ModelBudgets: map[string]Budget{"glm": {ValueBudget: &model60}, "free": {}},
	}}}
	sp, err := plan.SubscriptionPlan()
	if err != nil {
		t.Fatal(err)
	}
	c := sp.Constraints[0]
	if !c.Shared() || *c.ValueBudget != 60_000_000 || *c.ModelBudgets["glm"].ValueBudget != 120_000_000 {
		t.Errorf("constraint = %+v, want a shared 60$ window weighing glm at 120$", c)
	}
}

func TestSource_RetriesSoonWhileNoReleaseWasRead(t *testing.T) {
	s := NewSource("http://127.0.0.1:1", 24*time.Hour)
	s.Refresh(context.Background())
	s.mu.Lock()
	s.fetchedAt = time.Now().Add(-2 * unavailableRetry)
	s.mu.Unlock()
	_, _ = s.Catalog(context.Background())
	s.mu.Lock()
	started := s.inflight != nil
	s.mu.Unlock()
	if !started {
		t.Error("no new fetch a minute after a failed one, want a retry well before the refresh interval")
	}
	s.Refresh(context.Background())
}

func TestProviderAt_RecognizesAnOfferingsOwnEndpoint(t *testing.T) {
	p := Provider{ID: "acme", Offerings: []Offering{{Model: "haiku", ModelID: "haiku", APIType: "anthropic", BaseURL: "https://acme.test/anthropic/v1"}}}
	p.Xolo.Type, p.Xolo.BaseURL = "openai", "https://acme.test/v1"
	c := &Catalog{Providers: []Provider{p}}
	for _, baseURL := range []string{"https://acme.test/v1/", "https://acme.test/anthropic/v1"} {
		if got := c.ProviderAt(baseURL); got == nil || got.ID != "acme" {
			t.Errorf("%s: provider = %v, want acme", baseURL, got)
		}
	}
	if got := c.ProviderAt("https://elsewhere.test/v1"); got != nil {
		t.Errorf("provider = %s, want none", got.ID)
	}
}

func TestBundle_RefusesAModelBudgetThatNamesNoOffering(t *testing.T) {
	c := testCatalog()
	// Keyed by the catalog model rather than the offering's model_id.
	c.Providers[0].Plans[0].Constraints[0].ModelBudgets["GLM 5"] = Budget{}
	if _, err := c.Bundle("acme", "pro"); !errors.Is(err, ErrInvalid) {
		t.Errorf("err = %v, want the release refused rather than a budget that applies to no model", err)
	}
	if _, err := c.Bundle("acme", ""); err != nil {
		t.Errorf("without the plan: %v", err)
	}
}

func TestSource_NeverRefreshesMoreThanOnceAMinute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"kind":"xolo.catalog","format":"1","version":"v1","models":[],"providers":[]}`))
	}))
	defer server.Close()
	s := NewSource(server.URL, 0)
	s.Refresh(context.Background())
	if _, err := s.Catalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight != nil {
		t.Error("a zero interval started another fetch right after the last one")
	}
}

func TestBundle_DropsTheModelBudgetOfAnOfferingItLeavesOut(t *testing.T) {
	c := testCatalog()
	acme := &c.Providers[0]
	acme.Offerings = append(acme.Offerings, Offering{Model: "kimi", ModelID: "kimi-far", APIType: "gemini", Prices: Prices{Input: 1, Output: 2}})
	acme.Plans[0].Constraints[0].ModelBudgets["kimi-far"] = Budget{}
	bundle, err := c.Bundle("acme", "pro")
	if err != nil {
		t.Fatalf("err = %v, want the plan imported: kimi-far is an offering, left out as unreachable", err)
	}
	if _, kept := bundle.Provider.SubscriptionPlan.Constraints[0].ModelBudgets["kimi-far"]; kept {
		t.Error("the budget of kimi-far, which the bundle leaves out, should go")
	}
	if _, kept := acme.Plans[0].Constraints[0].ModelBudgets["kimi-far"]; !kept {
		t.Error("the catalog's own plan should be left untouched")
	}
}

func TestBundle_KeepsTheSharedBudgetOfAnOfferingItLeavesOut(t *testing.T) {
	c := testCatalog()
	acme := &c.Providers[0]
	acme.Offerings = append(acme.Offerings, Offering{Model: "kimi", ModelID: "kimi-far", APIType: "gemini", Prices: Prices{Input: 1, Output: 2}})
	tokens := int64(1000)
	acme.Plans = append(acme.Plans, Plan{ID: "shared", Name: "Shared", Constraints: []Constraint{{
		Kind: "rolling_window", Label: "5h", Duration: "5h", Scope: "shared", TokenBudget: &tokens,
		ModelBudgets: map[string]Budget{"glm-5": {TokenBudget: &tokens}, "kimi-far": {TokenBudget: &tokens}},
	}}})
	bundle, err := c.Bundle("acme", "shared")
	if err != nil {
		t.Fatal(err)
	}
	if _, kept := bundle.Provider.SubscriptionPlan.Constraints[0].ModelBudgets["kimi-far"]; !kept {
		t.Error("a shared constraint keeps the budget: its smallest one weighs the models it does not list")
	}
	if err := bundle.Validate(); err != nil {
		t.Errorf("the bundle does not validate: %v", err)
	}
}

func TestPrices_VariesOnlyWithPublishedTiersOrPeak(t *testing.T) {
	for raw, want := range map[string]bool{"": false, "null": false, "[]": false, "{}": false, "[ ]": false, "{ }": false, `[{"above":200000,"input":2}]`: true} {
		if got := (Prices{Tiers: json.RawMessage(raw)}).Varies(); got != want {
			t.Errorf("tiers %q: varies = %v, want %v", raw, got, want)
		}
	}
}
