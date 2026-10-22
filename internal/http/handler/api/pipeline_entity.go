package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/core/secretcleanup"
)

// pipelineEntityResponse is the JSON shape returned for any PipelineEntity
// (virtual model, middleware) that carries an editable pipeline graph.
type pipelineEntityResponse struct {
	ID          string               `json:"id"`
	OrgID       string               `json:"orgId"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Graph       *model.PipelineGraph `json:"graph,omitempty"`
	CreatedAt   string               `json:"createdAt"`
	UpdatedAt   string               `json:"updatedAt"`
}

func toEntityResponse(e model.PipelineEntity) pipelineEntityResponse {
	return pipelineEntityResponse{
		ID:          e.EntityID(),
		OrgID:       string(e.OrgID()),
		Name:        e.Name(),
		Description: e.Description(),
		Graph:       e.Graph(),
		CreatedAt:   e.CreatedAt().Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:   e.UpdatedAt().Format("2006-01-02T15:04:05Z07:00"),
	}
}

// graphUpdateRequest is the payload accepted by the generic graph update handler.
type graphUpdateRequest struct {
	Description string               `json:"description"`
	Graph       *model.PipelineGraph `json:"graph,omitempty"`
}

// pipelineResource abstracts a store of PipelineEntity for the generic pipeline
// graph HTTP handlers, so virtual models and middlewares share the exact same
// GET/PUT logic (serialization, RBAC, secret cleanup).
type pipelineResource struct {
	get       func(ctx context.Context, id string) (model.PipelineEntity, error)
	save      func(ctx context.Context, e model.PipelineEntity) error
	readPerm  rbac.Permission
	writePerm rbac.Permission
	notFound  string
}

// serveGetEntity returns the entity (with its graph) after a read-permission check.
func (h *Handler) serveGetEntity(w http.ResponseWriter, r *http.Request, res pipelineResource, id string) {
	ctx := r.Context()

	e, err := res.get(ctx, id)
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, res.notFound, http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if allowed, err := h.hasPermission(r, e.OrgID(), res.readPerm); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if !allowed {
		http.Error(w, res.notFound, http.StatusNotFound)
		return
	}

	writeJSON(w, http.StatusOK, toEntityResponse(e))
}

// rejectLocked answers 403 when err is a write refused because the resource
// is locked — reserved to the control plane by the ownership policy, or
// frozen by its deletion — and reports whether it did.
func rejectLocked(w http.ResponseWriter, err error) bool {
	if errors.Is(err, port.ErrOwnershipDenied) || errors.Is(err, port.ErrResourceDeleted) {
		http.Error(w, err.Error(), http.StatusForbidden)
		return true
	}
	return false
}

// serveUpdateEntity updates the entity's description and/or graph after a
// write-permission check, then prunes secrets tied to removed nodes.
func (h *Handler) serveUpdateEntity(w http.ResponseWriter, r *http.Request, res pipelineResource, id string) {
	ctx := r.Context()

	e, err := res.get(ctx, id)
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, res.notFound, http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if allowed, err := h.hasPermission(r, e.OrgID(), res.writePerm); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if !allowed {
		http.Error(w, res.notFound, http.StatusNotFound)
		return
	}

	var req graphUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if h.rejectGraph(w, r, req.Graph) {
		return
	}

	oldGraph := e.Graph()

	if req.Description != "" {
		e.SetDescription(req.Description)
	}
	e.SetGraph(req.Graph)
	e.SetUpdatedAt(time.Now())

	if err := res.save(ctx, e); err != nil {
		if rejectLocked(w, err) {
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := secretcleanup.PruneRemovedNodes(ctx, h.secretStore, string(e.OrgID()), oldGraph, req.Graph); err != nil {
		slog.ErrorContext(ctx, "could not prune secrets for removed pipeline nodes", slog.Any("error", err))
	}

	writeJSON(w, http.StatusOK, toEntityResponse(e))
}
