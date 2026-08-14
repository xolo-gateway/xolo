package v1

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	"github.com/pkg/errors"
)

func (h *Handler) handleListUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	query := r.URL.Query()

	tenant, ok := h.resolveTenant(w, r)
	if !ok {
		return
	}

	provider := strings.TrimSpace(query.Get("provider"))
	subject := strings.TrimSpace(query.Get("subject"))

	// Identity lookup: the way an external system resolves the user it declared
	// into the identifier Xolo assigned it.
	if provider != "" || subject != "" {
		if provider == "" || subject == "" {
			writeError(w, http.StatusBadRequest, codeInvalidRequest, "provider and subject must be provided together")
			return
		}

		user, err := h.provisioning.FindUserByIdentity(ctx, tenant.ID(), provider, subject)
		if err != nil {
			if errors.Is(err, port.ErrNotFound) {
				writeJSON(w, http.StatusOK, newListDTO([]userDTO{}, 1, 1, 0))
				return
			}
			writeServiceError(ctx, w, err, "user not found")
			return
		}

		writeJSON(w, http.StatusOK, newListDTO([]userDTO{newUserDTO(user)}, 1, 1, 1))

		return
	}

	page, limit, ok := pagination(r)
	if !ok {
		writeInvalidPagination(w)
		return
	}

	// Listing inactive accounts is how a control plane finds the users waiting
	// for approval when XOLO_HTTP_AUTHN_ACTIVE_BY_DEFAULT is false.
	var active *bool
	if raw := query.Get("active"); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidRequest, "active must be true or false")
			return
		}
		active = &parsed
	}

	offset := page - 1

	tenantID := tenant.ID()

	users, total, err := h.provisioning.ListUsers(ctx, port.QueryUsersOptions{
		Page:     &offset,
		Limit:    &limit,
		Search:   query.Get("search"),
		Active:   active,
		TenantID: &tenantID,
	})
	if err != nil {
		writeServiceError(ctx, w, err, "could not list users")
		return
	}

	items := make([]userDTO, 0, len(users))
	for _, user := range users {
		items = append(items, newUserDTO(user))
	}

	writeJSON(w, http.StatusOK, newListDTO(items, page, limit, total))
}

// handlePutUser upserts a user identified by its provider/subject tuple. It is
// idempotent: replaying the same request converges to the same state and
// answers 200 instead of 201.
func (h *Handler) handlePutUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	tenant, ok := h.resolveTenant(w, r)
	if !ok {
		return
	}

	var payload userIdentityRequest
	if !decodeJSON(w, r, &payload) {
		return
	}

	user, created, err := h.provisioning.ProvisionUser(ctx, tenant.ID(), toIdentityParams(payload))
	if err != nil {
		writeServiceError(ctx, w, err, "could not provision user")
		return
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}

	writeJSON(w, status, newUserDTO(user))
}

func (h *Handler) handleGetUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	tenant, ok := h.resolveTenant(w, r)
	if !ok {
		return
	}

	user, err := h.provisioning.GetUser(ctx, tenant.ID(), model.UserID(r.PathValue("userID")))
	if err != nil {
		writeServiceError(ctx, w, err, "user not found")
		return
	}

	writeJSON(w, http.StatusOK, newUserDTO(user))
}

func (h *Handler) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	tenant, ok := h.resolveTenant(w, r)
	if !ok {
		return
	}

	var payload updateUserRequest
	if !decodeJSON(w, r, &payload) {
		return
	}

	user, err := h.provisioning.UpdateUser(ctx, tenant.ID(), model.UserID(r.PathValue("userID")), service.UpdateUserParams{
		Email:       payload.Email,
		DisplayName: payload.DisplayName,
		Active:      payload.Active,
	})
	if err != nil {
		writeServiceError(ctx, w, err, "user not found")
		return
	}

	writeJSON(w, http.StatusOK, newUserDTO(user))
}
