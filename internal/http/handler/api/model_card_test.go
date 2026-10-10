package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/handler/api"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn"
)

// cardVMStore serves the virtual models a card test declares, by name and by
// list, which is all the card derivation asks of the store.
type cardVMStore struct {
	port.VirtualModelStore
	vms []model.VirtualModel
}

func (s *cardVMStore) ListVirtualModels(_ context.Context, orgID model.OrgID) ([]model.VirtualModel, error) {
	var out []model.VirtualModel
	for _, vm := range s.vms {
		if vm.OrgID() == orgID {
			out = append(out, vm)
		}
	}
	return out, nil
}

func (s *cardVMStore) GetVirtualModelByName(_ context.Context, orgID model.OrgID, name string) (model.VirtualModel, error) {
	for _, vm := range s.vms {
		if vm.OrgID() == orgID && vm.Name() == name {
			return vm, nil
		}
	}
	return nil, port.ErrNotFound
}

type cardEntry struct {
	ID               string `json:"id"`
	ContextLength    *int   `json:"context_length"`
	PerRequestLimits *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"per_request_limits"`
	Architecture struct {
		InputModalities []string `json:"input_modalities"`
	} `json:"architecture"`
	Pricing struct {
		Prompt     float64 `json:"prompt"`
		Completion float64 `json:"completion"`
	} `json:"pricing"`
	SupportedParameters []string `json:"supported_parameters"`
}

func realModel(org model.Organization, name string, ctxWindow int64, caps model.ModelCapabilities, promptCost, completionCost int64) *model.BaseLLMModel {
	m := model.NewLLMModel(model.NewProviderID(), org.ID(), name, name, "", promptCost, completionCost)
	m.SetContextWindow(ctxWindow)
	m.SetCapabilities(caps)
	return m
}

func nodeData(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func vmWithGraph(org model.Organization, name string, g *model.PipelineGraph) *model.BaseVirtualModel {
	vm := model.NewVirtualModel(org.ID(), name, "")
	vm.SetGraph(g)
	return vm
}

// cardPVMStore serves the personal virtual models of a card test.
type cardPVMStore struct {
	port.PersonalVirtualModelStore
	pvms []model.PersonalVirtualModel
}

func (s *cardPVMStore) ListPersonalVirtualModels(_ context.Context, userID model.UserID) ([]model.PersonalVirtualModel, error) {
	var out []model.PersonalVirtualModel
	for _, pvm := range s.pvms {
		if pvm.UserID() == userID {
			out = append(out, pvm)
		}
	}
	return out, nil
}

func (s *cardPVMStore) GetPersonalVirtualModelByName(_ context.Context, userID model.UserID, name string) (model.PersonalVirtualModel, error) {
	for _, pvm := range s.pvms {
		if pvm.UserID() == userID && pvm.Name() == name {
			return pvm, nil
		}
	}
	return nil, port.ErrNotFound
}

// cardUser is the caller of every card scenario, so personal models can be
// declared as theirs.
var cardUser = model.NewUser(testTenantID, "oidc", "u", "u@example.com", "U", true, "user")

// cardScenario describes one GET /api/v1/models call.
type cardScenario struct {
	orgs []model.Organization
	// models are the enabled models of each organization, by organization ID.
	models map[model.OrgID][]model.LLMModel
	vms    []model.VirtualModel
	pvms   []model.PersonalVirtualModel
	// tokenOrg scopes the call to one organization, like an API token. Empty
	// means a session, scoped by the memberships of every org.
	tokenOrg model.OrgID
	query    string
}

func (sc cardScenario) call(t *testing.T) map[string]cardEntry {
	t.Helper()

	user := cardUser
	orgsByID := map[string]model.Organization{}
	enabled := map[string][]model.LLMModel{}
	var memberships []model.Membership
	for _, org := range sc.orgs {
		orgsByID[string(org.ID())] = org
		enabled[string(org.ID())] = sc.models[org.ID()]
		memberships = append(memberships, model.NewMembership(user.ID(), org.ID()))
	}
	for _, pvm := range sc.pvms {
		if pvm.UserID() != user.ID() {
			t.Fatalf("personal model %q is not owned by the calling user", pvm.Name())
		}
	}

	h := api.NewHandler(
		&fakeProviderStoreForModels{enabledModels: enabled},
		&fakeOrgStoreForModels{
			orgsByID:    orgsByID,
			memberships: map[string][]model.Membership{string(user.ID()): memberships},
		},
		&cardVMStore{vms: sc.vms},
		&cardPVMStore{pvms: sc.pvms},
		nil, nil, nil, nil,
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/models"+sc.query, nil)
	ctx := authn.SetContextUser(req.Context(), &authn.User{
		Provider: "oidc", Subject: "u", OrgID: string(sc.tokenOrg), TokenID: "tok", TenantID: string(testTenantID),
	})
	ctx = httpCtx.SetUser(ctx, user)
	ctx = httpCtx.SetPermissionResolver(ctx, func(context.Context, model.OrgID) (rbac.PermissionSet, error) {
		return rbac.NewPermissionSet([]string{
			string(rbac.PermModelUseOrg), string(rbac.PermModelUseVirtual), string(rbac.PermPersonalVMCreate),
		}, nil), nil
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Data []cardEntry `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	out := map[string]cardEntry{}
	for _, e := range resp.Data {
		out[e.ID] = e
	}
	return out
}

// callModels calls the catalogue of a single organization as a token of it.
func callModels(t *testing.T, org model.Organization, models []model.LLMModel, vms []model.VirtualModel, query string) map[string]cardEntry {
	t.Helper()
	return cardScenario{
		orgs:     []model.Organization{org},
		models:   map[model.OrgID][]model.LLMModel{org.ID(): models},
		vms:      vms,
		tokenOrg: org.ID(),
		query:    query,
	}.call(t)
}

func modelNodeGraph(t *testing.T, proxyName string) *model.PipelineGraph {
	return &model.PipelineGraph{Nodes: []model.PipelineNode{
		{ID: "m", Type: model.NodeTypeModel, Data: nodeData(t, model.ModelNodeData{ProxyName: proxyName})},
	}}
}

func TestVirtualModelCard_SingleModelNode(t *testing.T) {
	org := model.NewOrganization(testTenantID, "acme", "ACME", "")
	vision := realModel(org, "vision", 128_000, model.ModelCapabilities{Tools: true, Vision: true}, 3000, 15000)
	vm := vmWithGraph(org, "auto", modelNodeGraph(t, "vision"))

	got := callModels(t, org, []model.LLMModel{vision}, []model.VirtualModel{vm}, "")["acme/auto"]

	if got.ContextLength == nil || *got.ContextLength != 128_000 {
		t.Fatalf("context_length = %v, want 128000", got.ContextLength)
	}
	if !slices.Contains(got.Architecture.InputModalities, "image") {
		t.Errorf("input_modalities = %v, want image", got.Architecture.InputModalities)
	}
	if !slices.Contains(got.SupportedParameters, "tools") {
		t.Errorf("supported_parameters = %v, want tools", got.SupportedParameters)
	}
	if got.Pricing.Prompt != 0.003 || got.Pricing.Completion != 0.015 {
		t.Errorf("pricing = %+v, want the real model's", got.Pricing)
	}
}

func TestVirtualModelCard_FallbackTakesTheConservativeCandidate(t *testing.T) {
	org := model.NewOrganization(testTenantID, "acme", "ACME", "")
	big := realModel(org, "big", 200_000, model.ModelCapabilities{Tools: true, Vision: true}, 1000, 2000)
	small := realModel(org, "small", 32_000, model.ModelCapabilities{Vision: true}, 5000, 1000)
	vm := vmWithGraph(org, "safe", &model.PipelineGraph{Nodes: []model.PipelineNode{
		{ID: "f", Type: model.NodeTypeModelFallback, Data: nodeData(t, model.ModelFallbackNodeData{Models: []string{"big", "small"}})},
	}})

	got := callModels(t, org, []model.LLMModel{big, small}, []model.VirtualModel{vm}, "")["acme/safe"]

	if got.ContextLength == nil || *got.ContextLength != 32_000 {
		t.Fatalf("context_length = %v, want the smaller window 32000", got.ContextLength)
	}
	if slices.Contains(got.SupportedParameters, "tools") {
		t.Errorf("tools advertised although only one candidate supports it: %v", got.SupportedParameters)
	}
	if !slices.Contains(got.Architecture.InputModalities, "image") {
		t.Errorf("both candidates see images, got %v", got.Architecture.InputModalities)
	}
	if got.Pricing.Prompt != 0.005 || got.Pricing.Completion != 0.002 {
		t.Errorf("pricing = %+v, want the highest of each", got.Pricing)
	}
}

func TestVirtualModelCard_ToolsFilter(t *testing.T) {
	org := model.NewOrganization(testTenantID, "acme", "ACME", "")
	withTools := realModel(org, "agent", 64_000, model.ModelCapabilities{Tools: true}, 0, 0)
	noTools := realModel(org, "plain", 64_000, model.ModelCapabilities{}, 0, 0)
	vms := []model.VirtualModel{
		vmWithGraph(org, "agentic", modelNodeGraph(t, "agent")),
		vmWithGraph(org, "basic", modelNodeGraph(t, "plain")),
	}

	got := callModels(t, org, []model.LLMModel{withTools, noTools}, vms, "?supported_parameters=tools")

	if _, ok := got["acme/agentic"]; !ok {
		t.Error("virtual model over a tool-capable model is missing from the filtered list")
	}
	if _, ok := got["acme/basic"]; ok {
		t.Error("virtual model over a model without tools must not match the tools filter")
	}
}

func TestVirtualModelCard_ModelRefThroughSelect(t *testing.T) {
	org := model.NewOrganization(testTenantID, "acme", "ACME", "")
	a := realModel(org, "a", 100_000, model.ModelCapabilities{Tools: true}, 0, 0)
	b := realModel(org, "b", 50_000, model.ModelCapabilities{Tools: true}, 0, 0)
	g := &model.PipelineGraph{
		Nodes: []model.PipelineNode{
			{ID: "ra", Type: model.NodeTypeModelRef, Data: nodeData(t, model.ModelRefNodeData{ProxyName: "a"})},
			{ID: "rb", Type: model.NodeTypeModelRef, Data: nodeData(t, model.ModelRefNodeData{ProxyName: "b"})},
			{ID: "sel", Type: model.NodeTypeSelect},
			{ID: "m", Type: model.NodeTypeModel},
		},
		Edges: []model.PipelineEdge{
			{ID: "e1", Source: "ra", SourcePort: "model_name", Target: "sel", TargetPort: "when_true"},
			{ID: "e2", Source: "rb", SourcePort: "model_name", Target: "sel", TargetPort: "when_false"},
			{ID: "e3", Source: "sel", SourcePort: "value", Target: "m", TargetPort: "model_name"},
		},
	}

	got := callModels(t, org, []model.LLMModel{a, b}, []model.VirtualModel{vmWithGraph(org, "routed", g)}, "")["acme/routed"]

	if got.ContextLength == nil || *got.ContextLength != 50_000 {
		t.Fatalf("context_length = %v, want 50000", got.ContextLength)
	}
	if !slices.Contains(got.SupportedParameters, "tools") {
		t.Errorf("both branches support tools, got %v", got.SupportedParameters)
	}
}

func TestVirtualModelCard_NestedVirtualModel(t *testing.T) {
	org := model.NewOrganization(testTenantID, "acme", "ACME", "")
	base := realModel(org, "base", 16_000, model.ModelCapabilities{Reasoning: true}, 0, 0)
	inner := vmWithGraph(org, "inner", modelNodeGraph(t, "base"))
	outer := vmWithGraph(org, "outer", modelNodeGraph(t, "inner"))

	got := callModels(t, org, []model.LLMModel{base}, []model.VirtualModel{inner, outer}, "")["acme/outer"]

	if got.ContextLength == nil || *got.ContextLength != 16_000 {
		t.Fatalf("context_length = %v, want 16000", got.ContextLength)
	}
	if !slices.Contains(got.SupportedParameters, "reasoning") {
		t.Errorf("reasoning lost through the nested model: %v", got.SupportedParameters)
	}
}

func TestVirtualModelCard_CycleIsNotDerived(t *testing.T) {
	org := model.NewOrganization(testTenantID, "acme", "ACME", "")
	ping := vmWithGraph(org, "ping", modelNodeGraph(t, "pong"))
	pong := vmWithGraph(org, "pong", modelNodeGraph(t, "ping"))

	got := callModels(t, org, nil, []model.VirtualModel{ping, pong}, "")

	for _, id := range []string{"acme/ping", "acme/pong"} {
		if got[id].ContextLength != nil {
			t.Errorf("%s: context_length = %d for a cycle, want omitted", id, *got[id].ContextLength)
		}
	}
}

func TestVirtualModelCard_OverrideWinsOverDerivation(t *testing.T) {
	org := model.NewOrganization(testTenantID, "acme", "ACME", "")
	real := realModel(org, "real", 128_000, model.ModelCapabilities{Tools: true, Vision: true}, 0, 0)
	vm := vmWithGraph(org, "capped", modelNodeGraph(t, "real"))
	vm.SetCatalogOverrides(&model.CatalogOverrides{
		ContextWindow:   32_000,
		MaxOutputTokens: 4_096,
		Capabilities:    &model.ModelCapabilities{Reasoning: true},
	})

	got := callModels(t, org, []model.LLMModel{real}, []model.VirtualModel{vm}, "")["acme/capped"]

	if got.ContextLength == nil || *got.ContextLength != 32_000 {
		t.Fatalf("context_length = %v, want the override 32000", got.ContextLength)
	}
	if got.PerRequestLimits == nil || got.PerRequestLimits.CompletionTokens != 4_096 {
		t.Errorf("per_request_limits = %+v, want completion 4096", got.PerRequestLimits)
	}
	if slices.Contains(got.SupportedParameters, "tools") || !slices.Contains(got.SupportedParameters, "reasoning") {
		t.Errorf("capabilities should be the override's, got %v", got.SupportedParameters)
	}
	if slices.Contains(got.Architecture.InputModalities, "image") {
		t.Errorf("vision should be gone with the overridden capabilities, got %v", got.Architecture.InputModalities)
	}
}

func TestVirtualModelCard_RuntimeModelWithoutOverrideKeepsToday(t *testing.T) {
	org := model.NewOrganization(testTenantID, "acme", "ACME", "")
	real := realModel(org, "real", 128_000, model.ModelCapabilities{Tools: true}, 100, 200)
	passthrough := vmWithGraph(org, "dynamic", &model.PipelineGraph{Nodes: []model.PipelineNode{
		{ID: "m", Type: model.NodeTypeModel, Data: nodeData(t, model.ModelNodeData{Passthrough: true})},
	}})
	unwired := vmWithGraph(org, "unwired", &model.PipelineGraph{Nodes: []model.PipelineNode{
		{ID: "m", Type: model.NodeTypeModel},
	}})

	got := callModels(t, org, []model.LLMModel{real}, []model.VirtualModel{passthrough, unwired}, "")

	for _, id := range []string{"acme/dynamic", "acme/unwired"} {
		e := got[id]
		if e.ContextLength != nil {
			t.Errorf("%s: context_length = %d, want omitted", id, *e.ContextLength)
		}
		if slices.Contains(e.SupportedParameters, "tools") || e.Pricing.Prompt != 0 {
			t.Errorf("%s: card should stay the plain one, got %+v", id, e)
		}
	}
}

func TestVirtualModelCard_OverrideAppliesWhenNothingIsDerivable(t *testing.T) {
	org := model.NewOrganization(testTenantID, "acme", "ACME", "")
	vm := vmWithGraph(org, "dynamic", &model.PipelineGraph{Nodes: []model.PipelineNode{
		{ID: "m", Type: model.NodeTypeModel, Data: nodeData(t, model.ModelNodeData{Passthrough: true})},
	}})
	vm.SetCatalogOverrides(&model.CatalogOverrides{ContextWindow: 64_000, Capabilities: &model.ModelCapabilities{Tools: true}})

	got := callModels(t, org, nil, []model.VirtualModel{vm}, "?supported_parameters=tools")["acme/dynamic"]

	if got.ContextLength == nil || *got.ContextLength != 64_000 {
		t.Fatalf("context_length = %v, want 64000", got.ContextLength)
	}
}

func personalVMWithGraph(name string, g *model.PipelineGraph) *model.BasePersonalVirtualModel {
	pvm := model.NewPersonalVirtualModel(callerID(), name, "")
	pvm.SetGraph(g)
	return pvm
}

func callerID() model.UserID { return cardUser.ID() }

func TestVirtualModelCard_StaticNameSharedByBothSelectBranches(t *testing.T) {
	org := model.NewOrganization(testTenantID, "acme", "ACME", "")
	a := realModel(org, "a", 100_000, model.ModelCapabilities{Tools: true}, 0, 0)
	// Edges without an ID, as an imported graph may carry them, and one
	// model_ref wired to both branches.
	g := &model.PipelineGraph{
		Nodes: []model.PipelineNode{
			{ID: "ra", Type: model.NodeTypeModelRef, Data: nodeData(t, model.ModelRefNodeData{ProxyName: "a"})},
			{ID: "sel", Type: model.NodeTypeSelect},
			{ID: "m", Type: model.NodeTypeModel},
		},
		Edges: []model.PipelineEdge{
			{Source: "ra", SourcePort: "model_name", Target: "sel", TargetPort: "when_true"},
			{Source: "ra", SourcePort: "model_name", Target: "sel", TargetPort: "when_false"},
			{Source: "sel", SourcePort: "value", Target: "m", TargetPort: "model_name"},
		},
	}

	got := callModels(t, org, []model.LLMModel{a}, []model.VirtualModel{vmWithGraph(org, "same", g)}, "")["acme/same"]

	if got.ContextLength == nil || *got.ContextLength != 100_000 {
		t.Fatalf("context_length = %v, want 100000", got.ContextLength)
	}
}

func TestVirtualModelCard_NestedUnderivableKeepsItsOverrides(t *testing.T) {
	org := model.NewOrganization(testTenantID, "acme", "ACME", "")
	inner := vmWithGraph(org, "inner", &model.PipelineGraph{Nodes: []model.PipelineNode{
		{ID: "m", Type: model.NodeTypeModel, Data: nodeData(t, model.ModelNodeData{Passthrough: true})},
	}})
	inner.SetCatalogOverrides(&model.CatalogOverrides{ContextWindow: 24_000})
	outer := vmWithGraph(org, "outer", modelNodeGraph(t, "inner"))

	got := callModels(t, org, nil, []model.VirtualModel{inner, outer}, "")["acme/outer"]

	if got.ContextLength == nil || *got.ContextLength != 24_000 {
		t.Fatalf("context_length = %v, want the nested override 24000", got.ContextLength)
	}
}

func TestPersonalVirtualModelCard_Derived(t *testing.T) {
	org := model.NewOrganization(testTenantID, "acme", "ACME", "")
	vision := realModel(org, "vision", 128_000, model.ModelCapabilities{Tools: true, Vision: true}, 3000, 15000)
	pvm := personalVMWithGraph("mine", modelNodeGraph(t, "vision"))

	got := cardScenario{
		orgs:     []model.Organization{org},
		models:   map[model.OrgID][]model.LLMModel{org.ID(): {vision}},
		pvms:     []model.PersonalVirtualModel{pvm},
		tokenOrg: org.ID(),
	}.call(t)["~/mine"]

	if got.ContextLength == nil || *got.ContextLength != 128_000 {
		t.Fatalf("context_length = %v, want 128000", got.ContextLength)
	}
	if !slices.Contains(got.SupportedParameters, "tools") || !slices.Contains(got.Architecture.InputModalities, "image") {
		t.Errorf("capabilities not derived: %+v", got)
	}
	if got.Pricing.Prompt != 0.003 {
		t.Errorf("pricing = %+v, want the real model's", got.Pricing)
	}
}

func TestPersonalVirtualModelCard_OverrideWins(t *testing.T) {
	org := model.NewOrganization(testTenantID, "acme", "ACME", "")
	real := realModel(org, "real", 128_000, model.ModelCapabilities{Tools: true}, 0, 0)
	pvm := personalVMWithGraph("mine", modelNodeGraph(t, "real"))
	pvm.SetCatalogOverrides(&model.CatalogOverrides{ContextWindow: 16_000})

	got := cardScenario{
		orgs:     []model.Organization{org},
		models:   map[model.OrgID][]model.LLMModel{org.ID(): {real}},
		pvms:     []model.PersonalVirtualModel{pvm},
		tokenOrg: org.ID(),
	}.call(t)["~/mine"]

	if got.ContextLength == nil || *got.ContextLength != 16_000 {
		t.Fatalf("context_length = %v, want the override 16000", got.ContextLength)
	}
	if !slices.Contains(got.SupportedParameters, "tools") {
		t.Errorf("capabilities should still be derived, got %v", got.SupportedParameters)
	}
}

// A session spans every organization of the user, so the organization local
// names resolve against is not known: each is tried.
func TestPersonalVirtualModelCard_SessionWithSeveralOrgs(t *testing.T) {
	orgA := model.NewOrganization(testTenantID, "acme", "ACME", "")
	orgB := model.NewOrganization(testTenantID, "umbrella", "Umbrella", "")
	onlyInA := realModel(orgA, "shared", 64_000, model.ModelCapabilities{Tools: true}, 0, 0)
	pvm := personalVMWithGraph("mine", modelNodeGraph(t, "shared"))

	got := cardScenario{
		orgs:   []model.Organization{orgA, orgB},
		models: map[model.OrgID][]model.LLMModel{orgA.ID(): {onlyInA}},
		pvms:   []model.PersonalVirtualModel{pvm},
	}.call(t)["~/mine"]

	if got.ContextLength == nil || *got.ContextLength != 64_000 {
		t.Fatalf("context_length = %v, want 64000 from the org that has the model", got.ContextLength)
	}
}

func TestVirtualModelCard_ReferencesAPersonalModel(t *testing.T) {
	org := model.NewOrganization(testTenantID, "acme", "ACME", "")
	real := realModel(org, "real", 32_000, model.ModelCapabilities{Reasoning: true}, 0, 0)
	pvm := personalVMWithGraph("mine", modelNodeGraph(t, "real"))
	vm := vmWithGraph(org, "wraps", modelNodeGraph(t, "~/mine"))

	got := cardScenario{
		orgs:     []model.Organization{org},
		models:   map[model.OrgID][]model.LLMModel{org.ID(): {real}},
		vms:      []model.VirtualModel{vm},
		pvms:     []model.PersonalVirtualModel{pvm},
		tokenOrg: org.ID(),
	}.call(t)["acme/wraps"]

	if got.ContextLength == nil || *got.ContextLength != 32_000 {
		t.Fatalf("context_length = %v, want 32000", got.ContextLength)
	}
}

func TestVirtualModelCard_CycleKeepsTheOverridesOfItsMembers(t *testing.T) {
	org := model.NewOrganization(testTenantID, "acme", "ACME", "")
	ping := vmWithGraph(org, "ping", modelNodeGraph(t, "pong"))
	pong := vmWithGraph(org, "pong", modelNodeGraph(t, "ping"))
	pong.SetCatalogOverrides(&model.CatalogOverrides{ContextWindow: 12_000})

	got := callModels(t, org, nil, []model.VirtualModel{ping, pong}, "")

	if e := got["acme/ping"]; e.ContextLength == nil || *e.ContextLength != 12_000 {
		t.Fatalf("ping context_length = %v, want pong's override 12000", e.ContextLength)
	}
}

// The candidates of the organizations tried differ: capabilities intersect, the
// window is the smallest, and the price, which could be either, is left out.
func TestPersonalVirtualModelCard_SessionWithDivergingOrgs(t *testing.T) {
	orgA := model.NewOrganization(testTenantID, "acme", "ACME", "")
	orgB := model.NewOrganization(testTenantID, "umbrella", "Umbrella", "")
	inA := realModel(orgA, "shared", 64_000, model.ModelCapabilities{Tools: true, Vision: true}, 1000, 2000)
	inB := realModel(orgB, "shared", 32_000, model.ModelCapabilities{Tools: true}, 5000, 6000)
	pvm := personalVMWithGraph("mine", modelNodeGraph(t, "shared"))

	got := cardScenario{
		orgs:   []model.Organization{orgA, orgB},
		models: map[model.OrgID][]model.LLMModel{orgA.ID(): {inA}, orgB.ID(): {inB}},
		pvms:   []model.PersonalVirtualModel{pvm},
	}.call(t)["~/mine"]

	if got.ContextLength == nil || *got.ContextLength != 32_000 {
		t.Fatalf("context_length = %v, want the smaller window 32000", got.ContextLength)
	}
	if !slices.Contains(got.SupportedParameters, "tools") || slices.Contains(got.Architecture.InputModalities, "image") {
		t.Errorf("capabilities should be the intersection, got %+v", got)
	}
	if got.Pricing.Prompt != 0 || got.Pricing.Completion != 0 {
		t.Errorf("pricing = %+v, want none when the orgs disagree", got.Pricing)
	}
}
