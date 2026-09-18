package pipelinetest

import (
	"encoding/json"
	"fmt"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// GraphBuilder is a fluent builder for model.PipelineGraph, reducing the
// boilerplate of writing out node/edge literals by hand in tests.
type GraphBuilder struct {
	nodes []model.PipelineNode
	edges []model.PipelineEdge
}

// NewGraph creates an empty GraphBuilder.
func NewGraph() *GraphBuilder {
	return &GraphBuilder{}
}

// Generator adds a generator node.
func (b *GraphBuilder) Generator(id string) *GraphBuilder {
	b.nodes = append(b.nodes, model.PipelineNode{ID: id, Type: model.NodeTypeGenerator})
	return b
}

// Sink adds a sink node.
func (b *GraphBuilder) Sink(id string) *GraphBuilder {
	b.nodes = append(b.nodes, model.PipelineNode{ID: id, Type: model.NodeTypeSink})
	return b
}

// Plugin adds a plugin node referencing pluginName.
func (b *GraphBuilder) Plugin(id, pluginName string) *GraphBuilder {
	b.nodes = append(b.nodes, model.PipelineNode{
		ID:   id,
		Type: model.NodeTypePlugin,
		Data: MustJSON(model.PluginNodeData{PluginName: pluginName}),
	})
	return b
}

// Model adds a model node whose proxy model name is resolved dynamically via
// its "model_name" input port.
func (b *GraphBuilder) Model(id string) *GraphBuilder {
	b.nodes = append(b.nodes, model.PipelineNode{ID: id, Type: model.NodeTypeModel})
	return b
}

// ModelWithProxy adds a model node with a static proxy model name.
func (b *GraphBuilder) ModelWithProxy(id, proxyName string) *GraphBuilder {
	b.nodes = append(b.nodes, model.PipelineNode{
		ID:   id,
		Type: model.NodeTypeModel,
		Data: MustJSON(model.ModelNodeData{ProxyName: proxyName}),
	})
	return b
}

// ModelPassthrough adds a passthrough model node, which resolves the model
// requested by the caller (ExecutionContext.TargetModelName) or the next pending
// middleware. Used to test Middleware pipelines.
func (b *GraphBuilder) ModelPassthrough(id string) *GraphBuilder {
	b.nodes = append(b.nodes, model.PipelineNode{
		ID:   id,
		Type: model.NodeTypeModel,
		Data: MustJSON(model.ModelNodeData{Passthrough: true}),
	})
	return b
}

// Value adds a value node holding a static typed value.
func (b *GraphBuilder) Value(id, portType, value string) *GraphBuilder {
	b.nodes = append(b.nodes, model.PipelineNode{
		ID:   id,
		Type: model.NodeTypeValue,
		Data: MustJSON(model.ValueNodeData{PortType: portType, Value: value}),
	})
	return b
}

// Block adds a block node rejecting the request with message when its
// condition port is true.
func (b *GraphBuilder) Block(id, message string) *GraphBuilder {
	b.nodes = append(b.nodes, model.PipelineNode{
		ID:   id,
		Type: model.NodeTypeBlock,
		Data: MustJSON(model.BlockNodeData{Message: message}),
	})
	return b
}

// Edge connects srcID's srcPort output to dstID's dstPort input.
func (b *GraphBuilder) Edge(srcID, srcPort, dstID, dstPort string) *GraphBuilder {
	b.edges = append(b.edges, model.PipelineEdge{
		ID:         fmt.Sprintf("e%d", len(b.edges)+1),
		Source:     srcID,
		SourcePort: srcPort,
		Target:     dstID,
		TargetPort: dstPort,
	})
	return b
}

// Build returns the assembled PipelineGraph.
func (b *GraphBuilder) Build() *model.PipelineGraph {
	return &model.PipelineGraph{Nodes: b.nodes, Edges: b.edges}
}

// MustJSON marshals v to JSON, panicking on error. Useful for building
// node Data payloads outside of GraphBuilder.
func MustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
