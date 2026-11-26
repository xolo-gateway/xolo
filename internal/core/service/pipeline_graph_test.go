package service

import (
	"encoding/json"
	"testing"

	"github.com/pkg/errors"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// TestValidatePipelineNode_TraceCondition checks that a trace declaring a
// port named like its fixed condition input is refused where the graph is
// written: read as the condition, a false value would silence the trace.
func TestValidatePipelineNode_TraceCondition(t *testing.T) {
	trace := func(inputs ...string) model.PipelineNode {
		data := model.TraceNodeData{Label: "budget"}
		for _, name := range inputs {
			data.Inputs = append(data.Inputs, model.TraceInputDef{Name: name, PortType: "boolean"})
		}
		raw, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		return model.PipelineNode{ID: "trace", Type: model.NodeTypeTrace, Data: raw}
	}

	if err := ValidatePipelineNode(trace("fin_de_budget", "budget_pressure")); err != nil {
		t.Errorf("declared ports with other names: %v", err)
	}
	if err := ValidatePipelineNode(trace()); err != nil {
		t.Errorf("no declared port: %v", err)
	}
	if err := ValidatePipelineNode(trace("score", model.TraceConditionPort)); !errors.Is(err, port.ErrInvalid) {
		t.Errorf("a declared port named %q: err = %v, want ErrInvalid", model.TraceConditionPort, err)
	}
}
