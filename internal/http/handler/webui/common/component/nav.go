package component

import (
	"context"
	"sort"
	"strings"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/templui/component/icon"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authz"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

// resolve fills the fields of the layout view model the handlers do not set
// themselves. Handlers declare *where* they are (Context, ContextSlug,
// SelectedItem); the navigation, the switcher and the admin-visit flag are
// derived from that plus the request context.
//
// Any field already set by a handler is left untouched, so a page can always
// override the defaults.
func (v AppLayoutVModel) resolve(ctx context.Context) AppLayoutVModel {
	if v.Context == "" {
		v.Context = ContextPersonal
	}
	if v.User == nil {
		v.User = User(ctx)
	}
	if v.ContextName == "" {
		// The personal space is named after its only inhabitant, so the switcher
		// reads "ESPACE PERSONNEL / cmsassot" and its pastille carries the same
		// initials as the user avatar in the footer.
		if v.Context == ContextPersonal && v.User != nil {
			v.ContextName = v.User.DisplayName()
		} else {
			v.ContextName = v.Context.DefaultName(ctx)
		}
	}
	if v.HomeLink == "" {
		v.HomeLink = contextHome(ctx, v.Context, v.ContextSlug)
	}
	if v.NavGroups == nil {
		switch v.Context {
		case ContextOrg:
			v.NavGroups = OrgNavGroups(ctx, v.ContextSlug, v.ContextOrgID, v.SelectedItem)
		case ContextPlatform:
			v.NavGroups = PlatformNavGroups(ctx, v.SelectedItem)
		default:
			v.NavGroups = PersonalNavGroups(ctx, v.SelectedItem)
		}
	}
	if v.Switcher == nil {
		v.Switcher = SwitcherEntries(ctx, v.Context, v.ContextSlug)
	}
	if v.Context == ContextOrg && !v.IsAdminVisit {
		v.IsAdminVisit = isAdminVisit(ctx, v.ContextOrgID)
	}
	return v
}

// contextHome returns the landing page of a context, used by the brand link.
func contextHome(ctx context.Context, c Context, slug string) string {
	switch c {
	case ContextOrg:
		return BaseURLString(ctx, WithPath("/orgs/", slug, "/usage"))
	case ContextPlatform:
		return BaseURLString(ctx, WithPath("/admin/"))
	default:
		return BaseURLString(ctx, WithPath("/usage"))
	}
}

// isAdminVisit reports a platform administrator browsing an organisation they
// are not a member of.
func isAdminVisit(ctx context.Context, orgID model.OrgID) bool {
	if orgID == "" || !AssertUser(ctx, authz.Has(authz.RoleAdmin)) {
		return false
	}
	for _, m := range httpCtx.Memberships(ctx) {
		if m.OrgID() == orgID {
			return false
		}
	}
	return true
}

// PersonalNavGroups builds the sidebar of the personal space.
func PersonalNavGroups(ctx context.Context, selected string) []NavGroup {
	space := NavGroup{Title: i18n.T(ctx, "nav.group.my_space"), Items: []NavEntry{
		{Label: i18n.T(ctx, "nav.my_usage"), Href: BaseURLString(ctx, WithPath("/usage")), Icon: icon.ChartColumn, Active: selected == "usage"},
		{Label: i18n.T(ctx, "nav.events"), Href: BaseURLString(ctx, WithPath("/events")), Icon: icon.Bell, Active: selected == "events"},
	}}
	if CanAccessModelsPage(ctx) {
		space.Items = append(space.Items, NavEntry{
			Label: i18n.T(ctx, "nav.org_models"), Href: BaseURLString(ctx, WithPath("/models")),
			Icon: icon.BrainCircuit, Active: selected == "models",
		})
	}
	if HasPermissionInAnyOrg(ctx, rbac.PermPersonalVMCreate) {
		space.Items = append(space.Items, NavEntry{
			Label: i18n.T(ctx, "nav.my_models"), Href: BaseURLString(ctx, WithPath("/profile/personal-models")),
			Icon: icon.Network, Active: selected == "personal-models",
		})
	}

	// Les clés API sont un outil de travail, pas un réglage de compte : elles
	// closent « Mon espace », à côté de l'usage et des modèles qu'elles servent.
	space.Items = append(space.Items, NavEntry{
		Label: i18n.T(ctx, "nav.api_keys"), Href: BaseURLString(ctx, WithPath("/profile/tokens")),
		Icon: icon.KeyRound, Active: selected == "tokens",
	})

	account := NavGroup{Title: i18n.T(ctx, "nav.group.account"), Items: []NavEntry{
		{Label: i18n.T(ctx, "nav.profile"), Href: BaseURLString(ctx, WithPath("/profile")), Icon: icon.User, Active: selected == "profile"},
	}}

	return []NavGroup{space, account}
}

// PlatformNavGroups builds the sidebar of the platform console.
func PlatformNavGroups(ctx context.Context, selected string) []NavGroup {
	// Organisations sits under Supervision, not Administration: the console reads
	// the fleet from there — cost, budget, error rate — and only then acts on it.
	supervision := NavGroup{Title: i18n.T(ctx, "nav.group.supervision"), Items: []NavEntry{
		{Label: i18n.T(ctx, "nav.overview"), Href: BaseURLString(ctx, WithPath("/admin/")), Icon: icon.Gauge, Active: selected == "overview"},
		{Label: i18n.T(ctx, "nav.orgs"), Href: BaseURLString(ctx, WithPath("/admin/orgs")), Icon: icon.Building2, Active: selected == "orgs"},
		{Label: i18n.T(ctx, "nav.proxy_health"), Href: BaseURLString(ctx, WithPath("/admin/health")), Icon: icon.Activity, Active: selected == "health"},
	}}

	administration := NavGroup{Title: i18n.T(ctx, "nav.group.administration"), Items: []NavEntry{
		{Label: i18n.T(ctx, "nav.users"), Href: BaseURLString(ctx, WithPath("/admin/users")), Icon: icon.Users, Active: selected == "users"},
		{Label: i18n.T(ctx, "nav.exchange_rates"), Href: BaseURLString(ctx, WithPath("/admin/exchange-rates")), Icon: icon.RefreshCw, Active: selected == "exchange-rates"},
		{Label: i18n.T(ctx, "nav.plugins"), Href: BaseURLString(ctx, WithPath("/admin/plugins")), Icon: icon.Plug, Active: selected == "plugins"},
	}}

	return []NavGroup{supervision, administration}
}

// OrgNavGroups builds the sidebar of an organisation, filtered by the current
// user's permissions within it.
func OrgNavGroups(ctx context.Context, slug string, orgID model.OrgID, selected string) []NavGroup {
	prefix := "org-" + slug + "-"
	href := func(parts ...string) string {
		return BaseURLString(ctx, WithPath(append([]string{"/orgs/", slug}, parts...)...))
	}

	usage := NavGroup{Title: i18n.T(ctx, "nav.group.usage"), Items: []NavEntry{
		{
			Label: i18n.T(ctx, "nav.dashboard"), Href: href("/usage"), Icon: icon.ChartColumn,
			// The org root redirects to /usage, so both selections light the same row.
			Active: selected == prefix+"usage" || selected == "org-"+slug,
		},
		{Label: i18n.T(ctx, "nav.events"), Href: href("/events"), Icon: icon.Bell, Active: selected == prefix+"events"},
	}}
	if HasPermission(ctx, orgID, rbac.PermEventsWrite) || HasPermission(ctx, orgID, rbac.PermEventsAlertsOwn) {
		usage.Items = append(usage.Items, NavEntry{
			Label: i18n.T(ctx, "nav.alerts"), Href: href("/events/alerts"), Icon: icon.TriangleAlert,
			Active: selected == prefix+"alerts",
		})
	}
	// Budget opens Gouvernance rather than closing Usage: a ceiling is a decision
	// taken on the organisation, next to who may spend it and with which role.
	governance := NavGroup{Title: i18n.T(ctx, "nav.group.governance")}
	if HasPermission(ctx, orgID, rbac.PermQuotaRead) {
		governance.Items = append(governance.Items, NavEntry{
			Label: i18n.T(ctx, "nav.budget"), Href: href("/admin/quota"), Icon: icon.PiggyBank, Active: selected == prefix+"quota",
		})
	}
	if HasPermission(ctx, orgID, rbac.PermMembersRead) {
		governance.Items = append(governance.Items, NavEntry{
			Label: i18n.T(ctx, "nav.members"), Href: href("/admin/members"), Icon: icon.Users, Active: selected == prefix+"members",
		})
	}
	// A team lead sees the teams it holds a role on, without teams:read.
	if HasPermission(ctx, orgID, rbac.PermTeamsRead) || HasPermissionInAnyTeam(ctx, orgID, rbac.PermMembersRead) ||
		HasPermissionInAnyTeam(ctx, orgID, rbac.PermQuotaRead) {
		governance.Items = append(governance.Items, NavEntry{
			Label: i18n.T(ctx, "nav.teams"), Href: href("/admin/teams"), Icon: icon.UsersRound, Active: selected == prefix+"teams",
		})
	}
	if HasPermission(ctx, orgID, rbac.PermRolesRead) {
		governance.Items = append(governance.Items, NavEntry{
			Label: i18n.T(ctx, "nav.roles"), Href: href("/admin/roles"), Icon: icon.Shield, Active: selected == prefix+"roles",
		})
	}
	if HasPermissionInAnyTeam(ctx, orgID, rbac.PermInvitesRead) {
		governance.Items = append(governance.Items, NavEntry{
			Label: i18n.T(ctx, "nav.invitations"), Href: href("/admin/invites"), Icon: icon.Mail, Active: selected == prefix+"invites",
		})
	}
	gateway := NavGroup{Title: i18n.T(ctx, "nav.group.gateway")}
	if HasPermission(ctx, orgID, rbac.PermProvidersRead) {
		gateway.Items = append(gateway.Items, NavEntry{
			Label: i18n.T(ctx, "nav.providers"), Href: href("/admin/providers"), Icon: icon.Server, Active: selected == prefix+"providers",
		})
	}
	if HasPermission(ctx, orgID, rbac.PermVirtualModelsRead) {
		gateway.Items = append(gateway.Items, NavEntry{
			Label: i18n.T(ctx, "nav.virtual_models"), Href: href("/admin/virtual-models"), Icon: icon.BrainCircuit, Active: selected == prefix+"virtual-models",
		})
	}
	if HasPermission(ctx, orgID, rbac.PermMiddlewaresRead) {
		gateway.Items = append(gateway.Items, NavEntry{
			Label: i18n.T(ctx, "nav.middlewares"), Href: href("/admin/middlewares"), Icon: icon.Layers, Active: selected == prefix+"middlewares",
		})
	}
	if HasPermission(ctx, orgID, rbac.PermApplicationsRead) {
		gateway.Items = append(gateway.Items, NavEntry{
			Label: i18n.T(ctx, "nav.applications"), Href: href("/admin/applications"), Icon: icon.AppWindow, Active: selected == prefix+"applications",
		})
	}
	// Paramètres closes Passerelle: what it holds — devise, rétention, plafonds —
	// configures the gateway of the organisation rather than governing its people.
	if HasPermission(ctx, orgID, rbac.PermSettingsRead) {
		gateway.Items = append(gateway.Items, NavEntry{
			Label: i18n.T(ctx, "nav.settings"), Href: href("/admin/settings"), Icon: icon.Settings, Active: selected == prefix+"settings",
		})
	}

	groups := []NavGroup{usage}
	for _, g := range []NavGroup{governance, gateway} {
		if len(g.Items) > 0 {
			groups = append(groups, g)
		}
	}
	return groups
}

// SwitcherEntries lists the destinations of the context popover: the personal
// space, every organisation the user belongs to, and — for platform admins — the
// console.
func SwitcherEntries(ctx context.Context, current Context, currentSlug string) []SwitcherEntry {
	// The row is labelled with the user, not with "Espace personnel": the group
	// heading above it already carries that, and the pastille then matches the
	// avatar of the sidebar footer.
	personal := i18n.T(ctx, "nav.context.personal_name")
	if user := User(ctx); user != nil {
		personal = user.DisplayName()
	}
	entries := []SwitcherEntry{{
		Context: ContextPersonal,
		Label:   personal,
		Href:    BaseURLString(ctx, WithPath("/usage")),
		Current: current == ContextPersonal,
	}}

	memberships := httpCtx.Memberships(ctx)
	orgs := make([]SwitcherEntry, 0, len(memberships))
	for _, m := range memberships {
		org := m.Org()
		if org == nil {
			continue
		}
		// TODO(rework-ux): the mockup shows a 30-day cost and a budget gauge next to
		// each organisation. No cross-org aggregate is exposed by the domain yet, so
		// Cost and Usage stay empty and the popover renders the row without them.
		orgs = append(orgs, SwitcherEntry{
			Context: ContextOrg,
			Label:   org.Name(),
			Slug:    org.Slug(),
			Href:    BaseURLString(ctx, WithPath("/orgs/", org.Slug(), "/usage")),
			Current: current == ContextOrg && org.Slug() == currentSlug,
		})
	}
	sort.Slice(orgs, func(i, j int) bool {
		return strings.ToLower(orgs[i].Label) < strings.ToLower(orgs[j].Label)
	})
	entries = append(entries, orgs...)

	if AssertUser(ctx, authz.Has(authz.RoleAdmin)) {
		entries = append(entries, SwitcherEntry{
			Context: ContextPlatform,
			Label:   i18n.T(ctx, "switcher.platform_console"),
			Href:    BaseURLString(ctx, WithPath("/admin/")),
			Current: current == ContextPlatform,
		})
	}

	return entries
}

// FilterSwitcherEntries keeps the entries whose name or slug contains q, matched
// case-insensitively and accent-insensitively enough for the two fields the
// switcher displays.
func FilterSwitcherEntries(entries []SwitcherEntry, q string) []SwitcherEntry {
	q = strings.TrimSpace(strings.ToLower(q))
	if q == "" {
		return entries
	}
	filtered := make([]SwitcherEntry, 0, len(entries))
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Label), q) || strings.Contains(strings.ToLower(e.Slug), q) {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// SwitcherFragmentURL is the endpoint the switcher search calls. The current
// context travels in the query string so the fragment can still mark the entry
// the user is on.
func SwitcherFragmentURL(ctx context.Context, current Context, currentSlug string) string {
	return BaseURLString(ctx,
		WithPath("/switcher"),
		WithValues("context", string(current), "slug", currentSlug),
	)
}

// UserRoleLabel returns the short role qualifier displayed under the user's name
// in the sidebar footer.
func UserRoleLabel(ctx context.Context, vmodel AppLayoutVModel) string {
	if vmodel.Context == ContextPlatform || (vmodel.Context == ContextOrg && vmodel.IsAdminVisit) {
		return i18n.T(ctx, "nav.role.platform_admin")
	}
	if vmodel.Context == ContextOrg {
		for _, m := range httpCtx.Memberships(ctx) {
			if m.OrgID() != vmodel.ContextOrgID {
				continue
			}
			names := make([]string, 0, len(m.Roles()))
			for _, r := range m.Roles() {
				names = append(names, r.Name())
			}
			if len(names) > 0 {
				return strings.Join(names, ", ")
			}
		}
		return i18n.T(ctx, "nav.role.member")
	}
	if AssertUser(ctx, authz.Has(authz.RoleAdmin)) {
		return i18n.T(ctx, "nav.role.platform_admin")
	}
	return i18n.T(ctx, "nav.role.user")
}
