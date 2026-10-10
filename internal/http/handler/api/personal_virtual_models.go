package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/secretcleanup"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
)

// ─── Request / response shapes ───────────────────────────────────────────────

type personalVMResponse struct {
	ID          string               `json:"id"`
	UserID      string               `json:"userId"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Graph       *model.PipelineGraph `json:"graph,omitempty"`
	CreatedAt   string               `json:"createdAt"`
	UpdatedAt   string               `json:"updatedAt"`
}

// ─── List ─────────────────────────────────────────────────────────────────────

func (h *Handler) handleListPersonalVMs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if httpCtx.TenantID(ctx) == "" || user.TenantID() != httpCtx.TenantID(ctx) {
		http.NotFound(w, r)
		return
	}

	vms, err := h.personalVMStore.ListPersonalVirtualModels(ctx, user.ID())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	out := make([]personalVMResponse, 0, len(vms))
	for _, vm := range vms {
		out = append(out, toPVMResponse(vm))
	}
	writeJSON(w, http.StatusOK, out)
}

// ─── Create ───────────────────────────────────────────────────────────────────

func (h *Handler) handleCreatePersonalVM(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if httpCtx.TenantID(ctx) == "" || user.TenantID() != httpCtx.TenantID(ctx) {
		http.NotFound(w, r)
		return
	}

	var req virtualModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if h.rejectGraph(w, r, req.Graph) {
		return
	}

	vm := model.NewPersonalVirtualModel(user.ID(), req.Name, req.Description)
	if req.Graph != nil {
		vm.SetGraph(req.Graph)
	}

	if err := h.personalVMStore.CreatePersonalVirtualModel(ctx, vm); err != nil {
		if errors.Is(err, port.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "a personal virtual model with this name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, toPVMResponse(vm))
}

// ─── Get ──────────────────────────────────────────────────────────────────────

func (h *Handler) handleGetPersonalVM(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if httpCtx.TenantID(ctx) == "" || user.TenantID() != httpCtx.TenantID(ctx) {
		http.NotFound(w, r)
		return
	}

	vmID := model.PersonalVirtualModelID(r.PathValue("vmID"))
	vm, err := h.personalVMStore.GetPersonalVirtualModelByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			writeError(w, http.StatusNotFound, "personal virtual model not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Ensure ownership.
	if vm.UserID() != user.ID() {
		writeError(w, http.StatusNotFound, "personal virtual model not found")
		return
	}

	writeJSON(w, http.StatusOK, toPVMResponse(vm))
}

// ─── Update ───────────────────────────────────────────────────────────────────

func (h *Handler) handleUpdatePersonalVM(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if httpCtx.TenantID(ctx) == "" || user.TenantID() != httpCtx.TenantID(ctx) {
		http.NotFound(w, r)
		return
	}

	vmID := model.PersonalVirtualModelID(r.PathValue("vmID"))
	vm, err := h.personalVMStore.GetPersonalVirtualModelByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			writeError(w, http.StatusNotFound, "personal virtual model not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if vm.UserID() != user.ID() {
		writeError(w, http.StatusNotFound, "personal virtual model not found")
		return
	}

	var req virtualModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	type mutable interface {
		SetDescription(string)
		SetGraph(*model.PipelineGraph)
		SetUpdatedAt(time.Time)
	}

	m, ok := vm.(mutable)
	if !ok {
		writeError(w, http.StatusInternalServerError, "cannot update this virtual model")
		return
	}

	if h.rejectGraph(w, r, req.Graph) {
		return
	}

	oldGraph := vm.Graph()

	if req.Description != "" {
		m.SetDescription(req.Description)
	}
	m.SetGraph(req.Graph)
	m.SetUpdatedAt(time.Now())

	if err := h.personalVMStore.SavePersonalVirtualModel(ctx, vm); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := secretcleanup.PruneRemovedNodes(ctx, h.secretStore, "~:"+string(user.ID()), oldGraph, req.Graph); err != nil {
		slog.ErrorContext(ctx, "could not prune secrets for removed pipeline nodes", slog.Any("error", err))
	}

	writeJSON(w, http.StatusOK, toPVMResponse(vm))
}

// ─── Delete ───────────────────────────────────────────────────────────────────

func (h *Handler) handleDeletePersonalVM(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if httpCtx.TenantID(ctx) == "" || user.TenantID() != httpCtx.TenantID(ctx) {
		http.NotFound(w, r)
		return
	}

	vmID := model.PersonalVirtualModelID(r.PathValue("vmID"))
	// Verify ownership before deleting.
	vm, err := h.personalVMStore.GetPersonalVirtualModelByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			writeError(w, http.StatusNotFound, "personal virtual model not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if vm.UserID() != user.ID() {
		writeError(w, http.StatusNotFound, "personal virtual model not found")
		return
	}

	if err := h.personalVMStore.DeletePersonalVirtualModel(ctx, vmID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := secretcleanup.PruneRemovedNodes(ctx, h.secretStore, "~:"+string(user.ID()), vm.Graph(), nil); err != nil {
		slog.ErrorContext(ctx, "could not prune secrets for deleted personal virtual model", slog.Any("error", err))
	}

	w.WriteHeader(http.StatusNoContent)
}

// ─── Export ───────────────────────────────────────────────────────────────────

func (h *Handler) handleExportPersonalVM(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if httpCtx.TenantID(ctx) == "" || user.TenantID() != httpCtx.TenantID(ctx) {
		http.NotFound(w, r)
		return
	}

	vmID := model.PersonalVirtualModelID(r.PathValue("vmID"))
	vm, err := h.personalVMStore.GetPersonalVirtualModelByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			writeError(w, http.StatusNotFound, "personal virtual model not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if vm.UserID() != user.ID() {
		writeError(w, http.StatusNotFound, "personal virtual model not found")
		return
	}

	bundle := model.PipelineBundle{
		Version:     "1",
		ExportedAt:  time.Now().UTC(),
		Name:        vm.Name(),
		Description: vm.Description(),
		Graph:       vm.Graph(),
	}

	filename := fmt.Sprintf("pipeline-%s.json", sanitizeFilename(vm.Name()))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(bundle)
}

// ─── Import ───────────────────────────────────────────────────────────────────

func (h *Handler) handleImportPersonalVM(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if httpCtx.TenantID(ctx) == "" || user.TenantID() != httpCtx.TenantID(ctx) {
		http.NotFound(w, r)
		return
	}

	var bundle model.PipelineBundle
	if err := json.NewDecoder(r.Body).Decode(&bundle); err != nil {
		writeError(w, http.StatusBadRequest, "invalid bundle JSON")
		return
	}
	if bundle.Name == "" {
		writeError(w, http.StatusBadRequest, "bundle must contain a non-empty name")
		return
	}
	if h.rejectGraph(w, r, bundle.Graph) {
		return
	}

	var vm *model.BasePersonalVirtualModel
	baseName := bundle.Name
	for attempt := 0; attempt <= 10; attempt++ {
		name := baseName
		if attempt > 0 {
			name = fmt.Sprintf("%s (%d)", baseName, attempt)
		}
		vm = model.NewPersonalVirtualModel(user.ID(), name, bundle.Description)
		if bundle.Graph != nil {
			vm.SetGraph(bundle.Graph)
		}
		err := h.personalVMStore.CreatePersonalVirtualModel(ctx, vm)
		if err == nil {
			break
		}
		if errors.Is(err, port.ErrAlreadyExists) {
			if attempt == 10 {
				writeError(w, http.StatusConflict, "a personal virtual model with this name already exists (10 attempts exhausted)")
				return
			}
			continue
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, toPVMResponse(vm))
}

// ─── Personal pipeline node types ─────────────────────────────────────────────

func (h *Handler) handlePersonalPipelineNodeTypes(w http.ResponseWriter, r *http.Request) {
	// Reuse the same node types as org pipelines; the frontend context determines
	// which API endpoints are used for loading/saving the pipeline.
	h.handlePipelineNodeTypes(w, r)
}

// ─── Helper ───────────────────────────────────────────────────────────────────

func toPVMResponse(vm model.PersonalVirtualModel) personalVMResponse {
	return personalVMResponse{
		ID:          string(vm.ID()),
		UserID:      string(vm.UserID()),
		Name:        vm.Name(),
		Description: vm.Description(),
		Graph:       vm.Graph(),
		CreatedAt:   vm.CreatedAt().Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:   vm.UpdatedAt().Format("2006-01-02T15:04:05Z07:00"),
	}
}
