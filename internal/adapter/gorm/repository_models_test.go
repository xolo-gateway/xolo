package gorm_test

import (
	"context"
	"testing"
	"time"

	"github.com/pkg/errors"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// disabledLLMModel wraps a model to flip its Enabled flag, the way the web UI
// does when an operator toggles a route off.
type disabledLLMModel struct {
	model.LLMModel
}

func (m disabledLLMModel) Enabled() bool { return false }

// inactiveProvider wraps a provider to flip its Active flag.
type inactiveProvider struct {
	model.Provider
}

func (p inactiveProvider) Active() bool { return false }

func TestProviderStore_Providers(t *testing.T) {
	eachBackend(t, scenarioProviderStoreProviders)
}

func scenarioProviderStoreProviders(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	otherOrg := model.NewOrganization(testTenantID, "other", "Other", "")
	if err := store.CreateOrg(ctx, otherOrg); err != nil {
		t.Fatalf("CreateOrg (other): %v", err)
	}

	provider := model.NewProvider(org.ID(), "OpenAI", "openai", "https://api.openai.com/v1", "encrypted-key", "USD")
	provider.SetCloudTier(2)
	provider.SetRetryConfig(&model.RetryConfig{Enabled: true, MaxAttempts: 3, Delay: 250 * time.Millisecond})
	provider.SetBillingMode(model.BillingModeSubscription)
	provider.SetSubscriptionPlan(&model.SubscriptionPlan{Label: "Pro"})
	if err := store.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	// A provider belonging to another org must never leak into the listing.
	if err := store.CreateProvider(ctx, model.NewProvider(otherOrg.ID(), "Other", "openai", "", "k", "USD")); err != nil {
		t.Fatalf("CreateProvider (other org): %v", err)
	}

	loaded, err := store.GetProviderByID(ctx, provider.ID())
	if err != nil {
		t.Fatalf("GetProviderByID: %v", err)
	}
	if loaded.Name() != "OpenAI" || loaded.Type() != "openai" || loaded.APIKey() != "encrypted-key" {
		t.Errorf("unexpected provider round-trip: %+v", loaded)
	}
	if loaded.CloudTier() != 2 {
		t.Errorf("expected cloud tier 2, got %d", loaded.CloudTier())
	}
	// JSON columns must survive the round-trip on both backends.
	if rc := loaded.RetryConfig(); rc == nil || !rc.Enabled || rc.MaxAttempts != 3 || rc.Delay != 250*time.Millisecond {
		t.Errorf("expected retry config to round-trip, got %+v", loaded.RetryConfig())
	}
	if loaded.SubscriptionPlan() == nil || loaded.SubscriptionPlan().Label != "Pro" {
		t.Errorf("expected subscription plan to round-trip, got %+v", loaded.SubscriptionPlan())
	}
	if loaded.BillingMode() != model.BillingModeSubscription {
		t.Errorf("expected billing mode %q, got %q", model.BillingModeSubscription, loaded.BillingMode())
	}

	if _, err := store.GetProviderByID(ctx, model.NewProviderID()); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("GetProviderByID (unknown): expected port.ErrNotFound, got %v", err)
	}

	providers, err := store.ListProviders(ctx, org.ID())
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(providers) != 1 {
		t.Fatalf("expected 1 provider scoped to the org, got %d", len(providers))
	}

	if err := store.DeleteProvider(ctx, provider.ID()); err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}
	if _, err := store.GetProviderByID(ctx, provider.ID()); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("GetProviderByID (deleted): expected port.ErrNotFound, got %v", err)
	}
}

func TestProviderStore_LLMModels(t *testing.T) {
	eachBackend(t, scenarioProviderStoreLLMModels)
}

func TestProviderStore_LLMModelKind(t *testing.T) {
	eachBackend(t, scenarioProviderStoreLLMModelKind)
}

func scenarioProviderStoreLLMModelKind(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	provider := model.NewProvider(org.ID(), "TypeSafe", "typesafe", "", "key", "USD")
	if err := store.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	chat := model.NewLLMModel(provider.ID(), org.ID(), "chat", "gpt-4o", "", 0, 0)
	jev := model.NewLLMModel(provider.ID(), org.ID(), "jev", "jev-latest", "", 0, 0)
	jev.SetKind(model.ModelKindDecision)
	for _, m := range []model.LLMModel{chat, jev} {
		if err := store.CreateLLMModel(ctx, m); err != nil {
			t.Fatalf("CreateLLMModel: %v", err)
		}
	}

	for m, want := range map[model.LLMModel]model.ModelKind{chat: model.ModelKindCompletion, jev: model.ModelKindDecision} {
		loaded, err := store.GetLLMModelByID(ctx, m.ID())
		if err != nil {
			t.Fatalf("GetLLMModelByID: %v", err)
		}
		if loaded.Kind() != want {
			t.Errorf("%s: kind = %q, want %q", m.ProxyName(), loaded.Kind(), want)
		}
	}

	// A save writes the kind back: an update must not turn the model into a
	// chat one.
	loaded, err := store.GetLLMModelByID(ctx, jev.ID())
	if err != nil {
		t.Fatalf("GetLLMModelByID: %v", err)
	}
	if err := store.SaveLLMModel(ctx, loaded); err != nil {
		t.Fatalf("SaveLLMModel: %v", err)
	}
	if reloaded, err := store.GetLLMModelByID(ctx, jev.ID()); err != nil || reloaded.Kind() != model.ModelKindDecision {
		t.Errorf("kind after save = %v (err %v), want decision", reloaded.Kind(), err)
	}
}

func scenarioProviderStoreLLMModels(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	provider := model.NewProvider(org.ID(), "OpenAI", "openai", "", "key", "USD")
	if err := store.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	llm := model.NewLLMModel(provider.ID(), org.ID(), "fast", "gpt-4o-mini", "A fast model", 150, 600)
	llm.SetContextWindow(128000)
	llm.SetCapabilities(model.ModelCapabilities{Tools: true, Vision: true})
	llm.SetExtraBody(map[string]any{"reasoning_effort": "low"})
	llm.SetAPIType("anthropic")
	llm.SetBaseURL("https://opencode.ai/inference/go/anthropic/v1")
	if err := store.CreateLLMModel(ctx, llm); err != nil {
		t.Fatalf("CreateLLMModel: %v", err)
	}

	loaded, err := store.GetLLMModelByID(ctx, llm.ID())
	if err != nil {
		t.Fatalf("GetLLMModelByID: %v", err)
	}
	if loaded.RealModel() != "gpt-4o-mini" || loaded.PromptCostPer1KTokens() != 150 {
		t.Errorf("unexpected model round-trip: real=%q prompt cost=%d", loaded.RealModel(), loaded.PromptCostPer1KTokens())
	}
	if loaded.ContextWindow() != 128000 {
		t.Errorf("expected context window 128000, got %d", loaded.ContextWindow())
	}
	if caps := loaded.Capabilities(); !caps.Tools || !caps.Vision || caps.Audio {
		t.Errorf("unexpected capabilities: %+v", caps)
	}
	if got := loaded.ExtraBody(); got["reasoning_effort"] != "low" {
		t.Errorf("expected extra body to round-trip, got %+v", got)
	}
	if loaded.APIType() != "anthropic" || loaded.BaseURL() != "https://opencode.ai/inference/go/anthropic/v1" {
		t.Errorf("expected the API override to round-trip, got type=%q url=%q", loaded.APIType(), loaded.BaseURL())
	}
	// CachedPromptCostPer1KTokens falls back to the prompt cost when unset.
	if loaded.CachedPromptCostPer1KTokens() != 150 {
		t.Errorf("expected cached prompt cost to fall back to 150, got %d", loaded.CachedPromptCostPer1KTokens())
	}

	byName, err := store.GetLLMModelByProxyName(ctx, org.ID(), "fast")
	if err != nil {
		t.Fatalf("GetLLMModelByProxyName: %v", err)
	}
	if byName.ID() != llm.ID() {
		t.Errorf("expected model %q, got %q", llm.ID(), byName.ID())
	}
	if _, err := store.GetLLMModelByProxyName(ctx, org.ID(), "unknown"); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("GetLLMModelByProxyName (unknown): expected port.ErrNotFound, got %v", err)
	}

	models, err := store.ListLLMModels(ctx, org.ID())
	if err != nil {
		t.Fatalf("ListLLMModels: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}

	// Deleting a provider cascades to the models it routes to.
	if err := store.DeleteProvider(ctx, provider.ID()); err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}
	models, err = store.ListLLMModels(ctx, org.ID())
	if err != nil {
		t.Fatalf("ListLLMModels (after provider delete): %v", err)
	}
	if len(models) != 0 {
		t.Errorf("expected the provider's models to be cascaded away, got %d", len(models))
	}
}

// TestDefaultedFlagsCanBeCleared guards a regression that made deactivation
// impossible: Organization.Active, Provider.Active, LLMModel.Enabled and
// Middleware.Enabled used to carry a truthy `gorm:"default:…"`. GORM omits a
// zero-valued field carrying a default from the INSERT, so the database
// substituted the default and the OnConflict/UpdateAll clause copied it back
// over the existing row — silently dropping the deactivation. Those columns
// are now declared without a default so the mappers always write them.
func TestDefaultedFlagsCanBeCleared(t *testing.T) {
	eachBackend(t, scenarioDefaultedFlagsCanBeCleared)
}

func scenarioDefaultedFlagsCanBeCleared(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	if err := store.SaveOrg(ctx, model.UpdateOrganization(org, model.WithOrgActive(false))); err != nil {
		t.Fatalf("SaveOrg: %v", err)
	}
	loadedOrg, err := store.GetOrgByID(ctx, org.ID())
	if err != nil {
		t.Fatalf("GetOrgByID: %v", err)
	}
	if loadedOrg.Active() {
		t.Error("expected the organization to be deactivated")
	}

	provider := model.NewProvider(org.ID(), "OpenAI", "openai", "", "key", "USD")
	if err := store.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if err := store.SaveProvider(ctx, inactiveProvider{provider}); err != nil {
		t.Fatalf("SaveProvider: %v", err)
	}
	loadedProvider, err := store.GetProviderByID(ctx, provider.ID())
	if err != nil {
		t.Fatalf("GetProviderByID: %v", err)
	}
	if loadedProvider.Active() {
		t.Error("expected the provider to be deactivated")
	}

	llm := model.NewLLMModel(provider.ID(), org.ID(), "fast", "gpt-4o-mini", "", 1, 1)
	if err := store.CreateLLMModel(ctx, llm); err != nil {
		t.Fatalf("CreateLLMModel: %v", err)
	}
	if err := store.SaveLLMModel(ctx, disabledLLMModel{llm}); err != nil {
		t.Fatalf("SaveLLMModel: %v", err)
	}
	loadedModel, err := store.GetLLMModelByID(ctx, llm.ID())
	if err != nil {
		t.Fatalf("GetLLMModelByID: %v", err)
	}
	if loadedModel.Enabled() {
		t.Error("expected the model to be disabled")
	}

	enabledModels, err := store.ListEnabledLLMModels(ctx, org.ID())
	if err != nil {
		t.Fatalf("ListEnabledLLMModels: %v", err)
	}
	if len(enabledModels) != 0 {
		t.Errorf("expected no enabled model, got %d", len(enabledModels))
	}

	// Same defect on create: a middleware built disabled comes back enabled.
	middleware := model.NewMiddleware(org.ID(), "off", "")
	middleware.SetEnabled(false)
	if err := store.CreateMiddleware(ctx, middleware); err != nil {
		t.Fatalf("CreateMiddleware: %v", err)
	}
	enabledMiddlewares, err := store.ListEnabledMiddlewares(ctx, org.ID())
	if err != nil {
		t.Fatalf("ListEnabledMiddlewares: %v", err)
	}
	if len(enabledMiddlewares) != 0 {
		t.Errorf("expected no enabled middleware, got %d", len(enabledMiddlewares))
	}
}

func TestVirtualModelStore_Lifecycle(t *testing.T) {
	eachBackend(t, scenarioVirtualModelStoreLifecycle)
}

func scenarioVirtualModelStoreLifecycle(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	vm := model.NewVirtualModel(org.ID(), "router", "Routes to the cheapest model")
	vm.SetGraph(&model.PipelineGraph{
		Nodes: []model.PipelineNode{
			{ID: "generator", Type: model.NodeTypeGenerator, Position: model.NodePosition{X: 1, Y: 2}},
			{ID: "sink", Type: model.NodeTypeSink},
		},
		Edges: []model.PipelineEdge{{Source: "generator", Target: "sink"}},
	})
	if err := store.CreateVirtualModel(ctx, vm); err != nil {
		t.Fatalf("CreateVirtualModel: %v", err)
	}

	loaded, err := store.GetVirtualModelByID(ctx, vm.ID())
	if err != nil {
		t.Fatalf("GetVirtualModelByID: %v", err)
	}
	if loaded.Name() != "router" {
		t.Errorf("expected name %q, got %q", "router", loaded.Name())
	}
	// The pipeline graph must come back intact from the TEXT/JSON column.
	graph := loaded.Graph()
	if graph == nil || len(graph.Nodes) != 2 || len(graph.Edges) != 1 {
		t.Fatalf("expected the pipeline graph to round-trip, got %+v", graph)
	}
	if graph.Nodes[0].ID != "generator" || graph.Nodes[0].Position.X != 1 {
		t.Errorf("unexpected first node after round-trip: %+v", graph.Nodes[0])
	}

	byName, err := store.GetVirtualModelByName(ctx, org.ID(), "router")
	if err != nil {
		t.Fatalf("GetVirtualModelByName: %v", err)
	}
	if byName.ID() != vm.ID() {
		t.Errorf("expected virtual model %q, got %q", vm.ID(), byName.ID())
	}
	if _, err := store.GetVirtualModelByName(ctx, org.ID(), "unknown"); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("GetVirtualModelByName (unknown): expected port.ErrNotFound, got %v", err)
	}

	vm.SetDescription("Updated description")
	if err := store.SaveVirtualModel(ctx, vm); err != nil {
		t.Fatalf("SaveVirtualModel: %v", err)
	}
	loaded, err = store.GetVirtualModelByID(ctx, vm.ID())
	if err != nil {
		t.Fatalf("GetVirtualModelByID (after save): %v", err)
	}
	if loaded.Description() != "Updated description" {
		t.Errorf("expected the description to be updated, got %q", loaded.Description())
	}

	list, err := store.ListVirtualModels(ctx, org.ID())
	if err != nil {
		t.Fatalf("ListVirtualModels: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 virtual model, got %d", len(list))
	}

	if err := store.DeleteVirtualModel(ctx, vm.ID()); err != nil {
		t.Fatalf("DeleteVirtualModel: %v", err)
	}
	if _, err := store.GetVirtualModelByID(ctx, vm.ID()); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("GetVirtualModelByID (deleted): expected port.ErrNotFound, got %v", err)
	}
}

// TestVirtualModelStore_RenameCollisionRejected pins the translation of a
// backend unique violation on idx_org_name into port.ErrAlreadyExists at
// SaveVirtualModel time. The previous incarnation used
// errors.Is(err, gorm.ErrDuplicatedKey), which is never produced because
// db.Config.TranslateError is not set anywhere; the current mapping uses
// isUniqueViolation (dialect.go) and inspects *sqlite3.Error / *pgconn.PgError
// directly. The two supported backends spell the offending index
// differently (SQLite reports "table.org_id, table.name", PostgreSQL the
// index name "idx_org_name"), so this must hold on both. Run it with
// XOLO_TEST_POSTGRES_DSN set to cover the PostgreSQL side.
func TestVirtualModelStore_RenameCollisionRejected(t *testing.T) {
	eachBackend(t, scenarioVirtualModelStoreRenameCollisionRejected)
}

func scenarioVirtualModelStoreRenameCollisionRejected(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	org := model.NewOrganization(testTenantID, "acme-rename", "Acme Rename", "")
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	first := model.NewVirtualModel(org.ID(), "router", "Routes to the cheapest model")
	if err := store.CreateVirtualModel(ctx, first); err != nil {
		t.Fatalf("CreateVirtualModel (first): %v", err)
	}

	second := model.NewVirtualModel(org.ID(), "premium", "Premium model")
	if err := store.CreateVirtualModel(ctx, second); err != nil {
		t.Fatalf("CreateVirtualModel (second): %v", err)
	}

	// Rename `second` onto the unique name held by `first`. The
	// (org_id, name) unique index must reject, and the store must
	// surface it as port.ErrAlreadyExists so the webui update handler
	// can redirect to ?error=exists.
	second.SetName("router")
	err := store.SaveVirtualModel(ctx, second)
	if !errors.Is(err, port.ErrAlreadyExists) {
		t.Fatalf("SaveVirtualModel (rename collision): expected port.ErrAlreadyExists, got %v", err)
	}
}

func TestMiddlewareStore_EnabledOrdering(t *testing.T) {
	eachBackend(t, scenarioMiddlewareStoreEnabledOrdering)
}

// TestMiddlewareStore_DuplicateNameRejected pins the translation of a
// backend unique violation on idx_mw_org_name into port.ErrAlreadyExists
// at CreateMiddleware time. The previous incarnation used
// errors.Is(err, gorm.ErrDuplicatedKey), which is never produced because
// db.Config.TranslateError is not set anywhere; the current mapping uses
// isUniqueViolation (dialect.go) and inspects *sqlite3.Error / *pgconn.PgError
// directly. The two supported backends spell the offending index differently
// (SQLite reports "middlewares.org_id, middlewares.name", PostgreSQL the
// index name "idx_mw_org_name"), so this must hold on both. Run it with
// XOLO_TEST_POSTGRES_DSN set to cover the PostgreSQL side. This is the
// Create* counterpart to TestVirtualModelStore_RenameCollisionRejected
// (which covers the Save* side); both are required to fully cover
// xolo-gateway/xolo#75.
func TestMiddlewareStore_DuplicateNameRejected(t *testing.T) {
	eachBackend(t, scenarioMiddlewareStoreDuplicateNameRejected)
}

func scenarioMiddlewareStoreDuplicateNameRejected(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	org := model.NewOrganization(testTenantID, "acme-mw-dup", "Acme MW Dup", "")
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	first := model.NewMiddleware(org.ID(), "guardrails", "Prompt guardrails")
	if err := store.CreateMiddleware(ctx, first); err != nil {
		t.Fatalf("CreateMiddleware (first): %v", err)
	}

	// A second create with the same (org_id, name) collides on the
	// idx_mw_org_name unique index and must surface as
	// port.ErrAlreadyExists — the webui form handler relies on this
	// to redirect to ?error=exists instead of leaking a 500.
	duplicate := model.NewMiddleware(org.ID(), "guardrails", "Same name, different description")
	err := store.CreateMiddleware(ctx, duplicate)
	if !errors.Is(err, port.ErrAlreadyExists) {
		t.Fatalf("CreateMiddleware (duplicate name): expected port.ErrAlreadyExists, got %v", err)
	}

	// A different name in the same org must still succeed.
	if err := store.CreateMiddleware(ctx, model.NewMiddleware(org.ID(), "pii-redaction", "PII redaction")); err != nil {
		t.Fatalf("CreateMiddleware (different name): %v", err)
	}

	// The same name in a *different* org must also succeed: the unique
	// index is per org_id, not global.
	otherOrg := model.NewOrganization(testTenantID, "other-mw-dup", "Other MW Dup", "")
	if err := store.CreateOrg(ctx, otherOrg); err != nil {
		t.Fatalf("CreateOrg (other): %v", err)
	}
	if err := store.CreateMiddleware(ctx, model.NewMiddleware(otherOrg.ID(), "guardrails", "Same name, other org")); err != nil {
		t.Fatalf("CreateMiddleware (other org): %v", err)
	}
}

func scenarioMiddlewareStoreEnabledOrdering(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	last := model.NewMiddleware(org.ID(), "last", "")
	last.SetPriority(20)
	first := model.NewMiddleware(org.ID(), "first", "")
	first.SetPriority(10)
	disabled := model.NewMiddleware(org.ID(), "disabled", "")
	disabled.SetPriority(1)
	disabled.SetEnabled(false)

	for _, m := range []*model.BaseMiddleware{last, first, disabled} {
		if err := store.CreateMiddleware(ctx, m); err != nil {
			t.Fatalf("CreateMiddleware(%s): %v", m.Name(), err)
		}
	}

	all, err := store.ListMiddlewares(ctx, org.ID())
	if err != nil {
		t.Fatalf("ListMiddlewares: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 middlewares, got %d", len(all))
	}

	enabled, err := store.ListEnabledMiddlewares(ctx, org.ID())
	if err != nil {
		t.Fatalf("ListEnabledMiddlewares: %v", err)
	}
	if len(enabled) != 2 {
		t.Fatalf("expected 2 enabled middlewares, got %d", len(enabled))
	}
	// Ascending priority: the outermost middleware comes first.
	if enabled[0].Name() != "first" || enabled[1].Name() != "last" {
		t.Errorf("expected [first last], got [%s %s]", enabled[0].Name(), enabled[1].Name())
	}

	if err := store.DeleteMiddleware(ctx, first.ID()); err != nil {
		t.Fatalf("DeleteMiddleware: %v", err)
	}
	if _, err := store.GetMiddlewareByID(ctx, first.ID()); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("GetMiddlewareByID (deleted): expected port.ErrNotFound, got %v", err)
	}
}
