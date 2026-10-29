package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// memoryProviderStore is the part of port.ProviderStore an import writes
// through, kept in memory.
type memoryProviderStore struct {
	port.ProviderStore
	providers map[model.ProviderID]model.Provider
	models    map[model.LLMModelID]model.LLMModel
	writes    int
	// refuse, when set, fails the writes of the models it names.
	refuse map[string]bool
}

func newMemoryProviderStore() *memoryProviderStore {
	return &memoryProviderStore{providers: map[model.ProviderID]model.Provider{}, models: map[model.LLMModelID]model.LLMModel{}}
}

func (s *memoryProviderStore) CreateProvider(_ context.Context, p model.Provider) error {
	s.writes++
	s.providers[p.ID()] = p
	return nil
}

func (s *memoryProviderStore) SaveProvider(_ context.Context, p model.Provider) error {
	s.writes++
	s.providers[p.ID()] = p
	return nil
}

func (s *memoryProviderStore) CreateLLMModel(_ context.Context, m model.LLMModel) error {
	if s.refuse[m.ProxyName()] {
		return errors.New("refused")
	}
	s.writes++
	s.models[m.ID()] = m
	return nil
}

func (s *memoryProviderStore) SaveLLMModel(_ context.Context, m model.LLMModel) error {
	s.writes++
	s.models[m.ID()] = m
	return nil
}

func (s *memoryProviderStore) modelsOf(orgID model.OrgID) []model.LLMModel {
	var models []model.LLMModel
	for _, m := range s.models {
		if m.OrgID() == orgID {
			models = append(models, m)
		}
	}
	return models
}

// takenNames is a ProxyNames over a set of names.
type takenNames map[string]bool

func (n takenNames) Conflict(name string) string {
	if n[name] {
		return "taken"
	}
	return ""
}

func (n takenNames) Declare(name string) { n[name] = true }

func (n takenNames) Release(name string) { delete(n, name) }

func namesOf(models []model.LLMModel) takenNames {
	n := takenNames{}
	for _, m := range models {
		n[m.ProxyName()] = true
	}
	return n
}

// configuredProvider is a provider with every exported setting and two
// models, one of them disabled.
func configuredProvider(t *testing.T, store *memoryProviderStore, orgID model.OrgID) model.Provider {
	t.Helper()
	p := model.NewProvider(orgID, "OpenCode Go", "openai", "https://opencode.ai/zen/go/v1", "encrypted-secret", "USD")
	p.SetCloudTier(2)
	p.SetRetryConfig(&model.RetryConfig{Enabled: true, MaxAttempts: 3, Delay: time.Second})
	p.SetRequestHeaders(&model.ProviderRequestHeaders{Headers: map[string]string{"X-Title": "xolo"}})
	budget := int64(1000)
	p.SetBillingMode(model.BillingModeSubscription)
	p.SetSubscriptionPlan(&model.SubscriptionPlan{Label: "Go", Constraints: []model.PlanConstraint{{Kind: model.ConstraintRollingWindow, Label: "5h", Duration: model.PlanDuration(5 * time.Hour), TokenBudget: &budget}}})
	if err := store.CreateProvider(context.Background(), p); err != nil {
		t.Fatal(err)
	}

	glm := model.NewLLMModel(p.ID(), orgID, "glm-5", "glm-5", "GLM 5", 600, 2200)
	glm.SetCachedPromptCostPer1KTokens(110)
	glm.SetContextWindow(200_000)
	glm.SetOutputWindow(32_000)
	glm.SetActiveParams(32_000_000_000)
	glm.SetCapabilities(model.ModelCapabilities{Tools: true, Reasoning: true})
	glm.SetAPIType("anthropic")
	glm.SetBaseURL("https://opencode.ai/zen/go")
	glm.SetExtraBody(map[string]any{"reasoning_split": true})
	glm.SetTokenLimitConfig(&model.TokenLimitConfig{Enabled: true, MaxTokens: 1000, Interval: time.Minute})
	kimi := model.NewLLMModel(p.ID(), orgID, "kimi-k3", "kimi-k3", "", 300, 1200)
	kimi.SetEnabled(false)
	for _, m := range []model.LLMModel{glm, kimi} {
		if err := store.CreateLLMModel(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// selectAll selects every change of plan, the billing included.
func selectAll(plan ProviderBundlePlan) BundleSelection {
	sel := BundleSelection{Settings: map[string]bool{}, Billing: true, Models: map[int]bool{}, Fields: map[int]map[string]bool{}}
	for _, c := range plan.Settings {
		sel.Settings[c.Field] = true
	}
	for _, m := range plan.Models {
		sel.Models[m.Index] = true
		sel.Fields[m.Index] = map[string]bool{}
		for _, c := range m.Changes {
			sel.Fields[m.Index][c.Field] = true
		}
	}
	return sel
}

func TestProviderBundle_ExportHoldsNoSecretNorInternalID(t *testing.T) {
	store := newMemoryProviderStore()
	p := configuredProvider(t, store, "org-a")
	models := store.modelsOf("org-a")

	data, err := json.Marshal(model.NewProviderBundle(p, models, false, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	if strings.Contains(doc, "encrypted-secret") {
		t.Error("the export carries the API key")
	}
	for _, id := range []string{string(p.ID()), string(models[0].ID()), string(models[1].ID()), "org-a"} {
		if strings.Contains(doc, id) {
			t.Errorf("the export carries the internal ID %q", id)
		}
	}

	enabled := model.NewProviderBundle(p, models, true, time.Now())
	if len(enabled.Models) != 1 || enabled.Models[0].RealModel != "glm-5" {
		t.Errorf("enabled-only export = %+v, want the enabled model only", enabled.Models)
	}
}

func TestProviderBundle_RoundTripsIntoANewProviderThenChangesNothing(t *testing.T) {
	store := newMemoryProviderStore()
	source := configuredProvider(t, store, "org-a")
	data, err := json.Marshal(model.NewProviderBundle(source, store.modelsOf("org-a"), false, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := model.ParseProviderBundle(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	plan := PlanProviderBundleImport(bundle, nil, nil, takenNames{})
	if plan.Billing == nil {
		t.Fatal("the subscription plan should be offered apart")
	}
	result, err := ApplyProviderBundle(context.Background(), store, plan, selectAll(plan), &NewProviderTarget{OrgID: "org-b", Name: "OpenCode", APIKey: "other-key"})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.Created != 2 {
		t.Fatalf("created %d models, want 2", result.Created)
	}

	// The models of org-b now describe the same configuration as org-a's.
	imported := store.modelsOf("org-b")
	want := model.NewProviderBundle(source, store.modelsOf("org-a"), false, time.Time{})
	got := model.NewProviderBundle(result.Provider, imported, false, time.Time{})
	got.Provider.Name = want.Provider.Name
	if a, b := mustJSON(t, want.Models), mustJSON(t, sortedLike(want.Models, got.Models)); a != b {
		t.Errorf("imported models differ:\nwant %s\n got %s", a, b)
	}
	if a, b := mustJSON(t, want.Provider), mustJSON(t, got.Provider); a != b {
		t.Errorf("imported provider differs:\nwant %s\n got %s", a, b)
	}

	// The same bundle, imported again, changes nothing.
	again := PlanProviderBundleImport(bundle, result.Provider, imported, namesOf(imported))
	if again.Changed() {
		t.Errorf("second import plans changes: settings=%+v billing=%+v models=%+v", again.Settings, again.Billing, again.Models)
	}
	writes := store.writes
	if _, err := ApplyProviderBundle(context.Background(), store, again, selectAll(again), nil); err != nil {
		t.Fatal(err)
	}
	if store.writes != writes {
		t.Errorf("second import wrote %d times, want none", store.writes-writes)
	}
}

func TestPlanProviderBundleImport_ListsTheChangedFieldsAndWritesOnlyTheSelected(t *testing.T) {
	store := newMemoryProviderStore()
	p := configuredProvider(t, store, "org-a")
	models := store.modelsOf("org-a")

	bundle := model.NewProviderBundle(p, models, false, time.Now())
	for i := range bundle.Models {
		if bundle.Models[i].RealModel == "glm-5" {
			bundle.Models[i].Prices.Output = 2.5
			bundle.Models[i].ContextWindow = 256_000
			bundle.Models[i].Description = "GLM 5 (Go)"
		}
	}
	bundle.Models = append(bundle.Models, model.ProviderBundleModel{RealModel: "qwen3.8", ProxyName: "qwen3.8", Enabled: true})
	bundle.Models = append(bundle.Models, model.ProviderBundleModel{RealModel: "taken", ProxyName: "kimi-k3", Enabled: true})

	writes := store.writes
	plan := PlanProviderBundleImport(bundle, p, models, namesOf(models))
	if store.writes != writes {
		t.Fatal("planning wrote to the store")
	}

	byModel := map[string]BundleModelPlan{}
	for _, m := range plan.Models {
		byModel[m.Model.RealModel] = m
	}
	glm := byModel["glm-5"]
	if glm.Action != BundleModelUpdate {
		t.Fatalf("glm-5 action = %s, want update", glm.Action)
	}
	var fields []string
	for _, c := range glm.Changes {
		fields = append(fields, c.Field)
	}
	if got := strings.Join(fields, ","); got != "description,prices,windows" {
		t.Errorf("glm-5 changes = %s, want description,prices,windows", got)
	}
	if byModel["kimi-k3"].Action != BundleModelUnchanged {
		t.Errorf("kimi-k3 action = %s, want unchanged", byModel["kimi-k3"].Action)
	}
	if byModel["qwen3.8"].Action != BundleModelCreate {
		t.Errorf("qwen3.8 action = %s, want create", byModel["qwen3.8"].Action)
	}
	if m := byModel["taken"]; m.Action != BundleModelSkip || m.Conflict == "" {
		t.Errorf("taken = %s %q, want skipped on its proxy name", m.Action, m.Conflict)
	}
	if len(plan.Settings) != 0 || plan.Billing != nil {
		t.Errorf("settings = %+v billing = %+v, want none on the provider it came from", plan.Settings, plan.Billing)
	}

	// Only the prices of glm-5 are selected: its description stays.
	sel := BundleSelection{Fields: map[int]map[string]bool{glm.Index: {BundleFieldPrices: true}}}
	result, err := ApplyProviderBundle(context.Background(), store, plan, sel, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated != 1 || result.Created != 0 {
		t.Errorf("result = %+v, want one update only", result)
	}
	updated := store.models[glm.Existing.ID()]
	if updated.CompletionCostPer1KTokens() != 2500 || updated.Description() != "GLM 5" || updated.ContextWindow() != 200_000 {
		t.Errorf("updated glm-5 = cost %d, %q, window %d; want only the price changed",
			updated.CompletionCostPer1KTokens(), updated.Description(), updated.ContextWindow())
	}
	if updated.CreatedAt() != glm.Existing.CreatedAt() || updated.ProxyName() != "glm-5" {
		t.Error("the update lost the model's identity")
	}
}

func TestPlanProviderBundleImport_LeavesTheBillingToTheOperator(t *testing.T) {
	store := newMemoryProviderStore()
	source := configuredProvider(t, store, "org-a")
	bundle := model.NewProviderBundle(source, nil, false, time.Now())

	target := model.NewProvider("org-b", "OpenCode", "openai", "https://opencode.ai/zen/go/v1", "key", "USD")
	plan := PlanProviderBundleImport(bundle, target, nil, takenNames{})
	if plan.Billing == nil {
		t.Fatal("the subscription plan change should be listed")
	}

	sel := selectAll(plan)
	sel.Billing = false
	result, err := ApplyProviderBundle(context.Background(), store, plan, sel, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider.SubscriptionPlan() != nil || result.Provider.BillingMode() != model.BillingModePayg {
		t.Error("the subscription plan was applied without being selected")
	}
	if result.Provider.RetryConfig() == nil || result.Provider.ID() != target.ID() || result.Provider.APIKey() != "key" {
		t.Error("the selected settings should apply to the target, keeping its identity and key")
	}
}

func TestPlanProviderBundleImport_MatchesARenamedModelByItsRealModel(t *testing.T) {
	store := newMemoryProviderStore()
	p := configuredProvider(t, store, "org-a")
	models := store.modelsOf("org-a")

	bundle := model.NewProviderBundle(p, models, false, time.Now())
	for i := range bundle.Models {
		if bundle.Models[i].RealModel == "kimi-k3" {
			bundle.Models[i].ProxyName = "go/kimi-k3"
		}
	}
	plan := PlanProviderBundleImport(bundle, p, models, namesOf(models))
	for _, m := range plan.Models {
		if m.Model.RealModel != "kimi-k3" {
			continue
		}
		if m.Action != BundleModelUpdate || len(m.Changes) != 1 || m.Changes[0].Field != BundleFieldProxyName || m.Changes[0].Conflict != "" {
			t.Errorf("kimi-k3 plan = %s %+v, want a rename of the existing model", m.Action, m.Changes)
		}
	}
}

func TestParseProviderBundle_RefusesAnotherKindOrVersion(t *testing.T) {
	for _, doc := range []string{
		`{"kind":"xolo.pipeline","version":"1","provider":{"type":"openai"}}`,
		`{"kind":"xolo.provider","version":"2","provider":{"type":"openai"}}`,
		`{"kind":"xolo.provider","version":"1","provider":{"type":"nope"}}`,
		`{"kind":"xolo.provider","version":"1","provider":{"type":"openai"},"models":[{"real_model":"a"}]}`,
		`not json`,
	} {
		if _, err := model.ParseProviderBundle([]byte(doc)); err == nil {
			t.Errorf("%s: parsed, want refused", doc)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// sortedLike orders got by the real models of want.
func sortedLike(want, got []model.ProviderBundleModel) []model.ProviderBundleModel {
	sorted := make([]model.ProviderBundleModel, 0, len(got))
	for _, w := range want {
		for _, g := range got {
			if g.RealModel == w.RealModel {
				sorted = append(sorted, g)
			}
		}
	}
	return sorted
}

func TestPlanProviderBundleImport_SkipsAKindTheTargetDoesNotServe(t *testing.T) {
	// A bundle of an openai provider, imported into a typesafe one: a
	// completion model without an API of its own would be served by the
	// target's API, which serves decisions only.
	bundle := model.ProviderBundle{Kind: model.ProviderBundleKind, Version: model.ProviderBundleVersion,
		Provider: model.ProviderBundleSettings{Type: "openai"},
		Models:   []model.ProviderBundleModel{{RealModel: "gpt", ProxyName: "gpt", Kind: model.ModelKindCompletion}, {RealModel: "gpt-2", ProxyName: "gpt-2", APIType: "openai"}}}
	target := model.NewProvider("org-a", "TypeSafe", "typesafe", "", "key", "USD")

	plan := PlanProviderBundleImport(bundle, target, nil, takenNames{})
	if m := plan.Models[0]; m.Action != BundleModelSkip || m.KindNotServed != "typesafe" {
		t.Errorf("gpt = %s (%q), want skipped as typesafe serves no completion", m.Action, m.KindNotServed)
	}
	if m := plan.Models[1]; m.Action != BundleModelCreate || m.Model.Kind != model.ModelKindCompletion {
		t.Errorf("gpt-2 = %s %q, want created as a completion served by its own openai API", m.Action, m.Model.Kind)
	}
}

func TestPlanProviderBundleImport_FreesTheNameARenamedModelLeaves(t *testing.T) {
	store := newMemoryProviderStore()
	p := configuredProvider(t, store, "org-a")
	models := store.modelsOf("org-a")

	// kimi-k3 moves to go/kimi-k3, and a new model takes its former name,
	// listed first in the bundle.
	bundle := model.NewProviderBundle(p, models, false, time.Now())
	for i := range bundle.Models {
		if bundle.Models[i].RealModel == "kimi-k3" {
			bundle.Models[i].ProxyName = "go/kimi-k3"
		}
	}
	bundle.Models = append([]model.ProviderBundleModel{{RealModel: "kimi-k3.1", ProxyName: "kimi-k3", Enabled: true}}, bundle.Models...)

	plan := PlanProviderBundleImport(bundle, p, models, namesOf(models))
	if m := plan.Models[0]; m.Action != BundleModelCreate {
		t.Errorf("kimi-k3.1 = %s %q, want created under the name kimi-k3 leaves", m.Action, m.Conflict)
	}
}

func TestApplyProviderBundle_GoesOnPastAModelTheStoreRefuses(t *testing.T) {
	store := newMemoryProviderStore()
	bundle := model.ProviderBundle{Kind: model.ProviderBundleKind, Version: model.ProviderBundleVersion,
		Provider: model.ProviderBundleSettings{Type: "openai"},
		Models:   []model.ProviderBundleModel{{RealModel: "a", ProxyName: "a"}, {RealModel: "b", ProxyName: "b"}}}
	plan := PlanProviderBundleImport(bundle, nil, nil, takenNames{})
	store.refuse = map[string]bool{"a": true}

	result, err := ApplyProviderBundle(context.Background(), store, plan, selectAll(plan), &NewProviderTarget{OrgID: "org-a", Name: "P", APIKey: "k"})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.Created != 1 || len(result.Failed) != 1 || result.Failed[0].ProxyName != "a" {
		t.Errorf("result = created %d, failed %+v; want b created and a reported", result.Created, result.Failed)
	}
}

func TestParseProviderBundle_RefusesReservedHeadersAndAbsurdPrices(t *testing.T) {
	for name, doc := range map[string]string{
		"reserved header": `{"kind":"xolo.provider","version":"1","provider":{"type":"openai","request_headers":{"headers":{"Authorization":"Bearer x"}}}}`,
		"absurd price":    `{"kind":"xolo.provider","version":"1","provider":{"type":"openai"},"models":[{"real_model":"a","proxy_name":"a","prices":{"input":1e300,"output":1}}]}`,
	} {
		if _, err := model.ParseProviderBundle([]byte(doc)); err == nil {
			t.Errorf("%s: parsed, want refused", name)
		}
	}
}

func TestPlanProviderBundleImport_ShowsAbsentSettingsAsEmpty(t *testing.T) {
	target := model.NewProvider("org-a", "P", "openai", "", "key", "USD")
	bundle := model.ProviderBundle{Kind: model.ProviderBundleKind, Version: model.ProviderBundleVersion,
		Provider: model.ProviderBundleSettings{Type: "openai", Retry: &model.RetryConfig{Enabled: true, MaxAttempts: 2}}}
	plan := PlanProviderBundleImport(bundle, target, nil, takenNames{})
	for _, c := range plan.Settings {
		if c.From == "null" || c.To == "null" {
			t.Errorf("%s shows %q → %q, want an absent value empty", c.Field, c.From, c.To)
		}
	}
}
