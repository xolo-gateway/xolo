package v1

import (
	"net/http"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

func (h *Handler) handleGetTenant(w http.ResponseWriter, r *http.Request) {
	tenant, ok := h.resolveTenant(w, r)
	if !ok {
		return
	}

	writeJSON(w, http.StatusOK, newTenantDTO(tenant))
}

func (h *Handler) handleUpdateTenant(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var payload updateTenantRequest
	if !decodeJSON(w, r, &payload) {
		return
	}

	tenant, err := h.provisioning.UpdateTenant(ctx, model.TenantID(r.PathValue("tenantID")), service.UpdateTenantParams{
		Name:        payload.Name,
		Description: payload.Description,
		Active:      payload.Active,
	})
	if err != nil {
		writeServiceError(ctx, w, err, "tenant not found")
		return
	}

	writeJSON(w, http.StatusOK, newTenantDTO(tenant))
}
