package rbac

import (
	"slices"
	"strings"
)

// Permission is a fixed authorization code checked at enforcement points
// (HTTP routes, proxy hooks). Permissions are defined in code — the code is
// the source of truth — and only known codes are ever exposed to the UI.
type Permission string

const (
	// Admin sections (read/write). A GET route maps to ":read", a
	// POST/DELETE route maps to ":write". "write" implies "read".
	PermMembersRead  Permission = "members:read"
	PermMembersWrite Permission = "members:write"

	PermRolesRead  Permission = "roles:read"
	PermRolesWrite Permission = "roles:write"

	// Teams: creating teams and deciding what they get (models, middlewares,
	// budget envelope). Managing who is in a team is members:write, which can
	// be held on the team alone.
	PermTeamsRead  Permission = "teams:read"
	PermTeamsWrite Permission = "teams:write"

	// Providers also cover the LLM models declared under /providers/...
	PermProvidersRead  Permission = "providers:read"
	PermProvidersWrite Permission = "providers:write"

	PermVirtualModelsRead  Permission = "virtual-models:read"
	PermVirtualModelsWrite Permission = "virtual-models:write"

	PermMiddlewaresRead  Permission = "middlewares:read"
	PermMiddlewaresWrite Permission = "middlewares:write"

	PermQuotaRead  Permission = "quota:read"
	PermQuotaWrite Permission = "quota:write"

	PermInvitesRead  Permission = "invites:read"
	PermInvitesWrite Permission = "invites:write"

	PermApplicationsRead  Permission = "applications:read"
	PermApplicationsWrite Permission = "applications:write"

	PermSettingsRead  Permission = "settings:read"
	PermSettingsWrite Permission = "settings:write"

	PermUsageRead Permission = "usage:read"

	// Events. Viewing one's own events requires no permission; these gate access
	// to broader event visibility and alert management.
	PermEventsReadAll  Permission = "events:read:all"   // global + other users' events
	PermEventsWrite    Permission = "events:write"      // manage org-wide alerts
	PermEventsAlertsOwn Permission = "events:alerts:own" // create/manage personal alerts on own events

	// Model usage (enforced at the proxy).
	PermModelUseOrg     Permission = "model:use:org"
	PermModelUseVirtual Permission = "model:use:virtual"

	// Personal virtual models.
	PermPersonalVMCreate Permission = "personal-vm:create"
)

// Model grant kinds, used by resource-scoped model access.
const (
	ModelKindLLM     = "llm"
	ModelKindVirtual = "virtual"
)

// writeImplies maps a "write" permission to the "read" permission it grants
// implicitly. Granting write access always grants the matching read access.
var writeImplies = map[Permission]Permission{
	PermMembersWrite:       PermMembersRead,
	PermRolesWrite:         PermRolesRead,
	PermTeamsWrite:         PermTeamsRead,
	PermProvidersWrite:     PermProvidersRead,
	PermVirtualModelsWrite: PermVirtualModelsRead,
	PermMiddlewaresWrite:   PermMiddlewaresRead,
	PermQuotaWrite:         PermQuotaRead,
	PermInvitesWrite:       PermInvitesRead,
	PermApplicationsWrite:  PermApplicationsRead,
	PermSettingsWrite:      PermSettingsRead,
	PermEventsWrite:        PermEventsReadAll,
}

// Implies returns the permission that `code` grants implicitly, if any.
//
// The UI needs this to explain itself: a permission matrix that lets a role's
// permissions be toggled must distinguish a permission that was granted from one
// that comes along with another, or unchecking the latter would appear to do
// nothing — NewPermissionSet re-derives it on the next read.
func Implies(code Permission) (Permission, bool) {
	implied, ok := writeImplies[code]
	return implied, ok
}

// PermissionDef describes a single permission for display in the UI.
type PermissionDef struct {
	Code  Permission
	Label string
	// TeamScopable permissions can be granted on a team only, by a role
	// assigned to a team member. The others are only granted on the whole
	// organization.
	TeamScopable bool
}

// PermissionGroup groups permissions by admin section for the UI.
type PermissionGroup struct {
	Section string
	Label   string
	Perms   []PermissionDef
}

// Catalog returns the full, grouped list of assignable permissions. It is the
// single source of truth for the role-editing UI and for IsKnown validation.
func Catalog() []PermissionGroup {
	return []PermissionGroup{
		{
			Section: "members",
			Label:   "Membres",
			Perms: []PermissionDef{
				{PermMembersRead, "Consulter les membres", true},
				{PermMembersWrite, "Gérer les membres", true},
			},
		},
		{
			Section: "roles",
			Label:   "Rôles & permissions",
			Perms: []PermissionDef{
				{PermRolesRead, "Consulter les rôles", false},
				{PermRolesWrite, "Gérer les rôles", false},
			},
		},
		{
			Section: "teams",
			Label:   "Équipes",
			Perms: []PermissionDef{
				{PermTeamsRead, "Consulter les équipes", false},
				{PermTeamsWrite, "Gérer les équipes et leurs accès", false},
			},
		},
		{
			Section: "providers",
			Label:   "Fournisseurs & modèles",
			Perms: []PermissionDef{
				{PermProvidersRead, "Consulter les fournisseurs et modèles", false},
				{PermProvidersWrite, "Gérer les fournisseurs et modèles", false},
			},
		},
		{
			Section: "virtual-models",
			Label:   "Modèles virtuels",
			Perms: []PermissionDef{
				{PermVirtualModelsRead, "Consulter les modèles virtuels", false},
				{PermVirtualModelsWrite, "Gérer les modèles virtuels", false},
			},
		},
		{
			Section: "middlewares",
			Label:   "Middlewares",
			Perms: []PermissionDef{
				{PermMiddlewaresRead, "Consulter les middlewares", false},
				{PermMiddlewaresWrite, "Gérer les middlewares", false},
			},
		},
		{
			Section: "quota",
			Label:   "Budget",
			Perms: []PermissionDef{
				{PermQuotaRead, "Consulter le budget", true},
				{PermQuotaWrite, "Gérer le budget", true},
			},
		},
		{
			Section: "invites",
			Label:   "Invitations",
			Perms: []PermissionDef{
				{PermInvitesRead, "Consulter les invitations", true},
				{PermInvitesWrite, "Gérer les invitations", true},
			},
		},
		{
			Section: "applications",
			Label:   "Applications",
			Perms: []PermissionDef{
				{PermApplicationsRead, "Consulter les applications", false},
				{PermApplicationsWrite, "Gérer les applications", false},
			},
		},
		{
			Section: "settings",
			Label:   "Paramètres",
			Perms: []PermissionDef{
				{PermSettingsRead, "Consulter les paramètres", false},
				{PermSettingsWrite, "Gérer les paramètres", false},
			},
		},
		{
			Section: "usage",
			Label:   "Usage",
			Perms: []PermissionDef{
				{PermUsageRead, "Consulter l'usage", true},
			},
		},
		{
			Section: "events",
			Label:   "Événements & alertes",
			Perms: []PermissionDef{
				{PermEventsReadAll, "Consulter les événements globaux et des autres utilisateurs", true},
				{PermEventsWrite, "Gérer les alertes de l'organisation", false},
				{PermEventsAlertsOwn, "Créer des alertes sur ses propres événements", false},
			},
		},
		{
			Section: "models",
			Label:   "Usage des modèles",
			Perms: []PermissionDef{
				{PermModelUseOrg, "Utiliser tous les modèles de l'organisation, sans passer par une équipe", false},
				{PermModelUseVirtual, "Utiliser tous les modèles virtuels, sans passer par une équipe", false},
			},
		},
		{
			Section: "personal",
			Label:   "Personnel",
			Perms: []PermissionDef{
				{PermPersonalVMCreate, "Créer des modèles virtuels personnels", false},
			},
		},
	}
}

// knownPermissions is built once from the catalog for fast validation.
var knownPermissions = func() map[Permission]struct{} {
	known := map[Permission]struct{}{}
	for _, group := range Catalog() {
		for _, def := range group.Perms {
			known[def.Code] = struct{}{}
		}
	}
	return known
}()

// teamScopable is built once from the catalog.
var teamScopable = func() map[Permission]struct{} {
	scopable := map[Permission]struct{}{}
	for _, group := range Catalog() {
		for _, def := range group.Perms {
			if def.TeamScopable {
				scopable[def.Code] = struct{}{}
			}
		}
	}
	return scopable
}()

// IsKnown reports whether code corresponds to a declared permission.
func IsKnown(code string) bool {
	_, ok := knownPermissions[Permission(code)]
	return ok
}

// IsTeamScopable reports whether the permission can be granted on a team only.
func IsTeamScopable(code Permission) bool {
	_, ok := teamScopable[code]
	return ok
}

// IsAdminPermission reports whether the permission grants access to an
// administration section (as opposed to plain usage permissions). It is used to
// decide whether to surface the organization admin navigation to a member.
func IsAdminPermission(code Permission) bool {
	switch code {
	case PermUsageRead, PermModelUseOrg, PermModelUseVirtual, PermPersonalVMCreate, PermEventsAlertsOwn:
		return false
	}
	return IsKnown(string(code))
}

// ModelGrant authorizes the use of a specific model resource.
type ModelGrant struct {
	ModelID string
	Kind    string // ModelKindLLM | ModelKindVirtual
}

// TeamAccess is what a principal can do and use through one of its teams: the
// permissions of the roles it holds on the team scope, and the models the
// team grants.
type TeamAccess struct {
	TeamID string
	Slug   string
	// Codes are the permission codes of the roles held on the team. Only the
	// team-scopable ones are kept (see IsTeamScopable).
	Codes  []string
	Grants []ModelGrant
}

// TeamRef designates a team of a permission set.
type TeamRef struct {
	ID   string
	Slug string
}

type teamSet struct {
	ref    TeamRef
	codes  map[Permission]struct{}
	grants map[string]struct{}
}

// PermissionSet holds the effective permissions resolved for a principal
// within an organization: the union of the permissions of the roles it holds on
// the organization, and per team, the permissions of the roles held on the
// team and the models the team grants.
type PermissionSet struct {
	owner bool
	codes map[Permission]struct{}
	// teams are ordered by slug.
	teams []*teamSet
}

// NewPermissionSet builds a permission set from the permission codes held on
// the organization and the access held through teams. Unknown codes are
// ignored, and so are team codes that cannot be scoped to a team. The "write
// implies read" rule is applied here so callers only ever need to check the
// granular permission.
func NewPermissionSet(codes []string, teams []TeamAccess) PermissionSet {
	set := PermissionSet{codes: expandCodes(codes, false)}
	for _, team := range teams {
		ts := &teamSet{
			ref:    TeamRef{ID: team.TeamID, Slug: team.Slug},
			codes:  expandCodes(team.Codes, true),
			grants: make(map[string]struct{}, len(team.Grants)),
		}
		for _, grant := range team.Grants {
			ts.grants[grantKey(grant.Kind, grant.ModelID)] = struct{}{}
		}
		set.teams = append(set.teams, ts)
	}
	slices.SortFunc(set.teams, func(a, b *teamSet) int {
		if c := strings.Compare(a.ref.Slug, b.ref.Slug); c != 0 {
			return c
		}
		return strings.Compare(a.ref.ID, b.ref.ID)
	})
	return set
}

func expandCodes(codes []string, teamScoped bool) map[Permission]struct{} {
	out := make(map[Permission]struct{}, len(codes))
	for _, raw := range codes {
		code := Permission(raw)
		if _, ok := knownPermissions[code]; !ok {
			continue
		}
		if teamScoped && !IsTeamScopable(code) {
			continue
		}
		out[code] = struct{}{}
		if implied, ok := writeImplies[code]; ok {
			out[implied] = struct{}{}
		}
	}
	return out
}

// OwnerPermissionSet returns a set that authorizes everything. It is used for
// the global admin and for members holding the builtin "owner" org role.
func OwnerPermissionSet() PermissionSet {
	return PermissionSet{owner: true}
}

// WithTeams returns the set with the given team access, keeping its other
// permissions. An owner set keeps authorizing everything, and gains the teams
// so that requests it makes through them are attributed to them.
func (s PermissionSet) WithTeams(teams []TeamAccess) PermissionSet {
	codes := make([]string, 0, len(s.codes))
	for code := range s.codes {
		codes = append(codes, string(code))
	}
	out := NewPermissionSet(codes, teams)
	out.owner = s.owner
	return out
}

// AsOwner returns the set authorizing everything, its teams kept for
// attribution.
func (s PermissionSet) AsOwner() PermissionSet {
	s.owner = true
	return s
}

func grantKey(kind, modelID string) string {
	return kind + "\x00" + modelID
}

// IsOwner reports whether the set bypasses all permission checks.
func (s PermissionSet) IsOwner() bool {
	return s.owner
}

// Has reports whether the set grants the given permission on the
// organization.
func (s PermissionSet) Has(perm Permission) bool {
	if s.owner {
		return true
	}
	_, ok := s.codes[perm]
	return ok
}

func (s PermissionSet) team(teamID string) *teamSet {
	for _, t := range s.teams {
		if t.ref.ID == teamID {
			return t
		}
	}
	return nil
}

// HasInTeam reports whether the set grants the permission on the team: on the
// organization, or through a role held on the team.
func (s PermissionSet) HasInTeam(teamID string, perm Permission) bool {
	if s.Has(perm) {
		return true
	}
	t := s.team(teamID)
	if t == nil {
		return false
	}
	_, ok := t.codes[perm]
	return ok
}

// HasInAnyTeam reports whether the set grants the permission on the
// organization or on at least one team.
func (s PermissionSet) HasInAnyTeam(perm Permission) bool {
	if s.Has(perm) {
		return true
	}
	for _, t := range s.teams {
		if _, ok := t.codes[perm]; ok {
			return true
		}
	}
	return false
}

// Teams returns the teams the principal belongs to, ordered by slug.
func (s PermissionSet) Teams() []TeamRef {
	refs := make([]TeamRef, 0, len(s.teams))
	for _, t := range s.teams {
		refs = append(refs, t.ref)
	}
	return refs
}

// InTeam reports whether the principal belongs to the team.
func (s PermissionSet) InTeam(teamID string) bool {
	return s.team(teamID) != nil
}

// HasOrgWideModelAccess reports whether the set can use every model of the
// given kind of the organization, whatever its teams: the owner bypass, or
// model:use:org for real models and model:use:virtual for virtual ones. It is
// the only access that does not go through a team.
func (s PermissionSet) HasOrgWideModelAccess(kind string) bool {
	switch kind {
	case ModelKindLLM:
		return s.Has(PermModelUseOrg)
	case ModelKindVirtual:
		return s.Has(PermModelUseVirtual)
	}
	return s.owner
}

// TeamGrants reports whether the team, which the principal must belong to,
// grants the model.
func (s PermissionSet) TeamGrants(teamID, modelID, kind string) bool {
	t := s.team(teamID)
	if t == nil {
		return false
	}
	_, ok := t.grants[grantKey(kind, modelID)]
	return ok
}

// CanUseModel reports whether a request attributed to teamID, or to the
// organization alone when teamID is empty, may use the model.
func (s PermissionSet) CanUseModel(teamID, modelID, kind string) bool {
	if s.HasOrgWideModelAccess(kind) {
		return true
	}
	return teamID != "" && s.TeamGrants(teamID, modelID, kind)
}

// TeamsGranting returns the teams of the principal granting the model,
// ordered by slug.
func (s PermissionSet) TeamsGranting(modelID, kind string) []TeamRef {
	var refs []TeamRef
	for _, t := range s.teams {
		if _, ok := t.grants[grantKey(kind, modelID)]; ok {
			refs = append(refs, t.ref)
		}
	}
	return refs
}

// HasAnyModelAccess reports whether the set can use at least one model of the
// given kind, org-wide or through a team. Pass an empty kind to match any.
func (s PermissionSet) HasAnyModelAccess(kind string) bool {
	if kind == "" {
		return s.HasAnyModelAccess(ModelKindLLM) || s.HasAnyModelAccess(ModelKindVirtual)
	}
	if s.HasOrgWideModelAccess(kind) {
		return true
	}
	for _, t := range s.teams {
		for key := range t.grants {
			if strings.HasPrefix(key, kind+"\x00") {
				return true
			}
		}
	}
	return false
}
