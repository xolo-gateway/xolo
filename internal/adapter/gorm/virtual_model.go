package gorm

import (
	"encoding/json"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

type VirtualModel struct {
	ID          string `gorm:"primaryKey;autoIncrement:false"`
	OrgID       string `gorm:"uniqueIndex:idx_org_name;not null"`
	Name        string `gorm:"uniqueIndex:idx_org_name;not null"`
	Description string
	// GraphJSON stores the PipelineGraph as JSON. Empty string or "{}" means no graph.
	GraphJSON string `gorm:"type:text;default:'{}'"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

type wrappedVirtualModel struct {
	m *VirtualModel
}

func (w *wrappedVirtualModel) ID() model.VirtualModelID { return model.VirtualModelID(w.m.ID) }
func (w *wrappedVirtualModel) EntityID() string         { return w.m.ID }
func (w *wrappedVirtualModel) OrgID() model.OrgID       { return model.OrgID(w.m.OrgID) }
func (w *wrappedVirtualModel) Name() string             { return w.m.Name }
func (w *wrappedVirtualModel) Description() string      { return w.m.Description }
func (w *wrappedVirtualModel) CreatedAt() time.Time     { return w.m.CreatedAt }
func (w *wrappedVirtualModel) UpdatedAt() time.Time     { return w.m.UpdatedAt }

// Setters — needed by API update handler.
func (w *wrappedVirtualModel) SetName(v string) { w.m.Name = v }
func (w *wrappedVirtualModel) SetDescription(v string) {
	w.m.Description = v
}
func (w *wrappedVirtualModel) SetGraph(v *model.PipelineGraph) {
	if v == nil {
		w.m.GraphJSON = "{}"
		return
	}
	if data, err := json.Marshal(v); err == nil {
		w.m.GraphJSON = string(data)
	}
}
func (w *wrappedVirtualModel) SetUpdatedAt(v time.Time) { w.m.UpdatedAt = v }

func (w *wrappedVirtualModel) Graph() *model.PipelineGraph {
	if w.m.GraphJSON == "" || w.m.GraphJSON == "{}" {
		return nil
	}
	var g model.PipelineGraph
	if err := json.Unmarshal([]byte(w.m.GraphJSON), &g); err != nil {
		return nil
	}
	// A graph with no nodes is treated as unconfigured.
	if len(g.Nodes) == 0 {
		return nil
	}
	return &g
}

var (
	_ model.VirtualModel   = &wrappedVirtualModel{}
	_ model.PipelineEntity = &wrappedVirtualModel{}
)

func fromVirtualModel(vm model.VirtualModel) *VirtualModel {
	graphJSON := "{}"
	if g := vm.Graph(); g != nil {
		if data, err := json.Marshal(g); err == nil {
			graphJSON = string(data)
		}
	}
	return &VirtualModel{
		ID:          string(vm.ID()),
		OrgID:       string(vm.OrgID()),
		Name:        vm.Name(),
		Description: vm.Description(),
		GraphJSON:   graphJSON,
		CreatedAt:   vm.CreatedAt(),
		UpdatedAt:   vm.UpdatedAt(),
	}
}
