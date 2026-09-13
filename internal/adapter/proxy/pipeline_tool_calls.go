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
// The id and the name always come from the original calls: they pair a call
// with its result and are not a node's to change. Only the arguments are read
// back, position by position.
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

	out := make([]llm.ToolCall, 0, len(original))
	for i, tc := range original {
		args := toolCallArguments(tc.Parameters())
		if rewritten, ok := rewrittenArgumentsAt(wire, i, tc.ID()); ok {
			args = rewritten
		}
		out = append(out, llm.NewToolCall(tc.ID(), tc.Name(), args))
	}
	return out
}

// rewrittenArgumentsAt reads back the arguments a node rewrote for the call at
// position i.
//
// The match is positional, not by id. A node is handed the array as encodeToolCalls
// built it and gives it back in the same order, whereas ids are not the key they
// look like: two parallel calls to the same tool can both arrive with an empty
// id from an OpenAI-compatible provider, and matching on that would give both
// of them the arguments of the last one -- a client reading the wrong file, with
// nothing in the logs to say why.
//
// The id is still used, as a guard rather than a key: when both sides carry one
// and they disagree, the node reordered or dropped something and the provider's
// arguments are kept.
func rewrittenArgumentsAt(wire []wireToolCall, i int, originalID string) (string, bool) {
	if i >= len(wire) {
		return "", false
	}
	if w := wire[i]; w.ID == "" || originalID == "" || w.ID == originalID {
		return w.Arguments, true
	}
	return "", false
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
	for _, idx := range s.sortedIndexes() {
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

	indexes := s.sortedIndexes()

	out := make([]llm.ToolCallDelta, 0, len(indexes))
	for i, idx := range indexes {
		call := s.byIdx[idx]
		args := call.Arguments
		if rewritten, ok := rewrittenArgumentsAt(wire, i, call.ID); ok {
			args = rewritten
		}
		out = append(out, llm.NewToolCallDelta(idx, call.ID, call.Name, args))
	}
	return out
}

// sortedIndexes returns the collected call indexes in provider order, which is
// the order json() serializes them in and therefore the order rewritten() must
// read them back in.
func (s *streamedToolCalls) sortedIndexes() []int {
	indexes := append([]int(nil), s.order...)
	sort.Ints(indexes)
	return indexes
}
