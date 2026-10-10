package profile

import (
	"context"
	"slices"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/profile/component"
)

// userTeams lists the teams the user belongs to, across its organizations,
// ordered by organization then team name. It is empty when teams are not
// enabled.
func (h *Handler) userTeams(ctx context.Context, userID model.UserID) ([]component.TeamOption, error) {
	if h.teamStore == nil {
		return nil, nil
	}
	memberships, err := h.orgStore.GetUserMemberships(ctx, userID)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	var options []component.TeamOption
	for _, m := range memberships {
		if m.Status() != model.StatusActive {
			continue
		}
		orgName := string(m.OrgID())
		if m.Org() != nil {
			orgName = m.Org().Name()
		}
		teamMembers, err := h.teamStore.ListMembershipTeams(ctx, m.ID())
		if err != nil {
			return nil, errors.WithStack(err)
		}
		for _, tm := range teamMembers {
			team, err := h.teamStore.GetTeamByID(ctx, tm.TeamID())
			if err != nil {
				return nil, errors.WithStack(err)
			}
			options = append(options, component.TeamOption{
				OrgID: m.OrgID(), OrgName: orgName, TeamID: team.ID(), TeamName: team.Name(), Slug: team.Slug(),
			})
		}
	}
	slices.SortFunc(options, func(a, b component.TeamOption) int {
		if a.OrgName != b.OrgName {
			if a.OrgName < b.OrgName {
				return -1
			}
			return 1
		}
		if a.TeamName < b.TeamName {
			return -1
		}
		if a.TeamName > b.TeamName {
			return 1
		}
		return 0
	})
	return options, nil
}

// pickTeam returns the option of teamID among the user's teams. An empty
// teamID picks none; an unknown one is refused.
func pickTeam(options []component.TeamOption, teamID model.TeamID) (component.TeamOption, bool) {
	if teamID == "" {
		return component.TeamOption{}, true
	}
	for _, opt := range options {
		if opt.TeamID == teamID {
			return opt, true
		}
	}
	return component.TeamOption{}, false
}
