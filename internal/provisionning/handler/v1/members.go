package v1

import (
	"net/http"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

func (h *Handler) handleListMembers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	org, ok := h.resolveOrganization(w, r)
	if !ok {
		return
	}

	page, limit, ok := pagination(r)
	if !ok {
		writeInvalidPagination(w)
		return
	}

	offset := page - 1

	members, total, err := h.provisioning.ListMembers(ctx, org.TenantID(), org.ID(), port.ListOrgMembersOptions{
		Page:  &offset,
		Limit: &limit,
	})
	if err != nil {
		writeServiceError(ctx, w, err, "organization not found")
		return
	}

	items := make([]membershipDTO, 0, len(members))
	for _, member := range members {
		items = append(items, newMembershipDTO(member))
	}

	writeJSON(w, http.StatusOK, newListDTO(items, page, limit, total))
}

func (h *Handler) handleGetMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	org, ok := h.resolveOrganization(w, r)
	if !ok {
		return
	}

	membership, err := h.provisioning.GetMember(ctx,
		org.TenantID(), org.ID(),
		model.MembershipID(r.PathValue("membershipID")),
	)
	if err != nil {
		writeServiceError(ctx, w, err, "membership not found")
		return
	}

	writeJSON(w, http.StatusOK, newMembershipDTO(membership))
}

func (h *Handler) handleSetMemberRoles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	org, ok := h.resolveOrganization(w, r)
	if !ok {
		return
	}

	var payload setMemberRolesRequest
	if !decodeJSON(w, r, &payload) {
		return
	}

	membership, err := h.provisioning.SetMemberRoles(ctx, org.TenantID(),
		org.ID(),
		model.MembershipID(r.PathValue("membershipID")),
		toRoleIDs(payload.RoleIDs),
		payload.BuiltinRoles,
	)
	if err != nil {
		writeServiceError(ctx, w, err, "membership not found")
		return
	}

	writeJSON(w, http.StatusOK, newMembershipDTO(membership))
}
