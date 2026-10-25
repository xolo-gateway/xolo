package webui

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

// cardOrgStore, cardProviderStore and cardVMStore serve the one organization,
// its models and its virtual models, which is all the card derivation reads.
type cardOrgStore struct {
	port.OrgStore
	org model.Organization
}

func (s *cardOrgStore) GetOrgByID(_ context.Context, id model.OrgID) (model.Organization, error) {
	if id != s.org.ID() {
		return nil, port.ErrNotFound
	}
	return s.org, nil
}

type cardProviderStore struct {
	port.ProviderStore
	models []model.LLMModel
}

func (s *cardProviderStore) ListEnabledLLMModels(context.Context, model.OrgID) ([]model.LLMModel, error) {
	return s.models, nil
}

func (s *cardProviderStore) ListProviders(context.Context, model.OrgID) ([]model.Provider, error) {
	return nil, nil
}

type cardVMStore struct {
	port.VirtualModelStore
	lookups int
}

func (s *cardVMStore) GetVirtualModelByName(context.Context, model.OrgID, string) (model.VirtualModel, error) {
	s.lookups++
	return nil, port.ErrNotFound
}

func modelNode(t *testing.T, data model.ModelNodeData) *model.PipelineGraph {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return &model.PipelineGraph{Nodes: []model.PipelineNode{{ID: "m", Type: model.NodeTypeModel, Data: raw}}}
}

// TestVirtualModelUsage pins what the profile models page shows of a virtual
// model: the windows and capabilities its pipeline derives, and undetermined
// capabilities when the pipeline picks its model at runtime.
func TestVirtualModelUsage(t *testing.T) {
	org := model.NewOrganization("tenant", "acme", "ACME", "")
	thinker := model.NewLLMModel(model.NewProviderID(), org.ID(), "thinker", "thinker", "", 0, 0)
	thinker.SetContextWindow(200_000)
	thinker.SetOutputWindow(32_000)
	thinker.SetCapabilities(model.ModelCapabilities{Reasoning: true, Tools: true})

	cards := service.NewModelCardService(
		&cardOrgStore{org: org},
		&cardProviderStore{models: []model.LLMModel{thinker}},
		&cardVMStore{}, nil, nil,
	).NewResolver(context.Background(), []model.OrgID{org.ID()}, "", "")

	derived := model.NewVirtualModel(org.ID(), "smart", "")
	derived.SetGraph(modelNode(t, model.ModelNodeData{ProxyName: "thinker"}))
	mu := virtualModelUsage(cards, derived, org, nil, []string{"acme/smart"})
	if mu.CapabilitiesUndetermined {
		t.Error("a pipeline calling a known model must determine the capabilities")
	}
	if caps := mu.Model.Capabilities(); !caps.Reasoning || !caps.Tools {
		t.Errorf("capabilities = %+v, want those of the called model", caps)
	}
	if mu.Model.ContextWindow() != 200_000 || mu.Model.OutputWindow() != 32_000 {
		t.Errorf("windows = %d/%d, want 200000/32000", mu.Model.ContextWindow(), mu.Model.OutputWindow())
	}

	passthrough := model.NewVirtualModel(org.ID(), "router", "")
	passthrough.SetGraph(modelNode(t, model.ModelNodeData{Passthrough: true}))
	mu = virtualModelUsage(cards, passthrough, org, nil, []string{"acme/router"})
	if !mu.CapabilitiesUndetermined {
		t.Error("a passthrough pipeline must leave the capabilities undetermined")
	}
	if mu.Model.Capabilities() != (model.ModelCapabilities{}) {
		t.Errorf("capabilities = %+v, want none", mu.Model.Capabilities())
	}
}

// TestVirtualModelUsageOutputWindow pins how a virtual model's output window
// combines its candidates: the smallest when all are known, unknown as soon as
// one is, and the catalogue override above both. Each candidate name is
// looked up once for the whole page.
func TestVirtualModelUsageOutputWindow(t *testing.T) {
	org := model.NewOrganization("tenant", "acme", "ACME", "")
	newModel := func(name string, output int64) model.LLMModel {
		m := model.NewLLMModel(model.NewProviderID(), org.ID(), name, name, "", 0, 0)
		m.SetOutputWindow(output)
		return m
	}
	vms := &cardVMStore{}
	cards := service.NewModelCardService(
		&cardOrgStore{org: org},
		&cardProviderStore{models: []model.LLMModel{newModel("large", 64_000), newModel("small", 8_000), newModel("unknown", 0)}},
		vms, nil, nil,
	).NewResolver(context.Background(), []model.OrgID{org.ID()}, "", "")

	fallback := func(t *testing.T, models ...string) *model.PipelineGraph {
		t.Helper()
		raw, err := json.Marshal(model.ModelFallbackNodeData{Models: models})
		if err != nil {
			t.Fatal(err)
		}
		return &model.PipelineGraph{Nodes: []model.PipelineNode{{ID: "f", Type: model.NodeTypeModelFallback, Data: raw}}}
	}

	tests := []struct {
		name      string
		models    []string
		overrides *model.CatalogOverrides
		want      int64
	}{
		{name: "smallest of the candidates", models: []string{"large", "small"}, want: 8_000},
		{name: "unknown when one candidate is", models: []string{"large", "unknown"}, want: 0},
		{name: "override wins", models: []string{"large", "small"}, overrides: &model.CatalogOverrides{MaxOutputTokens: 4_000}, want: 4_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := model.NewVirtualModel(org.ID(), "vm", "")
			vm.SetGraph(fallback(t, tt.models...))
			vm.SetCatalogOverrides(tt.overrides)
			mu := virtualModelUsage(cards, vm, org, nil, []string{"acme/vm"})
			if got := mu.Model.OutputWindow(); got != tt.want {
				t.Errorf("OutputWindow() = %d, want %d", got, tt.want)
			}
		})
	}
	if vms.lookups != 3 {
		t.Errorf("%d virtual model lookups for 3 distinct candidate names, want 3", vms.lookups)
	}
}
