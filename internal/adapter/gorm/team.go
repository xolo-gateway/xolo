package gorm

import (
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

type Team struct {
	ID                string `gorm:"primaryKey;autoIncrement:false"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
	OrgID             string `gorm:"index;uniqueIndex:team_org_slug_index;not null"`
	Slug              string `gorm:"uniqueIndex:team_org_slug_index;not null"`
	Name              string `gorm:"not null"`
	Description       string
	ShareQuotaEqually bool `gorm:"not null;default:false"`

	Org         *Organization     `gorm:"foreignKey:OrgID"`
	ModelGrants []*TeamModelGrant `gorm:"foreignKey:TeamID;constraint:OnDelete:CASCADE"`
	Middlewares []*TeamMiddleware `gorm:"foreignKey:TeamID;constraint:OnDelete:CASCADE"`
}

// TeamModelGrant authorizes the members of a team to use a model, real or
// virtual. A grant on a model deleted since is left in place and matches
// nothing, as role grants used to.
type TeamModelGrant struct {
	TeamID    string `gorm:"primaryKey"`
	ModelID   string `gorm:"primaryKey"`
	ModelKind string `gorm:"primaryKey"`
}

// TeamMiddleware attaches a middleware to a team: it then runs on the team's
// requests only, after the organization middlewares.
type TeamMiddleware struct {
	TeamID       string `gorm:"primaryKey"`
	MiddlewareID string `gorm:"primaryKey;index"`
}

// TeamMember is the membership of an organization member in a team.
type TeamMember struct {
	TeamID       string `gorm:"primaryKey"`
	MembershipID string `gorm:"primaryKey;index"`
	CreatedAt    time.Time
}

// TeamMemberRole assigns a role to a team member on the team scope.
type TeamMemberRole struct {
	TeamID       string `gorm:"primaryKey"`
	MembershipID string `gorm:"primaryKey"`
	RoleID       string `gorm:"primaryKey;index"`
}

type wrappedTeam struct {
	t *Team
}

func (w *wrappedTeam) ID() model.TeamID        { return model.TeamID(w.t.ID) }
func (w *wrappedTeam) OrgID() model.OrgID      { return model.OrgID(w.t.OrgID) }
func (w *wrappedTeam) Slug() string            { return w.t.Slug }
func (w *wrappedTeam) Name() string            { return w.t.Name }
func (w *wrappedTeam) Description() string     { return w.t.Description }
func (w *wrappedTeam) ShareQuotaEqually() bool { return w.t.ShareQuotaEqually }
func (w *wrappedTeam) CreatedAt() time.Time    { return w.t.CreatedAt }
func (w *wrappedTeam) UpdatedAt() time.Time    { return w.t.UpdatedAt }

func (w *wrappedTeam) ModelGrants() []model.ModelGrant {
	grants := make([]model.ModelGrant, 0, len(w.t.ModelGrants))
	for _, g := range w.t.ModelGrants {
		grants = append(grants, model.ModelGrant{ModelID: g.ModelID, Kind: g.ModelKind})
	}
	return model.NormalizeModelGrants(grants)
}

func (w *wrappedTeam) MiddlewareIDs() []model.MiddlewareID {
	ids := make([]model.MiddlewareID, 0, len(w.t.Middlewares))
	for _, m := range w.t.Middlewares {
		ids = append(ids, model.MiddlewareID(m.MiddlewareID))
	}
	return ids
}

var _ model.Team = &wrappedTeam{}

func fromTeam(t model.Team) *Team {
	team := &Team{
		ID:                string(t.ID()),
		CreatedAt:         t.CreatedAt(),
		UpdatedAt:         t.UpdatedAt(),
		OrgID:             string(t.OrgID()),
		Slug:              t.Slug(),
		Name:              t.Name(),
		Description:       t.Description(),
		ShareQuotaEqually: t.ShareQuotaEqually(),
	}
	for _, g := range model.NormalizeModelGrants(t.ModelGrants()) {
		team.ModelGrants = append(team.ModelGrants, &TeamModelGrant{TeamID: team.ID, ModelID: g.ModelID, ModelKind: g.Kind})
	}
	for _, id := range t.MiddlewareIDs() {
		team.Middlewares = append(team.Middlewares, &TeamMiddleware{TeamID: team.ID, MiddlewareID: string(id)})
	}
	return team
}

// teamMemberRow is a team member as read with its organization membership.
type teamMemberRow struct {
	TeamID       string
	MembershipID string
	UserID       string
	CreatedAt    time.Time
	roleIDs      []model.RoleID
	user         *User
}

type wrappedTeamMember struct {
	m *teamMemberRow
}

func (w *wrappedTeamMember) TeamID() model.TeamID { return model.TeamID(w.m.TeamID) }
func (w *wrappedTeamMember) MembershipID() model.MembershipID {
	return model.MembershipID(w.m.MembershipID)
}
func (w *wrappedTeamMember) UserID() model.UserID    { return model.UserID(w.m.UserID) }
func (w *wrappedTeamMember) RoleIDs() []model.RoleID { return w.m.roleIDs }
func (w *wrappedTeamMember) CreatedAt() time.Time    { return w.m.CreatedAt }
func (w *wrappedTeamMember) User() model.User {
	if w.m.user == nil {
		return nil
	}
	return &wrappedUser{w.m.user}
}

var _ model.TeamMember = &wrappedTeamMember{}
