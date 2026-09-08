package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// TraceExecutor handles NodeTypeTrace: it records the values arriving on its
// ports as a Xolo event, so a pipeline can be observed in production without
// guessing what its nodes produced. The ports are declared in the node data
// (TraceNodeData.Inputs); whatever is connected gets recorded under its port
// name. Without an emitter it only logs.
type TraceExecutor struct {
	noopBackwardExecutor
	emitter port.EventEmitter
}

func NewTraceExecutor(emitter port.EventEmitter) *TraceExecutor {
	return &TraceExecutor{emitter: emitter}
}

func (e *TraceExecutor) Forward(ctx context.Context, node model.PipelineNode, inputs map[string]interface{}, ec ExecutionContext) (*ForwardResult, error) {
	var data model.TraceNodeData
	if err := decodeNodeData(node, &data); err != nil {
		return nil, errors.Wrap(err, "trace node: invalid data")
	}

	attrs := map[string]string{"node_id": node.ID}
	if data.Label != "" {
		attrs["label"] = data.Label
	}
	keys := make([]string, 0, len(inputs))
	for k := range inputs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	logAttrs := []any{slog.String("node", node.ID), slog.String("label", data.Label)}
	for _, k := range keys {
		v := fmt.Sprint(inputs[k])
		attrs["port."+k] = v
		logAttrs = append(logAttrs, slog.String(k, v))
	}
	slog.DebugContext(ctx, "pipeline trace", logAttrs...)

	if e.emitter != nil {
		severity := model.SeverityInfo
		switch data.Severity {
		case "warning":
			severity = model.SeverityWarning
		case "error":
			severity = model.SeverityError
		}
		message := data.Label
		if message == "" {
			message = "trace " + node.ID
		}
		e.emitter.Emit(ctx, model.NewEvent(model.EventSourcePlatform, "pipeline.trace",
			model.WithEventOrg(model.OrgID(ec.OrgID)),
			model.WithEventUser(model.UserID(ec.UserID)),
			model.WithEventSeverity(severity),
			model.WithEventMessage(message),
			model.WithEventAttributes(attrs),
		))
	}

	return &ForwardResult{OutputValues: map[string]interface{}{}}, nil
}
