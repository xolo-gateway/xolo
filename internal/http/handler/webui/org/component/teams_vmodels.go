package component

import (
	"github.com/xolo-gateway/xolo/internal/core/model"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
)

// TeamsPageVModel lists the teams a member can see: every team with
// teams:read, else those it holds a team role on.
type TeamsPageVModel struct {
	common.AppLayoutVModel
	Org     model.Organization
	Teams   []TeamRow
	CanEdit bool
	Success string
}

type TeamRow struct {
	Team    model.Team
	Members int
}

// TeamFormVModel is the creation form of a team.
type TeamFormVModel struct {
	common.AppLayoutVModel
	Org         model.Organization
	Error       string
	Slug        string
	Name        string
	Description string
}

// TeamPageVModel is the page of one team: its settings and access, which the
// organization decides, and its members and budget split, which the team
// leads manage.
type TeamPageVModel struct {
	common.AppLayoutVModel
	Org     model.Organization
	Team    model.Team
	Success string
	Error   string

	// CanEditTeam allows changing the team and what it can use (teams:write).
	CanEditTeam bool
	// CanManageMembers allows adding and removing members, on the
	// organization or on this team (members:write).
	CanManageMembers bool
	// CanEditTeamBudget allows setting the team envelope, which only the
	// organization decides (quota:write on the organization).
	CanEditTeamBudget bool
	// CanSplitBudget allows splitting the envelope among the members, on the
	// organization or on this team (quota:write).
	CanSplitBudget bool
	CanReadBudget  bool

	ModelOptions        []ModelGrantOption
	SelectedModels      map[string]bool // "kind:id" -> granted
	MiddlewareOptions   []TeamMiddlewareOption
	SelectedMiddlewares map[string]bool

	Members    []TeamMemberRow
	Candidates []model.Membership
	Roles      []model.Role
	// Applications bound to the team, which keep it from being deleted.
	Applications []model.Application

	TeamQuota model.Quota
	// Spend of the team over the current day, month and year.
	Spend [3]int64
	// SampleModel is a model name qualified with the team, shown as an
	// example of how members name it.
	SampleModel string
}

type TeamMiddlewareOption struct {
	ID           string
	Name         string
	AppliesToAll bool
	Enabled      bool
}

type TeamMemberRow struct {
	Member    model.TeamMember
	RoleNames []string
	Budget    model.Quota
	// Spend of the member inside the team over the current month.
	MonthSpend int64
}

// TeamMemberPageVModel edits one member of a team: its team roles and its
// budget inside the team.
type TeamMemberPageVModel struct {
	common.AppLayoutVModel
	Org    model.Organization
	Team   model.Team
	Member model.TeamMember
	Error  string
	// Success is set after a save.
	Success string

	CanManageMembers bool
	CanSplitBudget   bool
	// Roles are the roles of the organization; AssignableRoles those the
	// current user may hand out on the team, which never widen its own
	// access.
	Roles           []model.Role
	AssignableRoles map[string]bool
	SelectedRoles   map[string]bool

	Budget model.Quota
	Spend  [3]int64
}
