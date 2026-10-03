package v1

import (
	"net/http"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

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

	membership, err := h.provisioning.SetMemberRoles(ctx,
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
