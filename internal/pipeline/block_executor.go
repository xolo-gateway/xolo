package pipeline

import (
	"context"
	"log/slog"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// DefaultBlockMessage is what the caller reads when a block node has no
// message of its own.
const DefaultBlockMessage = "Requête refusée par la politique du pipeline."

// BlockExecutor handles NodeTypeBlock: it rejects the request when the boolean
// arriving on its condition port is true. Plugins that block do so behind
// their own threshold; this node lets a pipeline decide from any combination
// of signals and shows that decision on the canvas. A rejection is recorded as
// a request.blocked event, the same type the blocking plugins emit, so the
// events page reads the same whichever node refused.
type BlockExecutor struct {
	noopBackwardExecutor
	emitter port.EventEmitter
}

// NewBlockExecutor creates a BlockExecutor. Without an emitter a rejection is
// only logged.
func NewBlockExecutor(emitter port.EventEmitter) *BlockExecutor {
	return &BlockExecutor{emitter: emitter}
}

func (e *BlockExecutor) Forward(ctx context.Context, node model.PipelineNode, inputs map[string]interface{}, ec ExecutionContext) (*ForwardResult, error) {
	var data model.BlockNodeData
	if err := decodeNodeData(node, &data); err != nil {
		return nil, errors.Wrap(err, "block node: invalid data")
	}

	cond, ok := inputs["condition"].(bool)
	if !ok {
		n, isNum := numberInput(inputs, "condition")
		if !isNum {
			return nil, errors.Errorf("block node %s: condition port is not connected or not a boolean", node.ID)
		}
		cond = n != 0
	}
	if !cond {
		return &ForwardResult{OutputValues: map[string]interface{}{}}, nil
	}

	message := data.Message
	if message == "" {
		message = DefaultBlockMessage
	}
	slog.InfoContext(ctx, "pipeline: request blocked by block node",
		slog.String("node", node.ID), slog.String("label", data.Label))

	if e.emitter != nil {
		attrs := map[string]string{"node_id": node.ID, "reason": message}
		if data.Label != "" {
			attrs["label"] = data.Label
		}
		e.emitter.Emit(ctx, model.NewEvent(model.EventSourcePlatform, "request.blocked",
			model.WithEventOrg(model.OrgID(ec.OrgID)),
			model.WithEventUser(model.UserID(ec.UserID)),
			model.WithEventSeverity(model.SeverityWarning),
			model.WithEventMessage("Requête bloquée : "+message),
			model.WithEventAttributes(attrs),
		))
	}

	return &ForwardResult{Rejected: true, RejectionReason: message}, nil
}
