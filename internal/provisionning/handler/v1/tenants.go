package v1

import (
	"net/http"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

func (h *Handler) handleListTenants(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// An exact slug lookup lets a reconciler read back the state it just
	// declared without knowing the generated identifier. On a single-tenant
	// instance it is also how a control plane discovers the tenant identifier
	// every other route needs.
	if slug := r.URL.Query().Get("slug"); slug != "" {
		tenant, err := h.provisioning.GetTenantBySlug(ctx, slug)
		if err != nil {
			if errors.Is(err, port.ErrNotFound) {
				writeJSON(w, http.StatusOK, newListDTO([]tenantDTO{}, 1, 1, 0))
				return
			}
			writeServiceError(ctx, w, err, "tenant not found")
			return
		}

		writeJSON(w, http.StatusOK, newListDTO([]tenantDTO{newTenantDTO(tenant)}, 1, 1, 1))

		return
	}

	page, limit, ok := pagination(r)
	if !ok {
		writeInvalidPagination(w)
		return
	}

	offset := page - 1

	tenants, total, err := h.provisioning.ListTenants(ctx, port.ListTenantsOptions{Page: &offset, Limit: &limit})
	if err != nil {
		writeServiceError(ctx, w, err, "could not list tenants")
		return
	}

	items := make([]tenantDTO, 0, len(tenants))
	for _, tenant := range tenants {
		items = append(items, newTenantDTO(tenant))
	}

	writeJSON(w, http.StatusOK, newListDTO(items, page, limit, total))
}

// handleCreateTenant provisions a tenant. On a single-tenant instance the
// service refuses it with a conflict: no hostname would ever resolve to the new
// tenant, so its organizations would be unreachable.
func (h *Handler) handleCreateTenant(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var payload createTenantRequest
	if !decodeJSON(w, r, &payload) {
		return
	}

	tenant, err := h.provisioning.CreateTenant(ctx, service.CreateTenantParams{
		Slug:        payload.Slug,
		Name:        payload.Name,
		Description: payload.Description,
		Active:      payload.Active,
	})
	if err != nil {
		writeServiceError(ctx, w, err, "could not create tenant")
		return
	}

	writeJSON(w, http.StatusCreated, newTenantDTO(tenant))
}

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

func (h *Handler) handleDeleteTenant(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if err := h.provisioning.DeleteTenant(ctx, model.TenantID(r.PathValue("tenantID"))); err != nil {
		writeServiceError(ctx, w, err, "tenant not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
