package pipeline

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/pkg/errors"
)

// ValueExecutor handles NodeTypeValue.
// It reads the static value and port type from the node data and emits the
// parsed value on its single "value" output port.
type ValueExecutor struct{}

func NewValueExecutor() *ValueExecutor { return &ValueExecutor{} }

func (e *ValueExecutor) Forward(_ context.Context, node model.PipelineNode, _ map[string]interface{}, _ ExecutionContext) (*ForwardResult, error) {
	data, err := parseValueNodeData(node)
	if err != nil {
		return nil, errors.Wrap(err, "value node: invalid data")
	}

	var parsed interface{}
	switch data.PortType {
	case "number":
		f, err := strconv.ParseFloat(data.Value, 64)
		if err != nil {
			return nil, errors.Errorf("value node: cannot parse %q as number: %v", data.Value, err)
		}
		parsed = f
	case "boolean":
		b, err := strconv.ParseBool(data.Value)
		if err != nil {
			return nil, errors.Errorf("value node: cannot parse %q as boolean", data.Value)
		}
		parsed = b
	default: // "string" and any unknown type
		parsed = data.Value
	}

	return &ForwardResult{
		OutputValues: map[string]interface{}{"value": parsed},
	}, nil
}

// ModifiesResponse is always false: a value node never rewrites the response.
func (e *ValueExecutor) ModifiesResponse(context.Context, model.PipelineNode) bool { return false }

func (e *ValueExecutor) Backward(ctx context.Context, in BackwardInput) (*BackwardResult, error) {
	return noopBackward(ctx, in)
}

func parseValueNodeData(node model.PipelineNode) (*model.ValueNodeData, error) {
	if node.Data == nil {
		return &model.ValueNodeData{PortType: "string"}, nil
	}
	var d model.ValueNodeData
	if err := json.Unmarshal(node.Data, &d); err != nil {
		return nil, err
	}
	if d.PortType == "" {
		d.PortType = "string"
	}
	return &d, nil
}
