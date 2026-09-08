package pipeline

import (
	"context"
	"fmt"

	"github.com/bornholm/genai/llm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/pkg/errors"
)

// Engine executes pipeline graphs.
type Engine struct {
	registry *Registry
}

// NewEngine creates an Engine backed by the given registry.
func NewEngine(registry *Registry) *Engine {
	return &Engine{registry: registry}
}

// ForwardExecution is the result of running the forward pass of a pipeline.
type ForwardExecution struct {
	// ResolvedClient is the llm.Client returned by the terminal model node.
	ResolvedClient llm.Client
	// ResolvedModel is the real model name to forward to the provider.
	ResolvedModel string
	// ResolvedModelID is the internal database ID of the resolved LLM model.
	ResolvedModelID model.LLMModelID
	// ExecutedNodes is the ordered list of nodes that ran (for backward pass).
	ExecutedNodes []ExecutedNode
	// FinalMessagesJSON is the messages array as it stood after all executed
	// nodes ran, taking into account any "messages_json" output value
	// produced by plugin nodes (e.g. modified_messages_json). It defaults to
	// ec.MessagesJSON when no node modified the messages.
	FinalMessagesJSON string
	// Tools is the aggregated list of llm.Tool contributed by executed nodes
	// (e.g. TOOL_PROVIDER plugins). Informational: ResolvedClient is already
	// wrapped with whatever decorator needs these tools.
	Tools []llm.Tool
	// ModelOutcome reports, after the call, which model actually answered when
	// the terminal node may switch models (fallback). Nil otherwise.
	ModelOutcome UsedModelReporter
}

// ExecutedNode pairs a node with the opaque state returned by its Forward call.
type ExecutedNode struct {
	Node      model.PipelineNode
	NodeState []byte
	// NoResponseRewrite records that this node declared, during its forward
	// pass, that it will not rewrite the response for this execution.
	NoResponseRewrite bool
}

// RunForward executes the graph in topological order (forward pass).
// It returns after the first terminal (model) node resolves an LLM client.
func (e *Engine) RunForward(ctx context.Context, graph *model.PipelineGraph, ec ExecutionContext) (*ForwardExecution, error) {
	if err := validateGraph(graph); err != nil {
		return nil, errors.Wrap(err, "invalid pipeline graph")
	}

	order, err := topoSort(graph)
	if err != nil {
		return nil, errors.Wrap(err, "pipeline graph has a cycle")
	}

	vc := newValueContext()
	var executed []ExecutedNode
	var tools []llm.Tool
	var decorators []func(llm.Client) llm.Client
	currentMessagesJSON := ec.MessagesJSON

	// Seed the generator node's output.
	for _, node := range graph.Nodes {
		if node.Type == model.NodeTypeGenerator {
			vc.Set(node.ID, "request", ec.RequestJSON)
		}
	}

	for _, nodeID := range order {
		node := nodeByID(graph, nodeID)
		if node == nil {
			continue
		}

		exec, ok := e.registry.Get(node.Type)
		if !ok {
			return nil, fmt.Errorf("no executor registered for node type %q (node %s)", node.Type, node.ID)
		}

		inputs := vc.ResolveInputs(graph, node.ID)
		// Expose the messages as they stand entering this node (after any
		// upstream modification) so nodes that recurse into nested pipelines
		// (model nodes resolving a virtual model / middleware chain) seed the
		// child with the already-modified messages rather than the originals.
		nodeEC := ec
		nodeEC.MessagesJSON = currentMessagesJSON
		result, err := exec.Forward(ctx, *node, inputs, nodeEC)
		if err != nil {
			return nil, errors.Wrapf(err, "node %s (%s) forward failed", node.ID, node.Type)
		}

		if result.Rejected {
			return nil, &RejectionError{Reason: result.RejectionReason}
		}

		// Splice the executed nodes of any nested pipeline before this node's own
		// entry, so the backward pass covers them (in reverse) too.
		executed = append(executed, result.NestedExecutedNodes...)
		executed = append(executed, ExecutedNode{Node: *node, NodeState: result.NodeState, NoResponseRewrite: result.NoResponseRewrite})

		// Store output values in the value context.
		for port, val := range result.OutputValues {
			vc.Set(node.ID, port, val)
		}

		if msgs, ok := result.OutputValues["messages_json"].(string); ok && msgs != "" {
			currentMessagesJSON = msgs
		}

		if len(result.Tools) > 0 {
			tools = append(tools, result.Tools...)
		}
		if result.ClientDecorator != nil {
			decorators = append(decorators, result.ClientDecorator)
		}

		// Terminal: model node resolved the LLM client.
		if result.ResolvedClient != nil {
			resolvedClient := result.ResolvedClient
			for _, decorate := range decorators {
				resolvedClient = decorate(resolvedClient)
			}
			return &ForwardExecution{
				ResolvedClient:    resolvedClient,
				ResolvedModel:     result.ResolvedModel,
				ResolvedModelID:   result.ResolvedModelID,
				ExecutedNodes:     executed,
				FinalMessagesJSON: currentMessagesJSON,
				Tools:             tools,
				ModelOutcome:      result.ModelOutcome,
			}, nil
		}
	}

	return nil, errors.New("pipeline graph has no terminal model node")
}

// RunBackward executes nodes in reverse order (post-response pass).
// It returns the (potentially modified) response content.
func (e *Engine) RunBackward(
	ctx context.Context,
	exec *ForwardExecution,
	responseContent string,
	tokens *TokensUsed,
	hadError bool,
) (string, error) {
	current := responseContent
	// Iterate in reverse order.
	for i := len(exec.ExecutedNodes) - 1; i >= 0; i-- {
		en := exec.ExecutedNodes[i]
		ex, ok := e.registry.Get(en.Node.Type)
		if !ok {
			continue
		}
		result, err := ex.Backward(ctx, en.Node, en.NodeState, current, tokens, hadError)
		if err != nil {
			// Non-fatal: log and continue.
			continue
		}
		if result.ModifiedResponseContent != "" {
			current = result.ModifiedResponseContent
		}
	}
	return current, nil
}

// MayModifyResponse reports whether the backward pass of this execution could
// rewrite the response content. It is false for the common case — no node
// declares a POST_RESPONSE capability — which lets callers stream the response
// straight through instead of buffering it to have the full text on hand.
//
// It is conservative by construction: any executor that does not implement
// ResponseModifier counts as a potential modifier only if its Backward is not
// known to be a no-op, so new node types default to the safe (buffered) path.
func (e *Engine) MayModifyResponse(ctx context.Context, exec *ForwardExecution) bool {
	if exec == nil {
		return false
	}
	for _, en := range exec.ExecutedNodes {
		// The node already told us during its forward pass that it will leave
		// this response alone, which is more precise than what its type can say.
		if en.NoResponseRewrite {
			continue
		}
		ex, ok := e.registry.Get(en.Node.Type)
		if !ok {
			// Unknown type: RunBackward skips it entirely.
			continue
		}
		rm, ok := ex.(ResponseModifier)
		if !ok {
			// No declared capability: assume it may rewrite the response.
			return true
		}
		if rm.ModifiesResponse(ctx, en.Node) {
			return true
		}
	}
	return false
}

// RejectionError is returned when a pipeline node blocks the request.
type RejectionError struct {
	Reason string
}

func (e *RejectionError) Error() string {
	if e.Reason != "" {
		return "request rejected by pipeline: " + e.Reason
	}
	return "request rejected by pipeline"
}

// validateGraph checks graph structure: generator + sink present, sink connected.
func validateGraph(g *model.PipelineGraph) error {
	hasGenerator := false
	var sinkIDs []string
	for _, n := range g.Nodes {
		switch n.Type {
		case model.NodeTypeGenerator:
			hasGenerator = true
		case model.NodeTypeSink:
			sinkIDs = append(sinkIDs, n.ID)
		}
	}
	if !hasGenerator {
		return errors.New("pipeline must have a generator node")
	}
	if len(sinkIDs) == 0 {
		return errors.New("pipeline must have a sink node")
	}
	// Every sink must have at least one incoming edge.
	for _, sinkID := range sinkIDs {
		connected := false
		for _, e := range g.Edges {
			if e.Target == sinkID {
				connected = true
				break
			}
		}
		if !connected {
			return errors.New("sink node has no incoming edge: connect a terminal node's response port to the sink")
		}
	}
	return nil
}

// topoSort returns nodes in topological order using Kahn's algorithm.
func topoSort(g *model.PipelineGraph) ([]string, error) {
	inDegree := make(map[string]int, len(g.Nodes))
	adj := make(map[string][]string, len(g.Nodes))
	for _, n := range g.Nodes {
		inDegree[n.ID] = 0
		adj[n.ID] = nil
	}
	for _, e := range g.Edges {
		adj[e.Source] = append(adj[e.Source], e.Target)
		inDegree[e.Target]++
	}

	queue := make([]string, 0, len(g.Nodes))
	for _, n := range g.Nodes {
		if inDegree[n.ID] == 0 {
			queue = append(queue, n.ID)
		}
	}

	var order []string
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		order = append(order, cur)
		for _, next := range adj[cur] {
			inDegree[next]--
			if inDegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}

	if len(order) != len(g.Nodes) {
		return nil, errors.New("cycle detected in pipeline graph")
	}
	return order, nil
}

func nodeByID(g *model.PipelineGraph, id string) *model.PipelineNode {
	for i := range g.Nodes {
		if g.Nodes[i].ID == id {
			return &g.Nodes[i]
		}
	}
	return nil
}
