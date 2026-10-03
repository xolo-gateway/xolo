package v1

import (
	"net/http"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

func (h *Handler) handleGetOrganization(w http.ResponseWriter, r *http.Request) {
	org, ok := h.resolveOrganization(w, r)
	if !ok {
		return
	}

	writeJSON(w, http.StatusOK, newOrganizationDTO(org))
}

func (h *Handler) handleUpdateOrganization(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	tenant, ok := h.resolveTenant(w, r)
	if !ok {
		return
	}

	var payload updateOrganizationRequest
	if !decodeJSON(w, r, &payload) {
		return
	}

	org, err := h.provisioning.UpdateOrganization(ctx, tenant.ID(), model.OrgID(r.PathValue("orgID")), service.UpdateOrganizationParams{
		Name:              payload.Name,
		Description:       payload.Description,
		Active:            payload.Active,
		Currency:          payload.Currency,
		ShareQuotaEqually: payload.ShareQuotaEqually,
	})
	if err != nil {
		writeServiceError(ctx, w, err, "organization not found")
		return
	}

	writeJSON(w, http.StatusOK, newOrganizationDTO(org))
}

func toIdentityParams(payload userIdentityRequest) service.UserIdentityParams {
	return service.UserIdentityParams{
		Provider:    payload.Provider,
		Subject:     payload.Subject,
		Email:       payload.Email,
		DisplayName: payload.DisplayName,
		Active:      payload.Active,
	}
}
