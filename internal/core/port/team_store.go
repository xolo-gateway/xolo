package port

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

type TeamStore interface {
	// CreateTeam fails with ErrAlreadyExists when the slug is taken in the
	// organization.
	CreateTeam(ctx context.Context, team model.Team) error
	// SaveTeam updates the team and fully replaces its model grants and
	// middlewares. A slug another team of the organization holds fails with
	// ErrAlreadyExists; the caller checks a new slug with service.CheckTeamSlug.
	SaveTeam(ctx context.Context, team model.Team) error
	// DeleteTeam removes the team, its members and their team roles. The
	// budgets of the team are removed with it. It fails with ErrNotAllowed
	// while applications are bound to the team: unbinding them silently would
	// change what they can reach. It fails with ErrTeamHasMiddlewares while a
	// middleware is attached to this team and no other: with no team left, it
	// would run for the whole organization.
	DeleteTeam(ctx context.Context, id model.TeamID) error
	GetTeamByID(ctx context.Context, id model.TeamID) (model.Team, error)
	GetTeamBySlug(ctx context.Context, orgID model.OrgID, slug string) (model.Team, error)
	ListOrgTeams(ctx context.Context, orgID model.OrgID) ([]model.Team, error)

	// ListTeamMembers returns the members of the team, user preloaded.
	ListTeamMembers(ctx context.Context, id model.TeamID) ([]model.TeamMember, error)
	// ListMembershipTeams returns the team memberships of an organization
	// membership.
	ListMembershipTeams(ctx context.Context, membershipID model.MembershipID) ([]model.TeamMember, error)
	// SetTeamMember adds the membership to the team, or replaces its team
	// roles when it is a member already: it is idempotent, and a member
	// already in the team keeps its join date. The membership must belong to
	// the organization of the team.
	SetTeamMember(ctx context.Context, id model.TeamID, membershipID model.MembershipID, roleIDs []model.RoleID) error
	RemoveTeamMember(ctx context.Context, id model.TeamID, membershipID model.MembershipID) error

	// ModelNamePrefixTaken reports whether a real or virtual model of the
	// organization has a local name starting with "<slug>/", which a team
	// named slug would make ambiguous.
	ModelNamePrefixTaken(ctx context.Context, orgID model.OrgID, slug string) (bool, error)
}
