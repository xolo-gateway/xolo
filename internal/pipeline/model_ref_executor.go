package pipeline

import (
	"context"
	"encoding/json"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// ModelRefExecutor handles NodeTypeModelRef: it emits the configured model
// proxy name on its "model_name" output port. It is a value node specialised
// for model names, so the editor can offer a catalog picker instead of a text
// field; at runtime the two behave the same.
type ModelRefExecutor struct{}

func NewModelRefExecutor() *ModelRefExecutor { return &ModelRefExecutor{} }

func (e *ModelRefExecutor) Forward(_ context.Context, node model.PipelineNode, _ map[string]interface{}, _ ExecutionContext) (*ForwardResult, error) {
	var data model.ModelRefNodeData
	if node.Data != nil {
		if err := json.Unmarshal(node.Data, &data); err != nil {
			return nil, errors.Wrap(err, "model_ref node: invalid data")
		}
	}
	if data.ProxyName == "" {
		return nil, errors.Errorf("model_ref node %s: no model selected", node.ID)
	}
	return &ForwardResult{
		OutputValues: map[string]interface{}{"model_name": data.ProxyName},
	}, nil
}

// ModifiesResponse is always false: a model reference never rewrites the response.
func (e *ModelRefExecutor) ModifiesResponse(context.Context, model.PipelineNode) bool { return false }

func (e *ModelRefExecutor) Backward(ctx context.Context, node model.PipelineNode, state []byte, responseContent string, tokens *TokensUsed, hadError bool) (*BackwardResult, error) {
	return noopBackward(ctx, node, state, responseContent, tokens, hadError)
}
