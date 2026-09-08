package pipeline

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"math"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// This file holds the small built-in logic nodes: compare, select, math,
// sample, context and note. None of them touches the response, so they share
// the same no-op backward pass.

type noopBackwardExecutor struct{}

func (noopBackwardExecutor) ModifiesResponse(context.Context, model.PipelineNode) bool { return false }

func (noopBackwardExecutor) Backward(ctx context.Context, node model.PipelineNode, state []byte, responseContent string, tokens *TokensUsed, hadError bool) (*BackwardResult, error) {
	return noopBackward(ctx, node, state, responseContent, tokens, hadError)
}

func decodeNodeData(node model.PipelineNode, into interface{}) error {
	if node.Data == nil {
		return nil
	}
	return json.Unmarshal(node.Data, into)
}

func numberInput(inputs map[string]interface{}, name string) (float64, bool) {
	v, ok := inputs[name]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case bool:
		if n {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// ─── compare ────────────────────────────────────────────────────────────────

// CompareExecutor handles NodeTypeCompare: value <op> threshold -> boolean.
// The threshold comes from the port when connected, from the data otherwise.
type CompareExecutor struct{ noopBackwardExecutor }

func NewCompareExecutor() *CompareExecutor { return &CompareExecutor{} }

func (e *CompareExecutor) Forward(_ context.Context, node model.PipelineNode, inputs map[string]interface{}, _ ExecutionContext) (*ForwardResult, error) {
	data := model.CompareNodeData{Op: "gt"}
	if err := decodeNodeData(node, &data); err != nil {
		return nil, errors.Wrap(err, "compare node: invalid data")
	}
	value, ok := numberInput(inputs, "value")
	if !ok {
		return nil, errors.Errorf("compare node %s: value port is not connected or not a number", node.ID)
	}
	threshold := data.Threshold
	if t, ok := numberInput(inputs, "threshold"); ok {
		threshold = t
	}

	var result bool
	switch data.Op {
	case "gt", "":
		result = value > threshold
	case "gte":
		result = value >= threshold
	case "lt":
		result = value < threshold
	case "lte":
		result = value <= threshold
	case "eq":
		result = value == threshold
	case "ne":
		result = value != threshold
	default:
		return nil, errors.Errorf("compare node %s: unknown op %q", node.ID, data.Op)
	}
	return &ForwardResult{OutputValues: map[string]interface{}{"result": result}}, nil
}

// ─── select ─────────────────────────────────────────────────────────────────

// SelectExecutor handles NodeTypeSelect: condition ? when_true : when_false.
type SelectExecutor struct{ noopBackwardExecutor }

func NewSelectExecutor() *SelectExecutor { return &SelectExecutor{} }

func (e *SelectExecutor) Forward(_ context.Context, node model.PipelineNode, inputs map[string]interface{}, _ ExecutionContext) (*ForwardResult, error) {
	cond, ok := inputs["condition"].(bool)
	if !ok {
		if n, isNum := numberInput(inputs, "condition"); isNum {
			cond = n != 0
		} else {
			return nil, errors.Errorf("select node %s: condition port is not connected or not a boolean", node.ID)
		}
	}
	port := "when_false"
	if cond {
		port = "when_true"
	}
	v, ok := inputs[port]
	if !ok {
		return nil, errors.Errorf("select node %s: %s port is not connected", node.ID, port)
	}
	return &ForwardResult{OutputValues: map[string]interface{}{"value": v}}, nil
}

// ─── math ───────────────────────────────────────────────────────────────────

var mathInputPorts = []string{"a", "b", "c", "d"}

// MathExecutor handles NodeTypeMath: combines the connected number inputs.
type MathExecutor struct{ noopBackwardExecutor }

func NewMathExecutor() *MathExecutor { return &MathExecutor{} }

func (e *MathExecutor) Forward(_ context.Context, node model.PipelineNode, inputs map[string]interface{}, _ ExecutionContext) (*ForwardResult, error) {
	data := model.MathNodeData{Op: "sum"}
	if err := decodeNodeData(node, &data); err != nil {
		return nil, errors.Wrap(err, "math node: invalid data")
	}

	var values, weights []float64
	for i, port := range mathInputPorts {
		v, ok := numberInput(inputs, port)
		if !ok {
			continue
		}
		values = append(values, v)
		w := 1.0
		if i < len(data.Weights) {
			w = data.Weights[i]
		}
		weights = append(weights, w)
	}
	if len(values) == 0 {
		return nil, errors.Errorf("math node %s: no number input connected", node.ID)
	}

	var result float64
	switch data.Op {
	case "sum", "":
		for _, v := range values {
			result += v
		}
	case "avg":
		for _, v := range values {
			result += v
		}
		result /= float64(len(values))
	case "min":
		result = math.Inf(1)
		for _, v := range values {
			result = math.Min(result, v)
		}
	case "max":
		result = math.Inf(-1)
		for _, v := range values {
			result = math.Max(result, v)
		}
	case "product":
		result = 1
		for _, v := range values {
			result *= v
		}
	case "weighted":
		var total float64
		for i, v := range values {
			result += v * weights[i]
			total += weights[i]
		}
		if total != 0 {
			result /= total
		}
	default:
		return nil, errors.Errorf("math node %s: unknown op %q", node.ID, data.Op)
	}
	return &ForwardResult{OutputValues: map[string]interface{}{"result": result}}, nil
}

// ─── sample ─────────────────────────────────────────────────────────────────

// SampleExecutor handles NodeTypeSample: selects a share of the traffic.
// Keyed on the user or the token, the same caller always lands on the same
// side, which is what a progressive rollout needs; "random" draws per request.
type SampleExecutor struct {
	noopBackwardExecutor
	random func() float64
}

func NewSampleExecutor() *SampleExecutor {
	return &SampleExecutor{random: func() float64 {
		// Cheap per-request randomness derived from the clock is enough for a
		// traffic split; it does not need to be unpredictable.
		h := fnv.New64a()
		var b [8]byte
		n := time.Now().UnixNano()
		for i := range b {
			b[i] = byte(n >> (8 * i))
		}
		h.Write(b[:])
		return float64(h.Sum64()%10000) / 100
	}}
}

func (e *SampleExecutor) Forward(_ context.Context, node model.PipelineNode, _ map[string]interface{}, ec ExecutionContext) (*ForwardResult, error) {
	data := model.SampleNodeData{Percent: 10, Key: "user"}
	if err := decodeNodeData(node, &data); err != nil {
		return nil, errors.Wrap(err, "sample node: invalid data")
	}

	var bucket float64
	switch data.Key {
	case "random":
		bucket = e.random()
	case "token":
		bucket = hashBucket(ec.TokenID + "|" + data.Salt)
	default: // "user"
		bucket = hashBucket(ec.UserID + "|" + data.Salt)
	}

	return &ForwardResult{OutputValues: map[string]interface{}{
		"selected": bucket < data.Percent,
		"bucket":   bucket,
	}}, nil
}

// hashBucket maps a key onto [0, 100) with two decimals.
func hashBucket(key string) float64 {
	h := fnv.New64a()
	h.Write([]byte(key))
	return float64(h.Sum64()%10000) / 100
}

// ─── context ────────────────────────────────────────────────────────────────

// ContextExecutor handles NodeTypeContext: exposes on ports what the host
// already knows about the request, so pipelines can route on the caller and
// the time of day without a plugin.
type ContextExecutor struct {
	noopBackwardExecutor
	now func() time.Time
}

func NewContextExecutor() *ContextExecutor { return &ContextExecutor{now: time.Now} }

var weekdays = []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}

func (e *ContextExecutor) Forward(_ context.Context, node model.PipelineNode, _ map[string]interface{}, ec ExecutionContext) (*ForwardResult, error) {
	var data model.ContextNodeData
	if err := decodeNodeData(node, &data); err != nil {
		return nil, errors.Wrap(err, "context node: invalid data")
	}
	loc := time.UTC
	if data.Timezone != "" {
		l, err := time.LoadLocation(data.Timezone)
		if err != nil {
			return nil, errors.Wrapf(err, "context node %s: unknown timezone %q", node.ID, data.Timezone)
		}
		loc = l
	}
	now := e.now().In(loc)
	wd := now.Weekday()

	return &ForwardResult{OutputValues: map[string]interface{}{
		"user_id":         ec.UserID,
		"org_id":          ec.OrgID,
		"token_id":        ec.TokenID,
		"display_name":    ec.DisplayName,
		"requested_model": ec.TargetModelName,
		"hour":            float64(now.Hour()) + float64(now.Minute())/60,
		"weekday":         weekdays[wd],
		"is_weekend":      wd == time.Saturday || wd == time.Sunday,
	}}, nil
}

// ─── note ───────────────────────────────────────────────────────────────────

// NoteExecutor handles NodeTypeNote: documentation on the canvas, nothing at
// runtime.
type NoteExecutor struct{ noopBackwardExecutor }

func NewNoteExecutor() *NoteExecutor { return &NoteExecutor{} }

func (e *NoteExecutor) Forward(context.Context, model.PipelineNode, map[string]interface{}, ExecutionContext) (*ForwardResult, error) {
	return &ForwardResult{OutputValues: map[string]interface{}{}}, nil
}
