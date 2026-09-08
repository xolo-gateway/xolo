package pipeline

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

func TestModelRefExecutor_EmitsProxyName(t *testing.T) {
	node := model.PipelineNode{ID: "ref", Type: model.NodeTypeModelRef, Data: json.RawMessage(`{"proxyName":"org/gpt-4o"}`)}
	res, err := NewModelRefExecutor().Forward(context.Background(), node, nil, ExecutionContext{})
	if err != nil {
		t.Fatal(err)
	}
	if res.OutputValues["model_name"] != "org/gpt-4o" {
		t.Errorf("unexpected output: %v", res.OutputValues)
	}
}

func TestModelRefExecutor_RequiresSelection(t *testing.T) {
	for _, data := range []json.RawMessage{nil, json.RawMessage(`{}`), json.RawMessage(`{"proxyName":""}`)} {
		node := model.PipelineNode{ID: "ref", Type: model.NodeTypeModelRef, Data: data}
		if _, err := NewModelRefExecutor().Forward(context.Background(), node, nil, ExecutionContext{}); err == nil {
			t.Errorf("data %s: expected an error", data)
		}
	}
}
