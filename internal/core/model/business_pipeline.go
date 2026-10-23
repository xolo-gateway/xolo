package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

// VirtualModelSettings is the representation of a virtual model. Its graph
// is the one the editor stores; CatalogOverrides is nil for none.
type VirtualModelSettings struct {
	Name             string                `json:"name"`
	Description      string                `json:"description"`
	CatalogOverrides *CatalogOverrides     `json:"catalog_overrides"`
	Graph            PipelineGraphSettings `json:"graph"`
}

// MiddlewareSettings is the representation of a middleware. Targets is
// ignored at runtime when AppliesToAll is set, but kept as written.
type MiddlewareSettings struct {
	Name         string                `json:"name"`
	Description  string                `json:"description"`
	Enabled      bool                  `json:"enabled"`
	Priority     int                   `json:"priority"`
	AppliesToAll bool                  `json:"applies_to_all"`
	Targets      []ModelRef            `json:"targets"`
	Graph        PipelineGraphSettings `json:"graph"`
}

// PipelineGraphSettings is the published form of a PipelineGraph: the same
// nodes and edges, with snake_case edge ports. The data of a node is the
// configuration of its type, as the editor writes it.
type PipelineGraphSettings struct {
	Nodes []PipelineNodeSettings `json:"nodes"`
	Edges []PipelineEdgeSettings `json:"edges"`
}

type PipelineNodeSettings struct {
	ID       string           `json:"id"`
	Type     PipelineNodeType `json:"type"`
	Position NodePosition     `json:"position"`
	Data     json.RawMessage  `json:"data"`
}

type PipelineEdgeSettings struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	SourcePort string `json:"source_port"`
	Target     string `json:"target"`
	TargetPort string `json:"target_port"`
}

// PipelineNodeItem is the configuration of one node, as the node routes
// return it. SecretKeys names the secrets set on a plugin node, never their
// values.
type PipelineNodeItem struct {
	ID         string           `json:"id"`
	Type       PipelineNodeType `json:"type"`
	Data       json.RawMessage  `json:"data"`
	SecretKeys []string         `json:"secret_keys"`
}

// PipelineNodeUpdate replaces the configuration of one node. Secrets are
// write-only: a value sets the key, null deletes it, a key left out is kept.
type PipelineNodeUpdate struct {
	Data    json.RawMessage    `json:"data"`
	Secrets map[string]*string `json:"secrets,omitempty"`
}

// PipelineGraphSettingsOf returns the published form of graph, normalized:
// nodes and edges sorted by ID, a missing node configuration as an empty
// object. A nil graph is an empty one.
func PipelineGraphSettingsOf(graph *PipelineGraph) PipelineGraphSettings {
	out := PipelineGraphSettings{Nodes: []PipelineNodeSettings{}, Edges: []PipelineEdgeSettings{}}
	if graph == nil {
		return out
	}
	for _, n := range graph.Nodes {
		out.Nodes = append(out.Nodes, PipelineNodeSettings{ID: n.ID, Type: n.Type, Position: n.Position, Data: n.Data})
	}
	for _, e := range graph.Edges {
		out.Edges = append(out.Edges, PipelineEdgeSettings{ID: e.ID, Source: e.Source, SourcePort: e.SourcePort, Target: e.Target, TargetPort: e.TargetPort})
	}
	return out.Normalize()
}

// Normalize sorts nodes and edges by ID, replaces a missing node
// configuration by an empty object and canonicalizes the others: neither the
// order of a graph nor the formatting of a configuration carries meaning, so
// a replay in another order or another formatting is a no-op.
func (g PipelineGraphSettings) Normalize() PipelineGraphSettings {
	nodes := slices.Clone(g.Nodes)
	if nodes == nil {
		nodes = []PipelineNodeSettings{}
	}
	for i := range nodes {
		// A configuration that is no JSON keeps its bytes: refusing it is
		// the job of the validation of the graph, which every write runs.
		if canonical, err := CanonicalJSON(nodes[i].Data); err == nil {
			nodes[i].Data = canonical
		}
	}
	slices.SortStableFunc(nodes, func(a, b PipelineNodeSettings) int { return strings.Compare(a.ID, b.ID) })
	edges := slices.Clone(g.Edges)
	if edges == nil {
		edges = []PipelineEdgeSettings{}
	}
	slices.SortStableFunc(edges, func(a, b PipelineEdgeSettings) int { return strings.Compare(a.ID, b.ID) })
	return PipelineGraphSettings{Nodes: nodes, Edges: edges}
}

// Graph returns the graph the settings describe, nil when it has no node: a
// graph without nodes is unconfigured.
func (g PipelineGraphSettings) Graph() *PipelineGraph {
	if len(g.Nodes) == 0 {
		return nil
	}
	out := &PipelineGraph{Nodes: make([]PipelineNode, 0, len(g.Nodes)), Edges: make([]PipelineEdge, 0, len(g.Edges))}
	for _, n := range g.Nodes {
		out.Nodes = append(out.Nodes, PipelineNode{ID: n.ID, Type: n.Type, Position: n.Position, Data: n.Data})
	}
	for _, e := range g.Edges {
		out.Edges = append(out.Edges, PipelineEdge{ID: e.ID, Source: e.Source, SourcePort: e.SourcePort, Target: e.Target, TargetPort: e.TargetPort})
	}
	return out
}

// PluginPlacement designates where the secrets of a plugin node are stored,
// besides their scope: the plugin and the node ID.
type PluginPlacement struct{ Plugin, Node string }

// PluginPlacements lists the placements of the plugin nodes of graph. A node
// whose configuration names no plugin holds no secret and is left out.
func PluginPlacements(graph *PipelineGraph) []PluginPlacement {
	if graph == nil {
		return nil
	}
	var out []PluginPlacement
	for _, node := range graph.Nodes {
		if node.Type != NodeTypePlugin {
			continue
		}
		var data PluginNodeData
		if json.Unmarshal(node.Data, &data) != nil || data.PluginName == "" {
			continue
		}
		out = append(out, PluginPlacement{Plugin: data.PluginName, Node: node.ID})
	}
	return out
}

// NormalizeModelRefs sorts and deduplicates model references, empty when
// there are none.
func NormalizeModelRefs(refs []ModelRef) []ModelRef {
	out := slices.Clone(refs)
	if out == nil {
		out = []ModelRef{}
	}
	compare := func(a, b ModelRef) int {
		if c := strings.Compare(string(a.Kind), string(b.Kind)); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	}
	slices.SortFunc(out, compare)
	return slices.CompactFunc(out, func(a, b ModelRef) bool { return compare(a, b) == 0 })
}

// CanonicalJSON re-encodes raw with sorted object keys, without
// insignificant spaces and with numbers as written; missing or null reads
// as an empty object.
func CanonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return json.RawMessage("{}"), nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var v any
	if err := decoder.Decode(&v); err != nil {
		return nil, err
	}
	if decoder.More() {
		return nil, errors.New("trailing data after JSON value")
	}
	return json.Marshal(v)
}
