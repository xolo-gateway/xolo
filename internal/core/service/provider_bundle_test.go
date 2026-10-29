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
		`{"kind":"xolo.provider","version":"1","provider":{"type":"openai","currency":"USD"},"models":[{"real_model":"a"}]}`,
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
		Provider: model.ProviderBundleSettings{Type: "openai", Currency: "USD"},
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
		Provider: model.ProviderBundleSettings{Type: "openai", Currency: "USD"},
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
		"reserved header":   `{"kind":"xolo.provider","version":"1","provider":{"type":"openai","currency":"USD","request_headers":{"headers":{"Authorization":"Bearer x"}}}}`,
		"billing mode":      `{"kind":"xolo.provider","version":"1","provider":{"type":"openai","currency":"USD","billing_mode":"free"}}`,
		"currency":          `{"kind":"xolo.provider","version":"1","provider":{"type":"openai","currency":"XXX"}}`,
		"no currency":       `{"kind":"xolo.provider","version":"1","provider":{"type":"openai"}}`,
		"scope":             `{"kind":"xolo.provider","version":"1","provider":{"type":"openai","currency":"USD","subscription_plan":{"label":"P","constraints":[{"kind":"rolling_window","label":"5h","duration":"5h","scope":"modle"}]}}}`,
		"model concurrency": `{"kind":"xolo.provider","version":"1","provider":{"type":"openai","currency":"USD","subscription_plan":{"label":"P","constraints":[{"kind":"concurrency","label":"c","max_concurrent":2,"scope":"model"}]}}}`,
		"cloud tier":        `{"kind":"xolo.provider","version":"1","provider":{"type":"openai","currency":"USD","cloud_tier":7}}`,
		"nan-free ratio":    `{"kind":"xolo.provider","version":"1","provider":{"type":"openai","currency":"USD","subscription_plan":{"label":"P","constraints":[{"kind":"rolling_window","label":"5h","duration":"5h","reserve_ratio":2}]}}}`,
		"no duration":       `{"kind":"xolo.provider","version":"1","provider":{"type":"openai","currency":"USD","subscription_plan":{"label":"P","constraints":[{"kind":"rolling_window","label":"5h"}]}}}`,
		"zero concurrency":  `{"kind":"xolo.provider","version":"1","provider":{"type":"openai","currency":"USD","subscription_plan":{"label":"P","constraints":[{"kind":"concurrency","label":"c","max_concurrent":0}]}}}`,
		"zero retry delay":  `{"kind":"xolo.provider","version":"1","provider":{"type":"openai","currency":"USD","retry":{"enabled":true,"max_attempts":3,"delay":0}}}`,
		"zero rate burst":   `{"kind":"xolo.provider","version":"1","provider":{"type":"openai","currency":"USD","rate_limit":{"enabled":true,"interval":1000000000,"max_burst":0}}}`,
		"zero token limit":  `{"kind":"xolo.provider","version":"1","provider":{"type":"openai","currency":"USD"},"models":[{"real_model":"a","proxy_name":"a","token_limit":{"enabled":true,"max_tokens":10,"interval":0},"prices":{"input":1,"output":1}}]}`,
		"negative window":   `{"kind":"xolo.provider","version":"1","provider":{"type":"openai","currency":"USD"},"models":[{"real_model":"a","proxy_name":"a","context_window":-1,"prices":{"input":1,"output":1}}]}`,
		"negative speed":    `{"kind":"xolo.provider","version":"1","provider":{"type":"openai","currency":"USD"},"models":[{"real_model":"a","proxy_name":"a","tokens_per_sec_low":-5,"prices":{"input":1,"output":1}}]}`,
		"absurd price":      `{"kind":"xolo.provider","version":"1","provider":{"type":"openai","currency":"USD"},"models":[{"real_model":"a","proxy_name":"a","prices":{"input":1e300,"output":1}}]}`,
	} {
		if _, err := model.ParseProviderBundle([]byte(doc)); err == nil {
			t.Errorf("%s: parsed, want refused", name)
		}
	}
}

func TestPlanProviderBundleImport_ShowsAbsentSettingsAsEmpty(t *testing.T) {
	target := model.NewProvider("org-a", "P", "openai", "", "key", "USD")
	bundle := model.ProviderBundle{Kind: model.ProviderBundleKind, Version: model.ProviderBundleVersion,
		Provider: model.ProviderBundleSettings{Type: "openai", Currency: "USD", Retry: &model.RetryConfig{Enabled: true, MaxAttempts: 2}}}
	plan := PlanProviderBundleImport(bundle, target, nil, takenNames{})
	for _, c := range plan.Settings {
		if c.From == "null" || c.To == "null" {
			t.Errorf("%s shows %q → %q, want an absent value empty", c.Field, c.From, c.To)
		}
	}
}

// uniqueNamesStore refuses a model whose proxy name another model of the
// organization holds, as the unique index on (org_id, proxy_name) does.
type uniqueNamesStore struct{ *memoryProviderStore }

func (s uniqueNamesStore) taken(m model.LLMModel) bool {
	for _, other := range s.models {
		if other.ID() != m.ID() && other.OrgID() == m.OrgID() && other.ProxyName() == m.ProxyName() {
			return true
		}
	}
	return false
}

func (s uniqueNamesStore) CreateLLMModel(ctx context.Context, m model.LLMModel) error {
	if s.taken(m) {
		return errors.New("unique index")
	}
	return s.memoryProviderStore.CreateLLMModel(ctx, m)
}

func (s uniqueNamesStore) SaveLLMModel(ctx context.Context, m model.LLMModel) error {
	if s.taken(m) {
		return errors.New("unique index")
	}
	return s.memoryProviderStore.SaveLLMModel(ctx, m)
}

func TestApplyProviderBundle_RenamesBeforeItCreatesUnderTheFreedName(t *testing.T) {
	mem := newMemoryProviderStore()
	p := configuredProvider(t, mem, "org-a")
	models := mem.modelsOf("org-a")

	bundle := model.NewProviderBundle(p, models, false, time.Now())
	for i := range bundle.Models {
		if bundle.Models[i].RealModel == "kimi-k3" {
			bundle.Models[i].ProxyName = "go/kimi-k3"
		}
	}
	bundle.Models = append([]model.ProviderBundleModel{{RealModel: "kimi-k3.1", ProxyName: "kimi-k3", Enabled: true}}, bundle.Models...)
	plan := PlanProviderBundleImport(bundle, p, models, namesOf(models))

	result, err := ApplyProviderBundle(context.Background(), uniqueNamesStore{mem}, plan, selectAll(plan), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failed) != 0 || result.Created != 1 || result.Updated != 1 {
		t.Errorf("result = created %d updated %d failed %+v; want the rename then the creation", result.Created, result.Updated, result.Failed)
	}
}

func TestPlanProviderBundleImportWith_ChecksKindsAgainstTheImportedType(t *testing.T) {
	bundle := model.ProviderBundle{Kind: model.ProviderBundleKind, Version: model.ProviderBundleVersion,
		Provider: model.ProviderBundleSettings{Type: "typesafe", Currency: "USD"},
		Models:   []model.ProviderBundleModel{{RealModel: "judge", ProxyName: "judge"}}}
	target := model.NewProvider("org-a", "P", "openai", "", "key", "USD")

	kept := PlanProviderBundleImport(bundle, target, nil, takenNames{})
	if m := kept.Models[0]; m.Model.Kind != model.ModelKindCompletion {
		t.Errorf("kind = %q, want the default of the openai type it keeps", m.Model.Kind)
	}
	changed := PlanProviderBundleImportWith(bundle, target, nil, takenNames{}, &BundleSelection{Settings: map[string]bool{BundleFieldType: true}})
	if m := changed.Models[0]; m.Action != BundleModelCreate || m.Model.Kind != model.ModelKindDecision {
		t.Errorf("kind = %s %q, want the default of the typesafe type it takes", m.Action, m.Model.Kind)
	}
}

func TestApplyProviderBundle_ReportsWhatTheSelectionMadeInapplicable(t *testing.T) {
	mem := newMemoryProviderStore()
	p := configuredProvider(t, mem, "org-a")
	models := mem.modelsOf("org-a")

	bundle := model.NewProviderBundle(p, models, false, time.Now())
	for i := range bundle.Models {
		if bundle.Models[i].RealModel == "kimi-k3" {
			bundle.Models[i].ProxyName = "go/kimi-k3"
		}
	}
	bundle.Models = append([]model.ProviderBundleModel{{RealModel: "kimi-k3.1", ProxyName: "kimi-k3", Enabled: true}}, bundle.Models...)

	// The operator unchecks the rename but keeps the model taking its name.
	sel := BundleSelection{Models: map[int]bool{0: true}, Fields: map[int]map[string]bool{}}
	plan := PlanProviderBundleImportWith(bundle, p, models, namesOf(models), &sel)
	result, err := ApplyProviderBundle(context.Background(), uniqueNamesStore{mem}, plan, sel, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 0 || len(result.Failed) != 0 || len(result.Skipped) != 1 || result.Skipped[0].Conflict == "" {
		t.Errorf("result = created %d failed %+v skipped %+v; want the creation reported as blocked by the kept name", result.Created, result.Failed, result.Skipped)
	}
}

func TestApplyProviderBundle_ReportsASelectedRenameItCannotApply(t *testing.T) {
	mem := newMemoryProviderStore()
	p := configuredProvider(t, mem, "org-a")
	models := mem.modelsOf("org-a")

	// kimi-k3 moves to the name glm-5 keeps: the operator checked the rename.
	bundle := model.NewProviderBundle(p, models, false, time.Now())
	index := -1
	for i := range bundle.Models {
		if bundle.Models[i].RealModel == "kimi-k3" {
			bundle.Models[i].ProxyName = "glm-5"
			index = i
		}
	}
	sel := BundleSelection{Fields: map[int]map[string]bool{index: {BundleFieldProxyName: true}}}
	plan := PlanProviderBundleImportWith(bundle, p, models, namesOf(models), &sel)
	result, err := ApplyProviderBundle(context.Background(), mem, plan, sel, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skipped) != 1 || result.Skipped[0].Conflict == "" || result.Skipped[0].RefusedField != BundleFieldProxyName {
		t.Errorf("skipped = %+v, want the refused rename reported as that one change", result.Skipped)
	}
}

func TestApplyProviderBundle_WritesTheOtherChangesOfAModelWithARefusedRename(t *testing.T) {
	mem := newMemoryProviderStore()
	p := configuredProvider(t, mem, "org-a")
	models := mem.modelsOf("org-a")

	// kimi-k3 moves to the name glm-5 keeps and gets a description: the
	// rename is refused, the description written.
	bundle := model.NewProviderBundle(p, models, false, time.Now())
	index := -1
	for i := range bundle.Models {
		if bundle.Models[i].RealModel == "kimi-k3" {
			bundle.Models[i].ProxyName = "glm-5"
			bundle.Models[i].Description = "Kimi K3"
			index = i
		}
	}
	sel := BundleSelection{Fields: map[int]map[string]bool{index: {BundleFieldProxyName: true, BundleFieldDescription: true}}}
	plan := PlanProviderBundleImportWith(bundle, p, models, namesOf(models), &sel)
	result, err := ApplyProviderBundle(context.Background(), mem, plan, sel, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated != 1 || len(result.Skipped) != 1 || result.Skipped[0].RefusedField != BundleFieldProxyName {
		t.Fatalf("result = updated %d skipped %+v; want the description written and the rename reported", result.Updated, result.Skipped)
	}
	updated := mem.models[result.Skipped[0].Existing.ID()]
	if updated.Description() != "Kimi K3" || updated.ProxyName() != "kimi-k3" {
		t.Errorf("model = %q %q, want the new description under its kept name", updated.ProxyName(), updated.Description())
	}
}

func TestApplyProviderBundle_ValidatesTheProviderLikeEveryWritePath(t *testing.T) {
	// What the provider form and the provisioning API refuse, a bundle
	// cannot write either: here, model budgets on a provider-wide limit,
	// which the bundle's own validation leaves to the service.
	mem := newMemoryProviderStore()
	max := 2
	bundle := model.ProviderBundle{Kind: model.ProviderBundleKind, Version: model.ProviderBundleVersion,
		Provider: model.ProviderBundleSettings{Type: "openai", Currency: "USD", BillingMode: model.BillingModeSubscription,
			SubscriptionPlan: &model.SubscriptionPlan{Label: "P", Constraints: []model.PlanConstraint{{
				Kind: model.ConstraintConcurrency, Label: "c", MaxConcurrent: &max,
				ModelBudgets: map[string]model.PlanBudget{"a": {}},
			}}}}}
	plan := PlanProviderBundleImport(bundle, nil, nil, takenNames{})
	_, err := ApplyProviderBundle(context.Background(), mem, plan, selectAll(plan), &NewProviderTarget{OrgID: "org-a", Name: "P", APIKey: "k"})
	if !errors.Is(err, port.ErrInvalid) {
		t.Fatalf("err = %v, want the provider refused as invalid", err)
	}
	if mem.writes != 0 {
		t.Errorf("%d writes, want none before the provider is valid", mem.writes)
	}
}

func TestNewProviderBundle_LeavesOutHeadersThatLookLikeCredentials(t *testing.T) {
	p := model.NewProvider("org-a", "Gateway", "openai", "https://gw.test/v1", "key", "USD")
	p.SetRequestHeaders(&model.ProviderRequestHeaders{Headers: map[string]string{
		"X-Title": "xolo", "Ocp-Apim-Subscription-Key": "secret-1", "X-Auth-Token": "secret-2",
	}, SessionHeader: "X-Session-Affinity"})
	data, err := json.Marshal(model.NewProviderBundle(p, nil, false, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	if strings.Contains(doc, "secret-1") || strings.Contains(doc, "secret-2") {
		t.Error("the export carries a credential header")
	}
	if !strings.Contains(doc, "X-Title") || !strings.Contains(doc, "X-Session-Affinity") {
		t.Error("the export should keep the plain headers and the session header name")
	}
	if !strings.Contains(doc, `"omitted_request_headers":["Ocp-Apim-Subscription-Key","X-Auth-Token"]`) {
		t.Errorf("the export should name the headers it left out: %s", doc)
	}
}

func TestApplyProviderBundle_KeepsTheTargetsCredentialHeaders(t *testing.T) {
	mem := newMemoryProviderStore()
	target := model.NewProvider("org-a", "Gateway", "openai", "https://gw.test/v1", "key", "USD")
	target.SetRequestHeaders(&model.ProviderRequestHeaders{Headers: map[string]string{"Api-Key": "secret", "X-Old": "1"}})
	bundle := model.ProviderBundle{Kind: model.ProviderBundleKind, Version: model.ProviderBundleVersion,
		Provider: model.ProviderBundleSettings{Type: "openai", Currency: "USD", BaseURL: "https://gw.test/v1",
			RequestHeaders: &model.ProviderRequestHeaders{Headers: map[string]string{"X-Title": "xolo"}}}}
	plan := PlanProviderBundleImport(bundle, target, nil, takenNames{})
	result, err := ApplyProviderBundle(context.Background(), mem, plan, BundleSelection{Settings: map[string]bool{BundleFieldRequestHeaders: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	headers := result.Provider.RequestHeaders().Headers
	if headers["Api-Key"] != "secret" || headers["X-Title"] != "xolo" || headers["X-Old"] != "" {
		t.Errorf("headers = %v, want the bundle's plus the target's credential header", headers)
	}
}

func TestApplyProviderBundle_HeadersConvergeOnASecondImport(t *testing.T) {
	mem := newMemoryProviderStore()
	target := model.NewProvider("org-a", "Gateway", "openai", "https://gw.test/v1", "key", "USD")
	target.SetRequestHeaders(&model.ProviderRequestHeaders{Headers: map[string]string{"Api-Key": "secret", "X-Old": "1"}})
	bundle := model.ProviderBundle{Kind: model.ProviderBundleKind, Version: model.ProviderBundleVersion,
		Provider: model.ProviderBundleSettings{Type: "openai", Currency: "USD", BaseURL: "https://gw.test/v1",
			RequestHeaders: &model.ProviderRequestHeaders{Headers: map[string]string{"X-Title": "xolo"}}}}
	plan := PlanProviderBundleImport(bundle, target, nil, takenNames{})
	result, err := ApplyProviderBundle(context.Background(), mem, plan, BundleSelection{Settings: map[string]bool{BundleFieldRequestHeaders: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	again := PlanProviderBundleImport(bundle, result.Provider, nil, takenNames{})
	for _, c := range again.Settings {
		if c.Field == BundleFieldRequestHeaders {
			t.Errorf("second import still plans request headers: %q -> %q", c.From, c.To)
		}
	}
}

func TestParseProviderBundle_RefusesACredentialHeader(t *testing.T) {
	doc := `{"kind":"xolo.provider","version":"1","provider":{"type":"openai","currency":"USD","request_headers":{"headers":{"Api-Key":"x"}}}}`
	if _, err := model.ParseProviderBundle([]byte(doc)); err == nil {
		t.Error("a bundle carrying a credential header was accepted")
	}
}

// renameStore holds two models of one provider, a and b.
func renameFixture(t *testing.T) (*memoryProviderStore, model.Provider, model.LLMModel, model.LLMModel) {
	t.Helper()
	mem := newMemoryProviderStore()
	p := model.NewProvider("org-a", "P", "openai", "https://p.test/v1", "key", "USD")
	a := model.NewLLMModel(p.ID(), "org-a", "a", "model-a", "", 100, 200)
	b := model.NewLLMModel(p.ID(), "org-a", "b", "model-b", "", 100, 200)
	for _, m := range []model.LLMModel{a, b} {
		if err := mem.CreateLLMModel(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
	return mem, p, a, b
}

func TestApplyProviderBundle_RenamesAlongAChainWhateverTheBundleOrder(t *testing.T) {
	mem, p, a, b := renameFixture(t)
	models := mem.modelsOf("org-a")
	// b takes a's name, listed first; a moves to c.
	bundle := model.ProviderBundle{Kind: model.ProviderBundleKind, Version: model.ProviderBundleVersion,
		Provider: model.ProviderBundleSettings{Type: "openai", Currency: "USD", BaseURL: "https://p.test/v1"},
		Models: []model.ProviderBundleModel{
			{RealModel: "model-b", ProxyName: "a", Enabled: true, Prices: model.ModelPrices{Input: 0.1, Output: 0.2}},
			{RealModel: "model-a", ProxyName: "c", Enabled: true, Prices: model.ModelPrices{Input: 0.1, Output: 0.2}},
		}}
	plan := PlanProviderBundleImport(bundle, p, models, namesOf(models))
	for _, m := range plan.Models {
		for _, c := range m.Changes {
			if c.Refused() {
				t.Fatalf("%s: %s refused (%q), want the chain planned", m.Model.RealModel, c.Field, c.Conflict)
			}
		}
	}
	result, err := ApplyProviderBundle(context.Background(), uniqueNamesStore{mem}, plan, selectAll(plan), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failed) != 0 || mem.models[a.ID()].ProxyName() != "c" || mem.models[b.ID()].ProxyName() != "a" {
		t.Errorf("failed %+v, a=%q b=%q; want a moved to c, then b to a", result.Failed, mem.models[a.ID()].ProxyName(), mem.models[b.ID()].ProxyName())
	}
}

func TestApplyProviderBundle_ReportsASwapItCannotWrite(t *testing.T) {
	mem, p, _, _ := renameFixture(t)
	models := mem.modelsOf("org-a")
	bundle := model.ProviderBundle{Kind: model.ProviderBundleKind, Version: model.ProviderBundleVersion,
		Provider: model.ProviderBundleSettings{Type: "openai", Currency: "USD", BaseURL: "https://p.test/v1"},
		Models: []model.ProviderBundleModel{
			{RealModel: "model-a", ProxyName: "b", Enabled: true, Prices: model.ModelPrices{Input: 0.1, Output: 0.2}},
			{RealModel: "model-b", ProxyName: "a", Enabled: true, Prices: model.ModelPrices{Input: 0.1, Output: 0.2}},
		}}
	plan := PlanProviderBundleImport(bundle, p, models, namesOf(models))
	result, err := ApplyProviderBundle(context.Background(), uniqueNamesStore{mem}, plan, selectAll(plan), nil)
	if err != nil {
		t.Fatal(err)
	}
	// The dry run refuses the swap: nothing fails at the store, and both
	// renames are reported as not applied.
	if len(result.Failed) != 0 || len(result.Skipped) != 2 {
		t.Errorf("failed = %+v, skipped = %d; want both renames of the swap reported as refused", result.Failed, len(result.Skipped))
	}
}

func TestPlanProviderBundleImportWith_ChecksTheKindAgainstTheKeptAPI(t *testing.T) {
	mem := newMemoryProviderStore()
	p := model.NewProvider("org-a", "P", "openai", "https://p.test/v1", "key", "USD")
	m := model.NewLLMModel(p.ID(), "org-a", "m", "model-m", "", 100, 200)
	m.SetAPIType("anthropic")
	if err := mem.CreateLLMModel(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	models := mem.modelsOf("org-a")
	// The bundle moves the model to the OpenAI API and makes it an
	// embeddings model; the operator keeps the API but takes the kind.
	bundle := model.ProviderBundle{Kind: model.ProviderBundleKind, Version: model.ProviderBundleVersion,
		Provider: model.ProviderBundleSettings{Type: "openai", Currency: "USD", BaseURL: "https://p.test/v1"},
		Models: []model.ProviderBundleModel{{RealModel: "model-m", ProxyName: "m", Kind: model.ModelKindEmbeddings, APIType: "openai",
			Enabled: true, Prices: model.ModelPrices{Input: 0.1, Output: 0.2}}}}
	sel := BundleSelection{Fields: map[int]map[string]bool{0: {BundleFieldKind: true}}}
	plan := PlanProviderBundleImportWith(bundle, p, models, namesOf(models), &sel)
	result, err := ApplyProviderBundle(context.Background(), mem, plan, sel, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated != 0 || len(result.Skipped) != 1 || result.Skipped[0].RefusedField != BundleFieldKind || result.Skipped[0].KindNotServed != "anthropic" {
		t.Errorf("result = updated %d skipped %+v; want the kind change refused against the Anthropic API the model keeps", result.Updated, result.Skipped)
	}
	if mem.models[m.ID()].Kind() != model.ModelKindCompletion {
		t.Error("the model was given a kind its API does not serve")
	}
}

func TestPlanProviderBundleImport_NeverOffersTheNameARefusedRenameKeeps(t *testing.T) {
	// X(x) moves to h, H(h) to y, but y is held by Y, which the bundle does
	// not touch: H keeps h, so X cannot take it.
	mem := newMemoryProviderStore()
	p := model.NewProvider("org-a", "P", "openai", "https://p.test/v1", "key", "USD")
	x := model.NewLLMModel(p.ID(), "org-a", "x", "model-x", "", 100, 200)
	h := model.NewLLMModel(p.ID(), "org-a", "h", "model-h", "", 100, 200)
	y := model.NewLLMModel("other", "org-a", "y", "model-y", "", 100, 200)
	for _, m := range []model.LLMModel{x, h, y} {
		if err := mem.CreateLLMModel(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
	models := mem.modelsOf("org-a")
	bundle := model.ProviderBundle{Kind: model.ProviderBundleKind, Version: model.ProviderBundleVersion,
		Provider: model.ProviderBundleSettings{Type: "openai", Currency: "USD", BaseURL: "https://p.test/v1"},
		Models: []model.ProviderBundleModel{
			{RealModel: "model-x", ProxyName: "h", Enabled: true, Prices: model.ModelPrices{Input: 0.1, Output: 0.2}},
			{RealModel: "model-h", ProxyName: "y", Enabled: true, Prices: model.ModelPrices{Input: 0.1, Output: 0.2}},
		}}
	plan := PlanProviderBundleImport(bundle, p, models, namesOf(models))
	for _, m := range plan.Models {
		refused := false
		for _, c := range m.Changes {
			if c.Field == BundleFieldProxyName && c.Refused() {
				refused = true
			}
		}
		if !refused {
			t.Errorf("%s: rename offered, want it refused (the name it needs stays taken)", m.Model.RealModel)
		}
	}
	result, err := ApplyProviderBundle(context.Background(), uniqueNamesStore{mem}, plan, selectAll(plan), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failed) != 0 {
		t.Errorf("failed = %+v, want nothing the dry run did not announce", result.Failed)
	}
}

func TestPlanProviderBundleImport_RefusesASwapInTheDryRun(t *testing.T) {
	mem, p, _, _ := renameFixture(t)
	models := mem.modelsOf("org-a")
	bundle := model.ProviderBundle{Kind: model.ProviderBundleKind, Version: model.ProviderBundleVersion,
		Provider: model.ProviderBundleSettings{Type: "openai", Currency: "USD", BaseURL: "https://p.test/v1"},
		Models: []model.ProviderBundleModel{
			{RealModel: "model-a", ProxyName: "b", Enabled: true, Prices: model.ModelPrices{Input: 0.1, Output: 0.2}},
			{RealModel: "model-b", ProxyName: "a", Enabled: true, Prices: model.ModelPrices{Input: 0.1, Output: 0.2}},
		}}
	plan := PlanProviderBundleImport(bundle, p, models, namesOf(models))
	for _, m := range plan.Models {
		for _, c := range m.Changes {
			if c.Field == BundleFieldProxyName && !c.Refused() {
				t.Errorf("%s: swap rename offered, want it refused in the dry run", m.Model.RealModel)
			}
		}
	}
	result, err := ApplyProviderBundle(context.Background(), uniqueNamesStore{mem}, plan, selectAll(plan), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failed) != 0 {
		t.Errorf("failed = %+v, want nothing the dry run did not announce", result.Failed)
	}
}

func TestPlanProviderBundleImport_RefusesOnlyTheRenamesOfACycle(t *testing.T) {
	// a(a) and b(b) swap their names while d(d) moves to e: the swap is
	// refused, the unrelated rename is kept.
	mem := newMemoryProviderStore()
	p := model.NewProvider("org-a", "P", "openai", "https://p.test/v1", "key", "USD")
	for _, n := range []string{"a", "b", "c", "d"} {
		if err := mem.CreateLLMModel(context.Background(), model.NewLLMModel(p.ID(), "org-a", n, "model-"+n, "", 100, 200)); err != nil {
			t.Fatal(err)
		}
	}
	models := mem.modelsOf("org-a")
	prices := model.ModelPrices{Input: 0.1, Output: 0.2}
	bundle := model.ProviderBundle{Kind: model.ProviderBundleKind, Version: model.ProviderBundleVersion,
		Provider: model.ProviderBundleSettings{Type: "openai", Currency: "USD", BaseURL: "https://p.test/v1"},
		Models: []model.ProviderBundleModel{
			{RealModel: "model-d", ProxyName: "e", Enabled: true, Prices: prices},
			{RealModel: "model-a", ProxyName: "b", Enabled: true, Prices: prices},
			{RealModel: "model-b", ProxyName: "a", Enabled: true, Prices: prices},
		}}
	plan := PlanProviderBundleImport(bundle, p, models, namesOf(models))
	refused := map[string]bool{}
	for _, m := range plan.Models {
		for _, c := range m.Changes {
			if c.Field == BundleFieldProxyName {
				refused[m.Model.RealModel] = c.Refused()
			}
		}
	}
	if refused["model-d"] || !refused["model-a"] || !refused["model-b"] {
		t.Errorf("refused = %v, want the swap refused and the unrelated rename kept", refused)
	}
}
