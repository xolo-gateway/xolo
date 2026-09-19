package gorm

import (
	"encoding/json"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

type PersonalVirtualModel struct {
	ID          string `gorm:"primaryKey;autoIncrement:false"`
	UserID      string `gorm:"uniqueIndex:idx_pvm_user_name;not null"`
	Name        string `gorm:"uniqueIndex:idx_pvm_user_name;not null"`
	Description string
	GraphJSON   string `gorm:"type:text;default:'{}'"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type wrappedPersonalVirtualModel struct {
	m *PersonalVirtualModel
}

func (w *wrappedPersonalVirtualModel) ID() model.PersonalVirtualModelID {
	return model.PersonalVirtualModelID(w.m.ID)
}
func (w *wrappedPersonalVirtualModel) UserID() model.UserID { return model.UserID(w.m.UserID) }
func (w *wrappedPersonalVirtualModel) Name() string         { return w.m.Name }
func (w *wrappedPersonalVirtualModel) Description() string  { return w.m.Description }
func (w *wrappedPersonalVirtualModel) CreatedAt() time.Time { return w.m.CreatedAt }
func (w *wrappedPersonalVirtualModel) UpdatedAt() time.Time { return w.m.UpdatedAt }

func (w *wrappedPersonalVirtualModel) SetName(v string)         { w.m.Name = v }
func (w *wrappedPersonalVirtualModel) SetDescription(v string)  { w.m.Description = v }
func (w *wrappedPersonalVirtualModel) SetUpdatedAt(v time.Time) { w.m.UpdatedAt = v }

func (w *wrappedPersonalVirtualModel) SetGraph(v *model.PipelineGraph) {
	if v == nil {
		w.m.GraphJSON = "{}"
		return
	}
	if data, err := json.Marshal(v); err == nil {
		w.m.GraphJSON = string(data)
	}
}

func (w *wrappedPersonalVirtualModel) Graph() *model.PipelineGraph {
	if w.m.GraphJSON == "" || w.m.GraphJSON == "{}" {
		return nil
	}
	var g model.PipelineGraph
	if err := json.Unmarshal([]byte(w.m.GraphJSON), &g); err != nil {
		return nil
	}
	if len(g.Nodes) == 0 {
		return nil
	}
	return &g
}

var _ model.PersonalVirtualModel = &wrappedPersonalVirtualModel{}

func fromPersonalVirtualModel(vm model.PersonalVirtualModel) *PersonalVirtualModel {
	graphJSON := "{}"
	if g := vm.Graph(); g != nil {
		if data, err := json.Marshal(g); err == nil {
			graphJSON = string(data)
		}
	}
	return &PersonalVirtualModel{
		ID:          string(vm.ID()),
		UserID:      string(vm.UserID()),
		Name:        vm.Name(),
		Description: vm.Description(),
		GraphJSON:   graphJSON,
		CreatedAt:   vm.CreatedAt(),
		UpdatedAt:   vm.UpdatedAt(),
	}
}
