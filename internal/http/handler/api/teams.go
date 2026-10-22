package api

import (
	"net/http"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
)

// WithTeams enables the checks keeping team slugs and model names apart: a
// graph may not name a team-qualified model, and a virtual model may not be
// named after a team. Without it, nothing is checked.
func (h *Handler) WithTeams(teams port.TeamStore) *Handler {
	h.teamStore = teams
	return h
}

// rejectGraph answers the request and returns true when graph is malformed,
// as the provisioning API sees it, or names a model qualified with a team.
func (h *Handler) rejectGraph(w http.ResponseWriter, r *http.Request, graph *model.PipelineGraph) bool {
	if err := service.ValidatePipelineGraph(graph); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return true
	}
	if h.teamStore == nil || graph == nil {
		return false
	}
	ctx := r.Context()
	err := service.CheckStaticModelRefs(ctx, h.orgStore, h.teamStore, httpCtx.TenantID(ctx), graph)
	switch {
	case err == nil:
		return false
	case errors.Is(err, service.ErrTeamQualifiedReference):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
	return true
}

// rejectModelName answers the request and returns true when name, the name of
// a new virtual model of the organization, starts with the slug of one of its
// teams.
func (h *Handler) rejectModelName(w http.ResponseWriter, r *http.Request, orgID model.OrgID, name string) bool {
	if h.teamStore == nil {
		return false
	}
	err := service.CheckModelLocalName(r.Context(), h.teamStore, orgID, name)
	switch {
	case err == nil:
		return false
	case errors.Is(err, port.ErrTeamNameConflict):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
	return true
}
