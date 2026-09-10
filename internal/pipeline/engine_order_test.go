package pipeline

import (
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// TestTopoSort_TerminalNodesRunLast guards against a side-effect node being
// skipped because its edge was drawn after the model's: the forward pass
// returns as soon as the model node resolves, so every other ready node must
// come first in the order, whatever the edge declaration order.
func TestTopoSort_TerminalNodesRunLast(t *testing.T) {
	for _, terminal := range []model.PipelineNodeType{model.NodeTypeModel, model.NodeTypeModelFallback} {
		t.Run(string(terminal), func(t *testing.T) {
			g := &model.PipelineGraph{
				Nodes: []model.PipelineNode{
					{ID: "gen", Type: model.NodeTypeGenerator},
					{ID: "llm", Type: terminal},
					{ID: "guard", Type: model.NodeTypePlugin},
					{ID: "trace", Type: model.NodeTypeTrace},
					{ID: "out", Type: model.NodeTypeSink},
				},
				Edges: []model.PipelineEdge{
					// The model edge is declared first on purpose.
					{ID: "e1", Source: "gen", SourcePort: "request", Target: "llm", TargetPort: "request"},
					{ID: "e2", Source: "gen", SourcePort: "request", Target: "guard", TargetPort: "request"},
					{ID: "e3", Source: "guard", SourcePort: "risk", Target: "trace", TargetPort: "risk"},
					{ID: "e4", Source: "llm", SourcePort: "response", Target: "out", TargetPort: "response"},
				},
			}

			order, err := topoSort(g)
			if err != nil {
				t.Fatal(err)
			}

			pos := map[string]int{}
			for i, id := range order {
				pos[id] = i
			}
			for _, id := range []string{"guard", "trace"} {
				if pos[id] > pos["llm"] {
					t.Errorf("order = %v: %s runs after the terminal node and would be skipped", order, id)
				}
			}
			if pos["out"] < pos["llm"] {
				t.Errorf("order = %v: sink must stay after the model", order)
			}
		})
	}
}
