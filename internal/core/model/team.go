package model

import (
	"slices"
	"strings"
	"time"

	"github.com/rs/xid"
)

type TeamID string

func NewTeamID() TeamID {
	return TeamID(xid.New().String())
}

// Team is a scope inside an organization: its members, the models and
// middlewares they can use through it, and its budget. A role says what a
// member can do; a team says with what.
//
// Teams are flat: a team has no parent team. Every request is attributed to at
// most one team, carried by the model name "<org>/<team>/<model>", which picks
// the budget to charge and the middlewares to run.
type Team interface {
	WithID[TeamID]

	OrgID() OrgID
	// Slug is the team segment of qualified model names. It can be changed,
	// but clients store model names in their configuration: those naming the
	// former slug stop resolving.
	Slug() string
	Name() string
	Description() string
	// ShareQuotaEqually splits the team budget equally among the team members
	// that have no budget of their own in the team.
	ShareQuotaEqually() bool
	// ModelGrants lists the models, real or virtual, the team's members can use.
	ModelGrants() []ModelGrant
	// MiddlewareIDs lists the middlewares that run on the team's requests, after
	// the organization middlewares.
	MiddlewareIDs() []MiddlewareID
	CreatedAt() time.Time
	UpdatedAt() time.Time
}

type BaseTeam struct {
	id                TeamID
	orgID             OrgID
	slug              string
	name              string
	description       string
	shareQuotaEqually bool
	modelGrants       []ModelGrant
	middlewareIDs     []MiddlewareID
	createdAt         time.Time
	updatedAt         time.Time
}

func (t *BaseTeam) ID() TeamID                    { return t.id }
func (t *BaseTeam) OrgID() OrgID                  { return t.orgID }
func (t *BaseTeam) Slug() string                  { return t.slug }
func (t *BaseTeam) Name() string                  { return t.name }
func (t *BaseTeam) Description() string           { return t.description }
func (t *BaseTeam) ShareQuotaEqually() bool       { return t.shareQuotaEqually }
func (t *BaseTeam) ModelGrants() []ModelGrant     { return t.modelGrants }
func (t *BaseTeam) MiddlewareIDs() []MiddlewareID { return t.middlewareIDs }
func (t *BaseTeam) CreatedAt() time.Time          { return t.createdAt }
func (t *BaseTeam) UpdatedAt() time.Time          { return t.updatedAt }

// SetID gives the team an identifier chosen by the provisioning client.
func (t *BaseTeam) SetID(id TeamID) { t.id = id }

func (t *BaseTeam) SetSlug(slug string)               { t.slug = slug }
func (t *BaseTeam) SetName(name string)               { t.name = name }
func (t *BaseTeam) SetDescription(description string) { t.description = description }
func (t *BaseTeam) SetShareQuotaEqually(v bool)       { t.shareQuotaEqually = v }
func (t *BaseTeam) SetModelGrants(grants []ModelGrant) {
	t.modelGrants = NormalizeModelGrants(grants)
}
func (t *BaseTeam) SetMiddlewareIDs(ids []MiddlewareID) {
	ids = slices.Clone(ids)
	slices.Sort(ids)
	t.middlewareIDs = slices.Compact(ids)
}

var _ Team = &BaseTeam{}

func NewTeam(orgID OrgID, slug, name, description string) *BaseTeam {
	now := time.Now()
	return &BaseTeam{
		id:          NewTeamID(),
		orgID:       orgID,
		slug:        slug,
		name:        name,
		description: description,
		createdAt:   now,
		updatedAt:   now,
	}
}

// CopyTeam returns an editable copy of team, stamped as updated now.
func CopyTeam(team Team) *BaseTeam {
	return &BaseTeam{
		id:                team.ID(),
		orgID:             team.OrgID(),
		slug:              team.Slug(),
		name:              team.Name(),
		description:       team.Description(),
		shareQuotaEqually: team.ShareQuotaEqually(),
		modelGrants:       slices.Clone(team.ModelGrants()),
		middlewareIDs:     slices.Clone(team.MiddlewareIDs()),
		createdAt:         team.CreatedAt(),
		updatedAt:         time.Now(),
	}
}

// TeamGrants reports whether the team grants the model of the given kind.
func TeamGrants(team Team, modelID string, kind string) bool {
	return slices.Contains(team.ModelGrants(), ModelGrant{ModelID: modelID, Kind: kind})
}

// NormalizeModelGrants sorts and deduplicates grants, so that two equal sets
// compare equal whatever order they were given in.
func NormalizeModelGrants(grants []ModelGrant) []ModelGrant {
	grants = slices.Clone(grants)
	slices.SortFunc(grants, func(a, b ModelGrant) int {
		if c := strings.Compare(a.ModelID, b.ModelID); c != 0 {
			return c
		}
		return strings.Compare(a.Kind, b.Kind)
	})
	return slices.Compact(grants)
}

// TeamMember is the membership of an organization member in a team. Its roles
// are assigned on the team scope: a "team lead" role held here grants its
// team-scopable permissions on this team only.
type TeamMember interface {
	TeamID() TeamID
	MembershipID() MembershipID
	UserID() UserID
	RoleIDs() []RoleID
	CreatedAt() time.Time

	// Populated via preload, may be nil.
	User() User
}

type BaseTeamMember struct {
	teamID       TeamID
	membershipID MembershipID
	userID       UserID
	roleIDs      []RoleID
	createdAt    time.Time
	user         User
}

func (m *BaseTeamMember) TeamID() TeamID             { return m.teamID }
func (m *BaseTeamMember) MembershipID() MembershipID { return m.membershipID }
func (m *BaseTeamMember) UserID() UserID             { return m.userID }
func (m *BaseTeamMember) RoleIDs() []RoleID          { return m.roleIDs }
func (m *BaseTeamMember) CreatedAt() time.Time       { return m.createdAt }
func (m *BaseTeamMember) User() User                 { return m.user }

var _ TeamMember = &BaseTeamMember{}

func NewTeamMember(teamID TeamID, membershipID MembershipID, userID UserID, roleIDs []RoleID) *BaseTeamMember {
	roleIDs = slices.Clone(roleIDs)
	slices.Sort(roleIDs)
	return &BaseTeamMember{
		teamID:       teamID,
		membershipID: membershipID,
		userID:       userID,
		roleIDs:      slices.Compact(roleIDs),
		createdAt:    time.Now(),
	}
}

// TeamMemberScopeID is the quota scope id of a member's budget inside a team.
func TeamMemberScopeID(teamID TeamID, userID UserID) string {
	return string(teamID) + ":" + string(userID)
}

// ParseTeamMemberScopeID splits a team member quota scope id.
func ParseTeamMemberScopeID(scopeID string) (TeamID, UserID, bool) {
	team, user, ok := strings.Cut(scopeID, ":")
	if !ok || team == "" || user == "" {
		return "", "", false
	}
	return TeamID(team), UserID(user), true
}
