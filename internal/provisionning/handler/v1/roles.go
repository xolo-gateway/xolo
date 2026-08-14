package v1

import (
	"net/http"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

func (h *Handler) handleListRoles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	org, ok := h.resolveOrganization(w, r)
	if !ok {
		return
	}

	roles, err := h.provisioning.ListRoles(ctx, org.ID())
	if err != nil {
		writeServiceError(ctx, w, err, "organization not found")
		return
	}

	items := make([]roleDTO, 0, len(roles))
	for _, role := range roles {
		items = append(items, newRoleDTO(role))
	}

	// Roles are not paginated: an organization holds a handful of them. The
	// envelope still advertises the regular page size, so a client computing a
	// page count out of total/limit never divides by zero on an empty tenant.
	writeJSON(w, http.StatusOK, newListDTO(items, 1, maxPageLimit, int64(len(items))))
}

func (h *Handler) handleCreateRole(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	org, ok := h.resolveOrganization(w, r)
	if !ok {
		return
	}

	var payload roleRequest
	if !decodeJSON(w, r, &payload) {
		return
	}

	role, err := h.provisioning.CreateRole(ctx, org.ID(), service.RoleParams{
		Name:        payload.Name,
		Description: payload.Description,
		Permissions: payload.Permissions,
		ModelGrants: toModelGrants(payload.ModelGrants),
	})
	if err != nil {
		writeServiceError(ctx, w, err, "could not create role")
		return
	}

	writeJSON(w, http.StatusCreated, newRoleDTO(role))
}

func (h *Handler) handleGetRole(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	org, ok := h.resolveOrganization(w, r)
	if !ok {
		return
	}

	role, err := h.provisioning.GetRole(ctx,
		org.ID(),
		model.RoleID(r.PathValue("roleID")),
	)
	if err != nil {
		writeServiceError(ctx, w, err, "role not found")
		return
	}

	writeJSON(w, http.StatusOK, newRoleDTO(role))
}

func (h *Handler) handleUpdateRole(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	org, ok := h.resolveOrganization(w, r)
	if !ok {
		return
	}

	var payload roleRequest
	if !decodeJSON(w, r, &payload) {
		return
	}

	role, err := h.provisioning.UpdateRole(ctx,
		org.ID(),
		model.RoleID(r.PathValue("roleID")),
		service.RoleParams{
			Name:        payload.Name,
			Description: payload.Description,
			Permissions: payload.Permissions,
			ModelGrants: toModelGrants(payload.ModelGrants),
		},
	)
	if err != nil {
		writeServiceError(ctx, w, err, "role not found")
		return
	}

	writeJSON(w, http.StatusOK, newRoleDTO(role))
}

func (h *Handler) handleDeleteRole(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	org, ok := h.resolveOrganization(w, r)
	if !ok {
		return
	}

	err := h.provisioning.DeleteRole(ctx,
		org.ID(),
		model.RoleID(r.PathValue("roleID")),
	)
	if err != nil {
		writeServiceError(ctx, w, err, "role not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
