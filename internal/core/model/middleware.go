package model

import (
	"encoding/json"
	"time"

	"github.com/rs/xid"
)

type MiddlewareID string

func NewMiddlewareID() MiddlewareID {
	return MiddlewareID(xid.New().String())
}

// ModelRefKind identifies the kind of model a middleware target points to.
type ModelRefKind string

const (
	ModelRefKindLLM     ModelRefKind = "llm"     // provider-backed model (LLMModelID)
	ModelRefKindVirtual ModelRefKind = "virtual" // virtual model (VirtualModelID)
)

// ModelRef references a single model targeted by a middleware.
type ModelRef struct {
	Kind ModelRefKind `json:"kind"`
	ID   string       `json:"id"`
}

// Middleware is a pipeline that is applied transparently and dynamically to the
// models it targets, at request time — as opposed to a VirtualModel, which is
// exposed as a distinct named model. Several middlewares applicable to the same
// model are chained by ascending Priority (lower runs first / outermost).
type Middleware interface {
	WithID[MiddlewareID]
	PipelineEntity

	Enabled() bool
	Priority() int
	// AppliesToAll reports whether the middleware wraps every model of the org
	// (real and virtual), ignoring Targets.
	AppliesToAll() bool
	// Targets is the explicit list of models wrapped when AppliesToAll is false.
	Targets() []ModelRef
}

type BaseMiddleware struct {
	id           MiddlewareID
	orgID        OrgID
	name         string
	description  string
	enabled      bool
	priority     int
	appliesToAll bool
	targets      []ModelRef
	graph        *PipelineGraph
	createdAt    time.Time
	updatedAt    time.Time
}

func (m *BaseMiddleware) ID() MiddlewareID      { return m.id }
func (m *BaseMiddleware) EntityID() string      { return string(m.id) }
func (m *BaseMiddleware) OrgID() OrgID          { return m.orgID }
func (m *BaseMiddleware) Name() string          { return m.name }
func (m *BaseMiddleware) Description() string   { return m.description }
func (m *BaseMiddleware) Enabled() bool         { return m.enabled }
func (m *BaseMiddleware) Priority() int         { return m.priority }
func (m *BaseMiddleware) AppliesToAll() bool    { return m.appliesToAll }
func (m *BaseMiddleware) Targets() []ModelRef   { return m.targets }
func (m *BaseMiddleware) Graph() *PipelineGraph { return m.graph }
func (m *BaseMiddleware) CreatedAt() time.Time  { return m.createdAt }
func (m *BaseMiddleware) UpdatedAt() time.Time  { return m.updatedAt }

// SetID replaces the generated ID by one chosen by a provisioning client.
func (m *BaseMiddleware) SetID(v MiddlewareID) { m.id = v }

func (m *BaseMiddleware) SetName(v string)          { m.name = v }
func (m *BaseMiddleware) SetDescription(v string)   { m.description = v }
func (m *BaseMiddleware) SetEnabled(v bool)         { m.enabled = v }
func (m *BaseMiddleware) SetPriority(v int)         { m.priority = v }
func (m *BaseMiddleware) SetAppliesToAll(v bool)    { m.appliesToAll = v }
func (m *BaseMiddleware) SetTargets(v []ModelRef)   { m.targets = v }
func (m *BaseMiddleware) SetGraph(v *PipelineGraph) { m.graph = v }
func (m *BaseMiddleware) SetUpdatedAt(v time.Time)  { m.updatedAt = v }

var (
	_ Middleware     = &BaseMiddleware{}
	_ PipelineEntity = &BaseMiddleware{}
)

// NewMiddleware creates a middleware pre-seeded with a default pipeline graph:
// generator → model(passthrough) → sink. The passthrough model node resolves,
// at request time, the model actually requested by the caller — so a fresh
// middleware is a transparent no-op until plugin nodes are inserted between the
// generator and the model node.
func NewMiddleware(orgID OrgID, name, description string) *BaseMiddleware {
	now := time.Now()
	return &BaseMiddleware{
		id:           NewMiddlewareID(),
		orgID:        orgID,
		name:         name,
		description:  description,
		enabled:      true,
		priority:     0,
		appliesToAll: false,
		targets:      nil,
		graph:        DefaultMiddlewareGraph(),
		createdAt:    now,
		updatedAt:    now,
	}
}

// DefaultMiddlewareGraph returns a minimal passthrough pipeline graph.
func DefaultMiddlewareGraph() *PipelineGraph {
	modelData, _ := json.Marshal(ModelNodeData{Passthrough: true})
	return &PipelineGraph{
		Nodes: []PipelineNode{
			{ID: "gen", Type: NodeTypeGenerator, Position: NodePosition{X: 80, Y: 200}},
			{ID: "model", Type: NodeTypeModel, Position: NodePosition{X: 340, Y: 200}, Data: modelData},
			{ID: "sink", Type: NodeTypeSink, Position: NodePosition{X: 600, Y: 200}},
		},
		Edges: []PipelineEdge{
			{ID: "gen-model", Source: "gen", SourcePort: "request", Target: "model", TargetPort: "request"},
			{ID: "model-sink", Source: "model", SourcePort: "response", Target: "sink", TargetPort: "response"},
		},
	}
}
