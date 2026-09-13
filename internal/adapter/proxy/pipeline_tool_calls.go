package proxy

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"

	"github.com/bornholm/genai/llm"
)

// wireToolCall is how a tool call travels to the pipeline's backward pass and
// back: the shape an OpenAI-compatible client sees, with `arguments` as the
// JSON document the client will execute.
type wireToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// encodeToolCalls serializes the tool calls of a response for the backward
// pass. It returns an empty string when there are none, which nodes read as
// "nothing to restore".
func encodeToolCalls(calls []llm.ToolCall) string {
	if len(calls) == 0 {
		return ""
	}

	wire := make([]wireToolCall, 0, len(calls))
	for _, tc := range calls {
		wire = append(wire, wireToolCall{ID: tc.ID(), Name: tc.Name(), Arguments: toolCallArguments(tc.Parameters())})
	}

	raw, err := json.Marshal(wire)
	if err != nil {
		return ""
	}
	return string(raw)
}

// toolCallArguments normalizes a tool call's parameters to the JSON string the
// providers carry them as, matching what genai/proxy writes on the wire.
func toolCallArguments(params any) string {
	switch p := params.(type) {
	case nil:
		return ""
	case string:
		return p
	default:
		raw, err := json.Marshal(p)
		if err != nil {
			return ""
		}
		return string(raw)
	}
}

// decodeToolCalls turns the backward pass's rewritten tool calls back into
// llm.ToolCall values, returning nil when nothing was rewritten.
//
// The id and the name are taken from the original calls, matched by id: they
// pair a call with its result and are not a node's to change.
func decodeToolCalls(ctx context.Context, rewrittenJSON string, original []llm.ToolCall) []llm.ToolCall {
	if rewrittenJSON == "" || len(original) == 0 {
		return nil
	}

	var wire []wireToolCall
	if err := json.Unmarshal([]byte(rewrittenJSON), &wire); err != nil {
		slog.WarnContext(ctx, "pipeline: could not read the rewritten tool calls, keeping the provider's",
			slog.Any("error", err))
		return nil
	}

	byID := make(map[string]string, len(wire))
	for _, w := range wire {
		byID[w.ID] = w.Arguments
	}

	out := make([]llm.ToolCall, 0, len(original))
	for _, tc := range original {
		args, ok := byID[tc.ID()]
		if !ok {
			args = toolCallArguments(tc.Parameters())
		}
		out = append(out, llm.NewToolCall(tc.ID(), tc.Name(), args))
	}
	return out
}

// streamedToolCalls reassembles the tool calls of a streamed response, whose
// arguments arrive split across chunks, so the backward pass sees the whole
// payload rather than fragments of it.
type streamedToolCalls struct {
	order []int
	byIdx map[int]*wireToolCall
}

func (s *streamedToolCalls) collect(deltas []llm.ToolCallDelta) {
	for _, d := range deltas {
		if s.byIdx == nil {
			s.byIdx = map[int]*wireToolCall{}
		}
		call, ok := s.byIdx[d.Index()]
		if !ok {
			call = &wireToolCall{}
			s.byIdx[d.Index()] = call
			s.order = append(s.order, d.Index())
		}
		if id := d.ID(); id != "" {
			call.ID = id
		}
		if name := d.Name(); name != "" {
			call.Name = name
		}
		call.Arguments += d.ParametersDelta()
	}
}

// json serializes the reassembled calls for the backward pass, in the order the
// provider streamed them.
func (s *streamedToolCalls) json() string {
	if len(s.order) == 0 {
		return ""
	}

	wire := make([]wireToolCall, 0, len(s.order))
	for _, idx := range s.order {
		wire = append(wire, *s.byIdx[idx])
	}

	raw, err := json.Marshal(wire)
	if err != nil {
		return ""
	}
	return string(raw)
}

// rewritten turns the backward pass's answer into the tool call deltas to
// re-emit, or nil when nothing was rewritten. Each call is emitted once, with
// its whole argument payload: both stream writers accept a single
// fully-formed fragment.
func (s *streamedToolCalls) rewritten(ctx context.Context, rewrittenJSON string) []llm.ToolCallDelta {
	if rewrittenJSON == "" || len(s.order) == 0 {
		return nil
	}

	var wire []wireToolCall
	if err := json.Unmarshal([]byte(rewrittenJSON), &wire); err != nil {
		slog.WarnContext(ctx, "pipeline: could not read the rewritten tool calls, keeping the provider's",
			slog.Any("error", err))
		return nil
	}

	byID := make(map[string]string, len(wire))
	for _, w := range wire {
		byID[w.ID] = w.Arguments
	}

	indexes := append([]int(nil), s.order...)
	sort.Ints(indexes)

	out := make([]llm.ToolCallDelta, 0, len(indexes))
	for _, idx := range indexes {
		call := s.byIdx[idx]
		args := call.Arguments
		if rewritten, ok := byID[call.ID]; ok {
			args = rewritten
		}
		out = append(out, llm.NewToolCallDelta(idx, call.ID, call.Name, args))
	}
	return out
}
