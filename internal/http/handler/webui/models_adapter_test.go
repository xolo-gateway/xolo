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
}

func (s *cardVMStore) GetVirtualModelByName(context.Context, model.OrgID, string) (model.VirtualModel, error) {
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
