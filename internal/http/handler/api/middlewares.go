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
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
)

// mwResource adapts the middleware store to the generic pipeline graph handlers.
func (h *Handler) mwResource() pipelineResource {
	return pipelineResource{
		get: func(ctx context.Context, id string) (model.PipelineEntity, error) {
			mw, err := h.middlewareStore.GetMiddlewareByID(ctx, model.MiddlewareID(id))
			if err != nil {
				return nil, err
			}
			return mw.(model.PipelineEntity), nil
		},
		save: func(ctx context.Context, e model.PipelineEntity) error {
			return h.middlewareStore.SaveMiddleware(ctx, e.(model.Middleware))
		},
		readPerm:  rbac.PermMiddlewaresRead,
		writePerm: rbac.PermMiddlewaresWrite,
		notFound:  "middleware not found",
	}
}

type middlewareResponse struct {
	pipelineEntityResponse
	Enabled      bool             `json:"enabled"`
	Priority     int              `json:"priority"`
	AppliesToAll bool             `json:"appliesToAll"`
	Targets      []model.ModelRef `json:"targets"`
}

func toMiddlewareResponse(mw model.Middleware) middlewareResponse {
	return middlewareResponse{
		pipelineEntityResponse: toEntityResponse(mw),
		Enabled:                mw.Enabled(),
		Priority:               mw.Priority(),
		AppliesToAll:           mw.AppliesToAll(),
		Targets:                mw.Targets(),
	}
}

type middlewareCreateRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type middlewareSettingsRequest struct {
	Name         *string           `json:"name,omitempty"`
	Description  *string           `json:"description,omitempty"`
	Enabled      *bool             `json:"enabled,omitempty"`
	Priority     *int              `json:"priority,omitempty"`
	AppliesToAll *bool             `json:"appliesToAll,omitempty"`
	Targets      *[]model.ModelRef `json:"targets,omitempty"`
}

// ─── List / Create ──────────────────────────────────────────────────────────

func (h *Handler) handleListMiddlewares(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	org, ok := h.orgForPerm(w, r, rbac.PermMiddlewaresRead)
	if !ok {
		return
	}

	mws, err := h.middlewareStore.ListMiddlewares(ctx, org.ID())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	out := make([]middlewareResponse, 0, len(mws))
	for _, mw := range mws {
		out = append(out, toMiddlewareResponse(mw))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) handleCreateMiddleware(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	org, ok := h.orgForPerm(w, r, rbac.PermMiddlewaresWrite)
	if !ok {
		return
	}

	var req middlewareCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	mw := model.NewMiddleware(org.ID(), req.Name, req.Description)
	if err := h.middlewareStore.CreateMiddleware(ctx, mw); err != nil {
		if errors.Is(err, port.ErrAlreadyExists) {
			http.Error(w, "a middleware with this name already exists", http.StatusConflict)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, toMiddlewareResponse(mw))
}

// ─── Get / Update graph (generic) ─────────────────────────────────────────────

func (h *Handler) handleGetMiddleware(w http.ResponseWriter, r *http.Request) {
	h.serveGetEntity(w, r, h.mwResource(), r.PathValue("mwID"))
}

func (h *Handler) handleUpdateMiddleware(w http.ResponseWriter, r *http.Request) {
	h.serveUpdateEntity(w, r, h.mwResource(), r.PathValue("mwID"))
}

// ─── Update settings (middleware-specific fields) ─────────────────────────────

func (h *Handler) handleUpdateMiddlewareSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	mwID := model.MiddlewareID(r.PathValue("mwID"))

	mw, err := h.middlewareStore.GetMiddlewareByID(ctx, mwID)
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "middleware not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if allowed, err := h.hasPermission(ctx, mw.OrgID(), rbac.PermMiddlewaresWrite); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if !allowed {
		http.Error(w, "middleware not found", http.StatusNotFound)
		return
	}

	var req middlewareSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	m, ok := mw.(middlewareMutable)
	if !ok {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	applyMiddlewareSettings(m, req)
	if err := h.middlewareStore.SaveMiddleware(ctx, mw); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, toMiddlewareResponse(mw))
}

// middlewareMutable is the set of setters shared by every middleware
// implementation (domain and GORM wrapper).
type middlewareMutable interface {
	SetName(string)
	SetDescription(string)
	SetEnabled(bool)
	SetPriority(int)
	SetAppliesToAll(bool)
	SetTargets([]model.ModelRef)
	SetUpdatedAt(time.Time)
}

func applyMiddlewareSettings(m middlewareMutable, req middlewareSettingsRequest) {
	if req.Name != nil {
		m.SetName(*req.Name)
	}
	if req.Description != nil {
		m.SetDescription(*req.Description)
	}
	if req.Enabled != nil {
		m.SetEnabled(*req.Enabled)
	}
	if req.Priority != nil {
		m.SetPriority(*req.Priority)
	}
	if req.AppliesToAll != nil {
		m.SetAppliesToAll(*req.AppliesToAll)
	}
	if req.Targets != nil {
		m.SetTargets(*req.Targets)
	}
	m.SetUpdatedAt(time.Now())
}

// ─── Delete ───────────────────────────────────────────────────────────────────

func (h *Handler) handleDeleteMiddleware(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	mwID := model.MiddlewareID(r.PathValue("mwID"))

	mw, err := h.middlewareStore.GetMiddlewareByID(ctx, mwID)
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "middleware not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if allowed, err := h.hasPermission(ctx, mw.OrgID(), rbac.PermMiddlewaresWrite); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if !allowed {
		http.Error(w, "middleware not found", http.StatusNotFound)
		return
	}

	if err := h.middlewareStore.DeleteMiddleware(ctx, mwID); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := secretcleanup.PruneRemovedNodes(ctx, h.secretStore, mw.Graph(), nil); err != nil {
		slog.ErrorContext(ctx, "could not prune secrets for deleted middleware", slog.Any("error", err))
	}

	w.WriteHeader(http.StatusNoContent)
}

// orgForPerm resolves the {orgSlug} route var and enforces a permission.
func (h *Handler) orgForPerm(w http.ResponseWriter, r *http.Request, perm rbac.Permission) (model.Organization, bool) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")

	org, err := h.orgStore.GetOrgBySlug(ctx, httpCtx.TenantID(ctx), orgSlug)
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "org not found", http.StatusNotFound)
			return nil, false
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}

	if allowed, err := h.hasPermission(ctx, org.ID(), perm); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	} else if !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, false
	}

	return org, true
}
