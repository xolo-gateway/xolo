package pipeline

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// GeneratorExecutor handles NodeTypeGenerator.
// The generator node has no inputs; its "request" output is seeded by the
// engine before execution begins (via ValueContext.Set).
type GeneratorExecutor struct{}

func NewGeneratorExecutor() *GeneratorExecutor { return &GeneratorExecutor{} }

func (e *GeneratorExecutor) Forward(_ context.Context, _ model.PipelineNode, inputs map[string]interface{}, ec ExecutionContext) (*ForwardResult, error) {
	// The request value was already seeded by the engine; just expose it as output.
	return &ForwardResult{
		OutputValues: map[string]interface{}{
			"request": ec.RequestJSON,
		},
	}, nil
}

// ModifiesResponse is always false: a generator node never rewrites the response.
func (e *GeneratorExecutor) ModifiesResponse(context.Context, model.PipelineNode) bool { return false }

func (e *GeneratorExecutor) Backward(ctx context.Context, in BackwardInput) (*BackwardResult, error) {
	return noopBackward(ctx, in)
}
