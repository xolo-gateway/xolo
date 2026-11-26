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
// name. A value on model.TraceConditionPort decides whether the event is
// recorded at all. Without an emitter it only logs.
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

	// A graph saved before the condition port existed may declare a port of
	// that name, which writing a graph now refuses. Such a port stays what it
	// was, a recorded value, so the trace does not fall silent on a false.
	legacy := declaresConditionPort(data)
	if legacy {
		slog.WarnContext(ctx, "pipeline: trace declares a port named like its condition, recorded as a value; rename it",
			slog.String("node", node.ID), slog.String("port", model.TraceConditionPort))
	} else if !traceConditionHolds(ctx, node, inputs) {
		return &ForwardResult{OutputValues: map[string]interface{}{}}, nil
	}

	attrs := map[string]string{"node_id": node.ID}
	if data.Label != "" {
		attrs["label"] = data.Label
	}
	keys := make([]string, 0, len(inputs))
	for k := range inputs {
		if k == model.TraceConditionPort && !legacy {
			continue
		}
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
		e.emitter.Emit(ctx, model.NewEvent(model.EventSourcePlatform, model.EventTypePipelineTrace,
			model.WithEventOrg(model.OrgID(ec.OrgID)),
			model.WithEventUser(model.UserID(ec.UserID)),
			model.WithEventSeverity(severity),
			model.WithEventMessage(message),
			model.WithEventAttributes(attrs),
		))
	}

	return &ForwardResult{OutputValues: map[string]interface{}{}}, nil
}

// traceConditionHolds reads the condition port of a trace node. Without a
// value, the port not connected or its source silent, the trace records as it
// always did. A number counts as true when it is not zero, as for a block
// node. Any other value records the event, with a warning: a trace observes,
// and an observation lost to a wiring mistake would go unnoticed, where an
// event too many shows the mistake.
func traceConditionHolds(ctx context.Context, node model.PipelineNode, inputs map[string]interface{}) bool {
	v, ok := inputs[model.TraceConditionPort]
	if !ok {
		return true
	}
	if b, isBool := v.(bool); isBool {
		return b
	}
	if n, isNum := numberInput(inputs, model.TraceConditionPort); isNum {
		return n != 0
	}
	slog.WarnContext(ctx, "pipeline: trace condition is neither a boolean nor a number, recording anyway",
		slog.String("node", node.ID), slog.Any("value", v))
	return true
}

// declaresConditionPort reports whether the trace declares an input named like
// its fixed condition port.
func declaresConditionPort(data model.TraceNodeData) bool {
	for _, in := range data.Inputs {
		if in.Name == model.TraceConditionPort {
			return true
		}
	}
	return false
}
