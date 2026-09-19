package model

import (
	"time"

	"github.com/rs/xid"
)

type VirtualModelID string

func NewVirtualModelID() VirtualModelID {
	return VirtualModelID(xid.New().String())
}

type VirtualModel interface {
	WithID[VirtualModelID]

	OrgID() OrgID
	Name() string
	Description() string
	// Graph returns the pipeline graph definition for this virtual model.
	// A nil graph means the virtual model has no pipeline configured and
	// cannot be resolved by the pipeline engine.
	Graph() *PipelineGraph
	CreatedAt() time.Time
	UpdatedAt() time.Time
}

type BaseVirtualModel struct {
	id          VirtualModelID
	orgID       OrgID
	name        string
	description string
	graph       *PipelineGraph
	createdAt   time.Time
	updatedAt   time.Time
}

func (m *BaseVirtualModel) ID() VirtualModelID    { return m.id }
func (m *BaseVirtualModel) EntityID() string      { return string(m.id) }
func (m *BaseVirtualModel) OrgID() OrgID          { return m.orgID }
func (m *BaseVirtualModel) Name() string          { return m.name }
func (m *BaseVirtualModel) Description() string   { return m.description }
func (m *BaseVirtualModel) Graph() *PipelineGraph { return m.graph }
func (m *BaseVirtualModel) CreatedAt() time.Time  { return m.createdAt }
func (m *BaseVirtualModel) UpdatedAt() time.Time  { return m.updatedAt }

func (m *BaseVirtualModel) SetName(v string)          { m.name = v }
func (m *BaseVirtualModel) SetDescription(v string)   { m.description = v }
func (m *BaseVirtualModel) SetGraph(v *PipelineGraph) { m.graph = v }
func (m *BaseVirtualModel) SetUpdatedAt(v time.Time)  { m.updatedAt = v }

var (
	_ VirtualModel   = &BaseVirtualModel{}
	_ PipelineEntity = &BaseVirtualModel{}
)

func NewVirtualModel(orgID OrgID, name, description string) *BaseVirtualModel {
	now := time.Now()
	return &BaseVirtualModel{
		id:          NewVirtualModelID(),
		orgID:       orgID,
		name:        name,
		description: description,
		createdAt:   now,
		updatedAt:   now,
	}
}
