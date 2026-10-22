package service

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

const (
	maxPipelineNodes    = 500
	maxPipelineEdges    = 2000
	maxPipelineIDLength = 200
)

// ValidatePipelineGraph checks what every saved graph holds, drafts
// included: bounded sizes, unique node and edge IDs, known node types, node
// configurations that are JSON objects, plugin nodes naming their plugin,
// and edges between existing nodes. It does not require a generator nor a
// connected sink: the editor saves graphs being built, and the engine
// refuses an incomplete one when it runs it. The web UI and the
// provisioning API both go through it.
func ValidatePipelineGraph(graph *model.PipelineGraph) error {
	if graph == nil {
		return nil
	}
	if len(graph.Nodes) > maxPipelineNodes || len(graph.Edges) > maxPipelineEdges {
		return errors.Wrapf(port.ErrInvalid, "a pipeline holds at most %d nodes and %d edges", maxPipelineNodes, maxPipelineEdges)
	}
	nodes := make(map[string]struct{}, len(graph.Nodes))
	for _, node := range graph.Nodes {
		if !validPipelineID(node.ID) {
			return errors.Wrapf(port.ErrInvalid, "invalid node id %q", node.ID)
		}
		if _, taken := nodes[node.ID]; taken {
			return errors.Wrapf(port.ErrInvalid, "duplicate node id %q", node.ID)
		}
		nodes[node.ID] = struct{}{}
		if err := ValidatePipelineNode(node); err != nil {
			return err
		}
	}
	edges := make(map[string]struct{}, len(graph.Edges))
	for _, edge := range graph.Edges {
		if !validPipelineID(edge.ID) {
			return errors.Wrapf(port.ErrInvalid, "invalid edge id %q", edge.ID)
		}
		if _, taken := edges[edge.ID]; taken {
			return errors.Wrapf(port.ErrInvalid, "duplicate edge id %q", edge.ID)
		}
		edges[edge.ID] = struct{}{}
		for _, end := range []string{edge.Source, edge.Target} {
			if _, ok := nodes[end]; !ok {
				return errors.Wrapf(port.ErrInvalid, "edge %q designates no node %q", edge.ID, end)
			}
		}
	}
	return nil
}

// ValidatePipelineNode checks the type and configuration of one node.
func ValidatePipelineNode(node model.PipelineNode) error {
	if !node.Type.Known() {
		return errors.Wrapf(port.ErrInvalid, "node %q: unknown type %q", node.ID, node.Type)
	}
	if data := bytes.TrimSpace(node.Data); len(data) > 0 && !bytes.Equal(data, []byte("null")) {
		var object map[string]json.RawMessage
		if json.Unmarshal(data, &object) != nil || object == nil {
			return errors.Wrapf(port.ErrInvalid, "node %q: its data is no JSON object", node.ID)
		}
	}
	if node.Type == model.NodeTypePlugin {
		var data model.PluginNodeData
		if json.Unmarshal(node.Data, &data) != nil || strings.TrimSpace(data.PluginName) == "" {
			return errors.Wrapf(port.ErrInvalid, "plugin node %q names no plugin", node.ID)
		}
	}
	return nil
}

func validPipelineID(id string) bool {
	return id != "" && strings.TrimSpace(id) == id && utf8.RuneCountInString(id) <= maxPipelineIDLength
}
