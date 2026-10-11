package model

import (
	"time"

	"github.com/rs/xid"
)

type PersonalVirtualModelID string

func NewPersonalVirtualModelID() PersonalVirtualModelID {
	return PersonalVirtualModelID(xid.New().String())
}

type PersonalVirtualModel interface {
	WithID[PersonalVirtualModelID]

	UserID() UserID
	Name() string
	Description() string
	// TeamID is the team the model's requests are charged to, picked by its
	// owner among its teams. Every model the graph reaches is checked against
	// that team's access: a personal model delegates nothing. Empty when the
	// owner picked none, which only works when a single path exists.
	TeamID() TeamID
	// Graph returns the pipeline graph. Nil means no pipeline configured.
	Graph() *PipelineGraph
	// CatalogOverrides returns what the user declared for the model
	// catalogue, or nil when the card is left to derivation.
	CatalogOverrides() *CatalogOverrides
	CreatedAt() time.Time
	UpdatedAt() time.Time
}

type BasePersonalVirtualModel struct {
	id          PersonalVirtualModelID
	userID      UserID
	name        string
	description string
	teamID      TeamID
	graph       *PipelineGraph
	overrides   *CatalogOverrides
	createdAt   time.Time
	updatedAt   time.Time
}

func (m *BasePersonalVirtualModel) ID() PersonalVirtualModelID          { return m.id }
func (m *BasePersonalVirtualModel) UserID() UserID                      { return m.userID }
func (m *BasePersonalVirtualModel) Name() string                        { return m.name }
func (m *BasePersonalVirtualModel) Description() string                 { return m.description }
func (m *BasePersonalVirtualModel) TeamID() TeamID                      { return m.teamID }
func (m *BasePersonalVirtualModel) Graph() *PipelineGraph               { return m.graph }
func (m *BasePersonalVirtualModel) CatalogOverrides() *CatalogOverrides { return m.overrides }
func (m *BasePersonalVirtualModel) CreatedAt() time.Time                { return m.createdAt }
func (m *BasePersonalVirtualModel) UpdatedAt() time.Time                { return m.updatedAt }

func (m *BasePersonalVirtualModel) SetName(v string)                        { m.name = v }
func (m *BasePersonalVirtualModel) SetDescription(v string)                 { m.description = v }
func (m *BasePersonalVirtualModel) SetTeamID(v TeamID)                      { m.teamID = v }
func (m *BasePersonalVirtualModel) SetGraph(v *PipelineGraph)               { m.graph = v }
func (m *BasePersonalVirtualModel) SetCatalogOverrides(v *CatalogOverrides) { m.overrides = v }
func (m *BasePersonalVirtualModel) SetUpdatedAt(v time.Time)                { m.updatedAt = v }

var _ PersonalVirtualModel = &BasePersonalVirtualModel{}

func NewPersonalVirtualModel(userID UserID, name, description string) *BasePersonalVirtualModel {
	now := time.Now()
	return &BasePersonalVirtualModel{
		id:          NewPersonalVirtualModelID(),
		userID:      userID,
		name:        name,
		description: description,
		createdAt:   now,
		updatedAt:   now,
	}
}
