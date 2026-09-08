package model

import "encoding/json"

// PortType identifies the type of a pipeline node port.
type PortType string

const (
	PortTypeRequest  PortType = "request"
	PortTypeResponse PortType = "response"
	PortTypeNumber   PortType = "number"
	PortTypeString   PortType = "string"
	PortTypeBoolean  PortType = "boolean"
)

// PortDescriptor describes a named, typed input or output port on a pipeline node.
type PortDescriptor struct {
	Name     string   `json:"name"`
	Type     PortType `json:"type"`
	Required bool     `json:"required,omitempty"`
}

// PipelineNodeType identifies the kind of node in a pipeline graph.
type PipelineNodeType string

const (
	// Built-in node types (no gRPC binary).
	NodeTypeGenerator PipelineNodeType = "generator" // source: outputs request
	NodeTypeSink      PipelineNodeType = "sink"       // sink: inputs response
	NodeTypeModel     PipelineNodeType = "model"      // calls a real LLM
	NodeTypeValue     PipelineNodeType = "value"      // static value emitter
	NodeTypeModelRef  PipelineNodeType = "model_ref"  // emits the name of a model chosen from the catalog

	// Built-in logic and utility nodes (no gRPC binary).
	NodeTypeCompare       PipelineNodeType = "compare"        // number vs threshold -> boolean
	NodeTypeSelect        PipelineNodeType = "select"         // boolean ? string : string
	NodeTypeMath          PipelineNodeType = "math"           // combines numbers
	NodeTypeSample        PipelineNodeType = "sample"         // percentage split -> boolean
	NodeTypeContext       PipelineNodeType = "context"        // exposes request context (user, org, time)
	NodeTypeTrace         PipelineNodeType = "trace"          // records connected values as an event
	NodeTypeNote          PipelineNodeType = "note"           // free text, editor only
	NodeTypeModelFallback PipelineNodeType = "model_fallback" // ordered list of models, next on failure

	// gRPC plugin node.
	NodeTypePlugin PipelineNodeType = "plugin"
)

// PipelineGraph is the dataflow graph stored inside a VirtualModel.
type PipelineGraph struct {
	Nodes []PipelineNode `json:"nodes"`
	Edges []PipelineEdge `json:"edges"`
}

// NodeIDs returns the ID of every node in the graph.
func (g *PipelineGraph) NodeIDs() []string {
	if g == nil {
		return nil
	}
	ids := make([]string, len(g.Nodes))
	for i, n := range g.Nodes {
		ids[i] = n.ID
	}
	return ids
}

// PipelineNode is a single node in a pipeline graph.
type PipelineNode struct {
	ID       string           `json:"id"`
	Type     PipelineNodeType `json:"type"`
	Position NodePosition     `json:"position"`
	// Data holds the node-type-specific configuration (JSON).
	Data json.RawMessage `json:"data,omitempty"`
}

// NodePosition is the visual position in the React Flow canvas.
type NodePosition struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// PipelineEdge connects a source output port to a target input port.
type PipelineEdge struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	SourcePort string `json:"sourcePort"`
	Target     string `json:"target"`
	TargetPort string `json:"targetPort"`
}

// PluginNodeData is the Data payload for NodeTypePlugin.
type PluginNodeData struct {
	PluginName string          `json:"pluginName"`
	Config     json.RawMessage `json:"config,omitempty"`
}

// ModelNodeData is the Data payload for NodeTypeModel.
// ProxyName is the static model proxy name used when the model_name input port
// is not connected. If the port is connected, the runtime value takes precedence.
//
// Passthrough marks the node as resolving the model that was actually requested
// by the caller (ExecutionContext.TargetModelName) instead of a fixed proxy name.
// It is used by Middleware pipelines, which wrap arbitrary target models
// dynamically. When Passthrough is true, ProxyName is ignored.
type ModelNodeData struct {
	ProxyName   string `json:"proxyName,omitempty"`
	Passthrough bool   `json:"passthrough,omitempty"`
}

// ValueNodeData is the Data payload for NodeTypeValue.
type ValueNodeData struct {
	// PortType determines the output port type and how Value is interpreted.
	PortType string `json:"portType"` // "string" | "number" | "boolean"
	// Value is the raw string representation of the value (parsed at runtime).
	Value string `json:"value"`
}


// ModelRefNodeData is the Data payload for NodeTypeModelRef.
// The node emits ProxyName, chosen in the editor among the models and virtual
// models the organisation exposes, on its "model_name" string output port. It
// spares the user from typing a model name and lets the editor show a picker
// wherever a pipeline needs one: a model node, a router, a classifier.
type ModelRefNodeData struct {
	ProxyName string `json:"proxyName"`
}

// CompareNodeData is the Data payload for NodeTypeCompare.
type CompareNodeData struct {
	// Op is one of gt, gte, lt, lte, eq, ne.
	Op string `json:"op"`
	// Threshold is used when the threshold port is not connected.
	Threshold float64 `json:"threshold"`
}

// MathNodeData is the Data payload for NodeTypeMath.
type MathNodeData struct {
	// Op is one of sum, avg, min, max, product, weighted.
	Op string `json:"op"`
	// Weights apply to inputs a, b, c, d for the weighted op.
	Weights []float64 `json:"weights,omitempty"`
}

// SampleNodeData is the Data payload for NodeTypeSample.
type SampleNodeData struct {
	// Percent of requests selected, 0 to 100.
	Percent float64 `json:"percent"`
	// Key decides what the split is stable on: random, user or token.
	Key string `json:"key"`
	// Salt changes the assignment without changing the percentage.
	Salt string `json:"salt,omitempty"`
}

// ContextNodeData is the Data payload for NodeTypeContext.
type ContextNodeData struct {
	// Timezone (IANA) used for hour and weekday; defaults to UTC.
	Timezone string `json:"timezone,omitempty"`
}

// TraceNodeData is the Data payload for NodeTypeTrace.
// Inputs declares the ports the node offers; each connected value is recorded
// under its port name. The list is free so a trace can capture exactly the
// values a pipeline author wants to see, whatever their number and type.
type TraceNodeData struct {
	Label    string          `json:"label,omitempty"`
	Severity string          `json:"severity,omitempty"`
	Inputs   []TraceInputDef `json:"inputs,omitempty"`
}

// TraceInputDef names one input port of a trace node.
type TraceInputDef struct {
	Name     string `json:"name"`
	PortType string `json:"portType"`
}

// NoteNodeData is the Data payload for NodeTypeNote.
type NoteNodeData struct {
	Text string `json:"text"`
}

// ModelFallbackNodeData is the Data payload for NodeTypeModelFallback.
type ModelFallbackNodeData struct {
	// Models are tried in order; the next one is called when the previous fails.
	Models []string `json:"models"`
}
