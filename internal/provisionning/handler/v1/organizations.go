package v1

import (
	"net/http"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

type createOrganizationResponse struct {
	Organization organizationDTO `json:"organization"`

	// Owner and Membership are present only when an initial owner was
	// requested. OwnerCreated tells an external reconciler whether the identity
	// already existed.
	Owner        *userDTO       `json:"owner,omitempty"`
	Membership   *membershipDTO `json:"ownerMembership,omitempty"`
	OwnerCreated bool           `json:"ownerCreated"`
}

func (h *Handler) handleListOrganizations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	tenant, ok := h.resolveTenant(w, r)
	if !ok {
		return
	}

	// An exact slug lookup lets a reconciler read back the state it just
	// declared without knowing the generated identifier.
	if slug := r.URL.Query().Get("slug"); slug != "" {
		org, err := h.provisioning.GetOrganizationBySlug(ctx, tenant.ID(), slug)
		if err != nil {
			if errors.Is(err, port.ErrNotFound) {
				writeJSON(w, http.StatusOK, newListDTO([]organizationDTO{}, 1, 1, 0))
				return
			}
			writeServiceError(ctx, w, err, "organization not found")
			return
		}

		writeJSON(w, http.StatusOK, newListDTO([]organizationDTO{newOrganizationDTO(org)}, 1, 1, 1))

		return
	}

	page, limit, ok := pagination(r)
	if !ok {
		writeInvalidPagination(w)
		return
	}

	offset := page - 1
	tenantID := tenant.ID()

	orgs, total, err := h.provisioning.ListOrganizations(ctx, port.ListOrgsOptions{
		Page:     &offset,
		Limit:    &limit,
		TenantID: &tenantID,
	})
	if err != nil {
		writeServiceError(ctx, w, err, "could not list organizations")
		return
	}

	items := make([]organizationDTO, 0, len(orgs))
	for _, org := range orgs {
		items = append(items, newOrganizationDTO(org))
	}

	writeJSON(w, http.StatusOK, newListDTO(items, page, limit, total))
}

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
