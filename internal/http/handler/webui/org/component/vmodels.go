package component

import (
	"net/url"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
)

type OrgDashboardVModel struct {
	common.AppLayoutVModel
	Org       model.Organization
	Members   []model.Membership
	Providers []model.Provider
}

type MembersPageVModel struct {
	common.AppLayoutVModel
	Org          model.Organization
	Members      []model.Membership
	Success      string
	CurrentPage  int
	PageSize     int
	TotalMembers int
}

type ProvidersPageVModel struct {
	common.AppLayoutVModel
	Org         model.Organization
	Providers   []model.Provider
	ModelCounts map[model.ProviderID]int
	Success     string
}

type ProviderFormVModel struct {
	common.AppLayoutVModel
	Org      model.Organization
	Provider model.Provider
	IsNew    bool
	Error    string
	// Submitted is the form as it was posted, set when the page is rendered
	// back with an error: fields are refilled from it rather than from the
	// stored provider, so a rejected value is shown as typed instead of vanishing.
	Submitted url.Values
}

type ModelsPageVModel struct {
	common.AppLayoutVModel
	Org      model.Organization
	Provider model.Provider
	Models   []model.LLMModel
	Success  string
}

// ModelImportVModel is the page listing the models a provider serves, to
// import some of them at once.
type ModelImportVModel struct {
	common.AppLayoutVModel
	Org        model.Organization
	Provider   model.Provider
	Candidates []ModelImportCandidate
	// Prefix is prepended to each imported ID to form its proxy name.
	Prefix string
	Error  string
	// Imported and Skipped report the last import when it left models out.
	Imported int
	Skipped  []string
}

// ModelImportCandidate is one model as the provider lists it.
type ModelImportCandidate struct {
	ID            string
	DisplayName   string
	ContextWindow int64
	OutputWindow  int64
	// DeclaredAs is the proxy name of the provider's model already serving
	// this ID, or empty.
	DeclaredAs string
	// ProxyName is the name the import would give it with the current prefix,
	// and Conflict why that name cannot be used, or empty.
	ProxyName string
	Conflict  string
	// Checked keeps the admin's selection when the page is shown again.
	Checked bool
}

type ModelFormVModel struct {
	common.AppLayoutVModel
	Org      model.Organization
	Provider model.Provider
	Model    model.LLMModel
	IsNew    bool
	Error    string
}

type QuotaPageVModel struct {
	common.AppLayoutVModel
	Org         model.Organization
	Membership  model.Membership
	Application model.Application
	ScopeType   string
	ScopeID     string
	Quota       model.Quota
	Success     string
	DailyCost   int64 // current period spend in org currency (microcents)
	MonthlyCost int64
	YearlyCost  int64
	// Error explains why the submitted budget was refused.
	Error string
	// LoadError is set when the quota store or a spend lookup returned an
	// error other than ErrNotFound. The page still renders what it has, but
	// a banner warns the operator so the displayed numbers do not silently
	// disagree with what the enforcer actually enforces.
	LoadError string
	// Submitted carries the raw form values when the page is re-rendered
	// after a save validation failure (issue #88). When non-nil, the budget
	// fields display the operator's input verbatim instead of the stored
	// values, so a typo that was rejected does not silently become the
	// previous cap.
	Submitted map[string]string
	// FieldErrors maps a form field name ("daily_budget", …) to the
	// message parseBudgetField returned for it. The page marks the
	// offending MoneyInput as invalid and surfaces the first error at the
	// top of the form, so a hand-crafted POST that bypasses the browser's
	// HTML5 validation still gets a readable response (HTTP 422 with the
	// form re-rendered) rather than a silent no-op save.
	FieldErrors map[string]string
}

type InvitesPageVModel struct {
	common.AppLayoutVModel
	Org       model.Organization
	Invites   []model.InviteToken
	BaseURL   string
	Success   string
	NewURL    string
	RoleNames map[string]string // role ID → display name
	// TeamNames names the teams invitations make their invitee join.
	TeamNames map[model.TeamID]string
	// CanCreate allows creating invitations, for the organization or one of
	// WritableTeams; CanManageAll allows managing every invitation.
	CanCreate     bool
	CanManageAll  bool
	WritableTeams map[model.TeamID]bool
}

type InviteFormVModel struct {
	common.AppLayoutVModel
	Org      model.Organization
	OrgRoles []model.Role
	// Teams the invitee can be made to join. TeamOnly is set for a viewer
	// inviting on its teams only: a team is then required and the role is the
	// builtin member one.
	Teams    []model.Team
	TeamOnly bool
}

type OrgSettingsPageVModel struct {
	common.AppLayoutVModel
	Org     model.Organization
	Success string
	// Aperçu quota partagé (nil si non applicable)
	MemberCount         int
	SharedDailyBudget   *int64 // microcents, nil si org n'a pas ce budget
	SharedMonthlyBudget *int64
	SharedYearlyBudget  *int64
	// Rétention des événements (ring buffer).
	EventsMaxOverride *int // nil = utilise la valeur par défaut globale
	EventsDefault     int  // valeur par défaut globale
	EventsGlobalCap   int  // plafond global (borne supérieure)
}

type OrgDisplayUsageRecord struct {
	Record           model.UsageRecord
	DisplayModelName string // display name: "virtual -> resolved" or just the model name
	DisplayCost      int64
	DisplayCurrency  string
	Converted        bool
	EnergyWh         float64 // midpoint estimation in Wh (0 = unknown)
	EnergyLowWh      float64
	EnergyHighWh     float64
	CO2GramsMid      float64 // gCO₂ at world-average carbon intensity
	CO2GramsMin      float64 // gCO₂ at France nuclear intensity (best case)
	CO2GramsMax      float64 // gCO₂ at coal plant intensity (worst case)
}

type ChartDataPoint = common.ChartDataPoint

// ChartShare represents a value's share of a total, as a percentage, with
// an associated chart color (cycling through the design system's palette).
type ChartShare = common.ChartShare

// SubscriptionConstraintUsage holds runtime consumption data for one plan constraint.
type SubscriptionConstraintUsage struct {
	Constraint model.PlanConstraint
	// UpstreamModel is the model a per-model constraint's gauge is for; empty
	// for a provider-wide constraint, and for the single row of a per-model
	// constraint no model has consumed in the window.
	UpstreamModel string
	// rolling_window fields
	TokensUsed  int64
	ValueUsed   int64     // microcents, provider currency
	WindowStart time.Time // start of the current window (anchored or sliding)
	// OldestUsage is the creation time of the oldest usage record still counted in
	// the window (zero when the window is empty). For sliding windows, usage frees up
	// progressively from OldestUsage + Duration onwards.
	OldestUsage time.Time
	// Anchored is true when the window is a fixed (tumbling) window aligned on a manual
	// anchor, matching the upstream provider's real reset schedule.
	Anchored bool
	// ResetAt is the instant the current fixed window resets (zero for sliding windows).
	ResetAt time.Time
	// TokenMode and ValueMode explain how each per-user budget above was
	// allocated. They are read per gauge rather than per constraint: the pacing
	// factor depends on how full each budget is, so a constraint can be throttled
	// on tokens while still shared on value. Empty when the figures are org-wide
	// or the allocation could not be computed.
	TokenMode model.FairShareMode
	ValueMode model.FairShareMode
	// ActiveUsers and MemberCount are the basis of the allocation, displayed so a
	// share that moves between two visits can be accounted for.
	ActiveUsers int
	MemberCount int
	// ShareDegraded reports that the active-user count was unavailable and that
	// the static share was applied, so ActiveUsers is not a measurement.
	ShareDegraded bool
	// concurrency fields
	InFlight  int
	Exhausted bool
}

// SubscriptionProviderUsage aggregates plan usage for one subscription provider.
type SubscriptionProviderUsage struct {
	Provider    model.Provider
	Plan        model.SubscriptionPlan
	Constraints []SubscriptionConstraintUsage
	// PerUser is true when the figures represent the viewer's personal fair-share
	// (usage and budgets divided by the org member count) rather than the org-wide total.
	PerUser bool
}

type OrgUsagePageVModel struct {
	common.AppLayoutVModel
	Org         model.Organization
	Aggregate   *port.UsageAggregate
	Records     []OrgDisplayUsageRecord
	Users       map[model.UserID]model.User
	Members     []model.Membership // pour le filtre utilisateur
	OwnerFilter []string           // IDs sélectionnés (users + applications)
	// Teams are the teams the usage can be filtered on; TeamFilter the
	// selected one. TeamOnly hides the organization figures from a viewer
	// that reads the usage of its teams only.
	Teams      []model.Team
	TeamFilter model.TeamID
	TeamOnly   bool
	// BudgetTitle and BudgetHref name the budget shown by the budget card and
	// the page managing it: the organization's, or the filtered team's.
	BudgetTitle     string
	BudgetHref      string
	Applications    []model.Application
	ApplicationsMap map[model.ApplicationID]model.Application
	Since           time.Time
	Range           string // "7d", "30d", "90d", "180d", "365d"
	Page            int
	PageSize        int
	HasNext         bool
	// Subscription providers plan consumption
	SubscriptionProviders []SubscriptionProviderUsage
	// Chart/quota fields
	OrgQuota    model.Quota // may be nil if no quota defined
	DailyCost   int64       // today's org cost in org currency (microcents)
	MonthlyCost int64       // this month's org cost in org currency (microcents)
	YearlyCost  int64       // this year's org cost in org currency (microcents)
	Currency    string      // org currency
	ChartPerDay []ChartDataPoint
	// ChartCoveredPerDay is the value of the requests covered by a subscription,
	// stacked on ChartPerDay bar for bar.
	ChartCoveredPerDay  []ChartDataPoint
	ChartSharesPerModel []ChartShare
	ChartPerUser        []ChartDataPoint
	ChartPerProvider    []ChartDataPoint
	// PlanCoveredProviders holds the labels of ChartPerProvider billed by
	// subscription.
	PlanCoveredProviders map[string]bool
	// Consommation (en tokens) des requêtes couvertes par un abonnement, par utilisateur.
	// Séparé des graphiques de coût car le forfait est un coût fixe, pas marginal.
	ChartSubTokensPerUser []ChartDataPoint
	// Energy estimation
	TotalEnergyWh    float64 // sum of midpoint estimates (0 if all unknown)
	TotalCO2GramsMid float64 // sum of CO₂ midpoints (world average, grams)
}

type VirtualModelsPageVModel struct {
	common.AppLayoutVModel
	Org           model.Organization
	VirtualModels []model.VirtualModel
	// Selected is the virtual model whose pipeline the right pane previews. The
	// screen is a master/detail, so it is never nil when VirtualModels is not
	// empty — the handler falls back to the first one.
	Selected model.VirtualModel
	// SelectedTeams are the teams granted the selected model.
	SelectedTeams []model.Team
	BaseURL       string
	Success       string
	Error         string
}

type VirtualModelFormVModel struct {
	common.AppLayoutVModel
	Org          model.Organization
	VirtualModel model.VirtualModel
	IsNew        bool
	Name         string
	Description  string
	Error        string
}

type RolesPageVModel struct {
	common.AppLayoutVModel
	Org   model.Organization
	Roles []model.Role
	// Query filters the permission rows on their code, their label or the label
	// of their group.
	Query string
	// RoleID, when set, keeps only the permissions that role grants — the way to
	// audit one role in a matrix that shows them all.
	RoleID  string
	Success string
}

// ModelGrantOption is a selectable model (LLM or virtual) for team model
// grants.
type ModelGrantOption struct {
	ID    string
	Kind  string // rbac.ModelKindLLM | rbac.ModelKindVirtual
	Label string
}

type RoleFormVModel struct {
	common.AppLayoutVModel
	Org      model.Organization
	Role     model.Role
	IsNew    bool
	Error    string
	Selected map[string]bool // permission code -> checked
}

// roleLabelFromMap uses only roles resolved in the invitation's organization,
// including explicitly mapped legacy builtin codes.
func roleLabelFromMap(names map[string]string, roleID string) string {
	if names != nil {
		if name, ok := names[roleID]; ok {
			return name
		}
	}
	return "Rôle indisponible"
}

// BuiltinRoleLabel returns the French label for a builtin role kind.
func BuiltinRoleLabel(kind string) string {
	switch kind {
	case model.BuiltinKindOwner:
		return "Propriétaire"
	case model.BuiltinKindAdmin:
		return "Administrateur"
	default:
		return "Utilisateur"
	}
}
