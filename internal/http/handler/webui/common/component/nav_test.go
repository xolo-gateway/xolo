package component

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authz"
)

// fakeMembership is a minimal model.Membership carrying a preloaded org, which
// the domain constructors do not expose a setter for.
type fakeMembership struct {
	orgID model.OrgID
	org   model.Organization
	roles []model.Role
}

func (m fakeMembership) ID() model.MembershipID  { return model.NewMembershipID() }
func (m fakeMembership) UserID() model.UserID    { return "user-1" }
func (m fakeMembership) OrgID() model.OrgID      { return m.orgID }
func (m fakeMembership) CreatedAt() time.Time    { return time.Time{} }
func (m fakeMembership) User() model.User        { return nil }
func (m fakeMembership) Org() model.Organization { return m.org }
func (m fakeMembership) Roles() []model.Role     { return m.roles }

// testContext builds the request-scoped values the layout reads: a base URL, the
// current user, their memberships and a permission resolver.
func testContext(user model.User, memberships []model.Membership, perms map[model.OrgID]rbac.PermissionSet) context.Context {
	ctx := httpCtx.SetBaseURL(context.Background(), "http://xolo.test")
	ctx = httpCtx.SetUser(ctx, user)
	ctx = httpCtx.SetMemberships(ctx, memberships)
	return httpCtx.SetPermissionResolver(ctx, func(ctx context.Context, orgID model.OrgID) (rbac.PermissionSet, error) {
		return perms[orgID], nil
	})
}

func groupTitles(groups []NavGroup) []string {
	titles := make([]string, 0, len(groups))
	for _, g := range groups {
		titles = append(titles, g.Title)
	}
	return titles
}

func findEntry(groups []NavGroup, label string) (NavEntry, bool) {
	for _, g := range groups {
		for _, item := range g.Items {
			if item.Label == label {
				return item, true
			}
		}
	}
	return NavEntry{}, false
}

func TestPersonalNavGroups(t *testing.T) {
	user := model.NewUser(testTenantID, "test", "subject", "user@xolo.test", "Ada Lovelace", true, authz.RoleUser)
	ctx := testContext(user, nil, nil)

	groups := PersonalNavGroups(ctx, "tokens")

	if got, want := groupTitles(groups), []string{"Mon espace", "Compte"}; !equalStrings(got, want) {
		t.Errorf("group titles: got %v, want %v", got, want)
	}

	entry, ok := findEntry(groups, "Clés API")
	if !ok {
		t.Fatal(`expected a "Clés API" entry`)
	}
	if !entry.Active {
		t.Error(`"Clés API" should be active for selected item "tokens"`)
	}
	if want := "http://xolo.test/profile/tokens"; entry.Href != want {
		t.Errorf("href: got %q, want %q", entry.Href, want)
	}

	// Without any membership the org-scoped entries must not appear.
	if _, ok := findEntry(groups, "Modèles d'org."); ok {
		t.Error(`"Modèles d'org." should be hidden without an org granting model access`)
	}
	if _, ok := findEntry(groups, "Mes modèles"); ok {
		t.Error(`"Mes modèles" should be hidden without the personal VM permission`)
	}
}

func TestPlatformNavGroups(t *testing.T) {
	user := model.NewUser(testTenantID, "test", "subject", "admin@xolo.test", "Root", true, authz.RoleAdmin)
	ctx := testContext(user, nil, nil)

	groups := PlatformNavGroups(ctx, "users")

	if got, want := groupTitles(groups), []string{"Supervision", "Administration"}; !equalStrings(got, want) {
		t.Errorf("group titles: got %v, want %v", got, want)
	}

	// Both supervision entries got a route in lot 3; the overview lands on the
	// console root, which used to redirect to the organisation list.
	overview, ok := findEntry(groups, "Vue d'ensemble")
	if !ok {
		t.Fatal(`expected a "Vue d'ensemble" entry`)
	}
	if overview.Disabled || overview.ComingSoon {
		t.Error(`"Vue d'ensemble" is a real screen and must not be flagged coming soon`)
	}
	if got, want := overview.Href, "http://xolo.test/admin/"; got != want {
		t.Errorf("overview href: got %q, want %q", got, want)
	}

	health, ok := findEntry(groups, "Santé du proxy")
	if !ok {
		t.Fatal(`expected a "Santé du proxy" entry`)
	}
	if got, want := health.Href, "http://xolo.test/admin/health"; got != want {
		t.Errorf("health href: got %q, want %q", got, want)
	}

	users, _ := findEntry(groups, "Utilisateurs")
	if !users.Active {
		t.Error(`"Utilisateurs" should be active for selected item "users"`)
	}
}

func TestOrgNavGroupsFiltersOnPermissions(t *testing.T) {
	user := model.NewUser(testTenantID, "test", "subject", "user@xolo.test", "Ada Lovelace", true, authz.RoleUser)
	orgID := model.OrgID("org-1")

	t.Run("member without any admin permission", func(t *testing.T) {
		ctx := testContext(user, nil, map[model.OrgID]rbac.PermissionSet{
			orgID: rbac.NewPermissionSet(nil, nil),
		})

		groups := OrgNavGroups(ctx, "acme", orgID, "org-acme-usage")

		if got, want := groupTitles(groups), []string{"Usage"}; !equalStrings(got, want) {
			t.Errorf("group titles: got %v, want %v", got, want)
		}
		dashboard, ok := findEntry(groups, "Tableau de bord")
		if !ok {
			t.Fatal(`expected a "Tableau de bord" entry`)
		}
		if !dashboard.Active {
			t.Error(`"Tableau de bord" should be active for selected item "org-acme-usage"`)
		}
		if want := "http://xolo.test/orgs/acme/usage"; dashboard.Href != want {
			t.Errorf("href: got %q, want %q", dashboard.Href, want)
		}
	})

	t.Run("member with the full permission set", func(t *testing.T) {
		ctx := testContext(user, nil, map[model.OrgID]rbac.PermissionSet{
			orgID: rbac.NewPermissionSet([]string{
				string(rbac.PermQuotaRead),
				string(rbac.PermMembersRead),
				string(rbac.PermRolesRead),
				string(rbac.PermInvitesRead),
				string(rbac.PermSettingsRead),
				string(rbac.PermProvidersRead),
				string(rbac.PermVirtualModelsRead),
				string(rbac.PermMiddlewaresRead),
				string(rbac.PermApplicationsRead),
			}, nil),
		})

		groups := OrgNavGroups(ctx, "acme", orgID, "org-acme-roles")

		if got, want := groupTitles(groups), []string{"Usage", "Gouvernance", "Passerelle"}; !equalStrings(got, want) {
			t.Errorf("group titles: got %v, want %v", got, want)
		}
		roles, ok := findEntry(groups, "Rôles")
		if !ok || !roles.Active {
			t.Error(`"Rôles" should be present and active for selected item "org-acme-roles"`)
		}
	})
}

func TestSwitcherEntriesOrdering(t *testing.T) {
	user := model.NewUser(testTenantID, "test", "subject", "admin@xolo.test", "Root", true, authz.RoleAdmin)
	zebra := model.NewOrganization(testTenantID, "zebra", "Zebra", "")
	acme := model.NewOrganization(testTenantID, "acme", "ACME Corp", "")
	memberships := []model.Membership{
		fakeMembership{orgID: zebra.ID(), org: zebra},
		fakeMembership{orgID: acme.ID(), org: acme},
	}

	entries := SwitcherEntries(testContext(user, memberships, nil), ContextOrg, "acme")

	labels := make([]string, 0, len(entries))
	for _, e := range entries {
		labels = append(labels, e.Label)
	}
	// The personal row is labelled with the user: the group heading above it
	// already reads "Espace personnel", and the pastille must then carry the same
	// initials as the avatar in the sidebar footer.
	want := []string{"Root", "ACME Corp", "Zebra", "Console plateforme"}
	if !equalStrings(labels, want) {
		t.Fatalf("switcher entries: got %v, want %v", labels, want)
	}
	if !entries[1].Current {
		t.Error("the organisation matching the current slug must be marked current")
	}
	if entries[0].Current || entries[3].Current {
		t.Error("only one entry may be marked current")
	}
}

func TestSwitcherOmitsPlatformForNonAdmins(t *testing.T) {
	user := model.NewUser(testTenantID, "test", "subject", "user@xolo.test", "Ada Lovelace", true, authz.RoleUser)

	for _, e := range SwitcherEntries(testContext(user, nil, nil), ContextPersonal, "") {
		if e.Context == ContextPlatform {
			t.Fatal("a non-admin must not be offered the platform console")
		}
	}
}

func TestResolveDefaults(t *testing.T) {
	user := model.NewUser(testTenantID, "test", "subject", "user@xolo.test", "Ada Lovelace", true, authz.RoleUser)
	ctx := testContext(user, nil, nil)

	resolved := AppLayoutVModel{}.resolve(ctx)

	if resolved.Context != ContextPersonal {
		t.Errorf("context: got %q, want %q", resolved.Context, ContextPersonal)
	}
	if resolved.ContextName != "Ada Lovelace" {
		t.Errorf("context name: got %q, want the display name of the user", resolved.ContextName)
	}
	if want := "http://xolo.test/usage"; resolved.HomeLink != want {
		t.Errorf("home link: got %q, want %q", resolved.HomeLink, want)
	}
	if resolved.User == nil {
		t.Error("the user should be picked up from the request context")
	}
	if len(resolved.NavGroups) == 0 {
		t.Error("navigation groups should be derived from the context")
	}
}

func TestResolveDetectsAdminVisit(t *testing.T) {
	orgID := model.OrgID("org-1")
	visited := AppLayoutVModel{Context: ContextOrg, ContextSlug: "acme", ContextOrgID: orgID}

	admin := model.NewUser(testTenantID, "test", "subject", "admin@xolo.test", "Root", true, authz.RoleAdmin)
	member := model.NewUser(testTenantID, "test", "subject", "user@xolo.test", "Ada", true, authz.RoleUser)
	membership := []model.Membership{fakeMembership{orgID: orgID, org: model.NewOrganization(testTenantID, "acme", "ACME", "")}}

	if !visited.resolve(testContext(admin, nil, nil)).IsAdminVisit {
		t.Error("a platform admin browsing an org they do not belong to is an admin visit")
	}
	if visited.resolve(testContext(admin, membership, nil)).IsAdminVisit {
		t.Error("a platform admin who is a member of the org is not visiting")
	}
	if visited.resolve(testContext(member, nil, nil)).IsAdminVisit {
		t.Error("a non-admin can never be flagged as an admin visit")
	}
}

func TestAppLayoutRendersShellAnchors(t *testing.T) {
	user := model.NewUser(testTenantID, "test", "subject", "user@xolo.test", "Ada Lovelace", true, authz.RoleUser)
	ctx := testContext(user, nil, nil)

	var out strings.Builder
	vmodel := AppLayoutVModel{
		SelectedItem: "usage",
		Breadcrumbs:  []BreadcrumbItem{{Label: "Espace personnel", Href: "/usage"}, {Label: "Usage"}},
	}
	if err := AppLayout(vmodel).Render(ctx, &out); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := out.String()

	// Every id named by hx-select-oob must exist in the response, otherwise a
	// boosted navigation leaves part of the shell stale.
	for _, id := range strings.Split(boostedShell, ",") {
		anchor := `id="` + strings.TrimPrefix(id, "#") + `"`
		if !strings.Contains(html, anchor) {
			t.Errorf("missing out-of-band swap anchor %s", anchor)
		}
	}
	if !strings.Contains(html, `id="content"`) {
		t.Error(`missing the #content swap target`)
	}
	// The attribute must sit on <body>: popover portals its content there, and a
	// link inside the context switcher would otherwise have no ancestor to
	// inherit it from.
	bodyTag := html[strings.Index(html, "<body"):]
	bodyTag = bodyTag[:strings.Index(bodyTag, ">")]
	if !strings.Contains(bodyTag, `hx-select-oob="`+boostedShell+`"`) {
		t.Error("<body> should declare the out-of-band regions of the shell")
	}

	// An out-of-band swap replaces a region by id wherever it sits, so a region
	// nested in another would be inserted twice.
	switcher := strings.Index(html, `id="context-switcher"`)
	context := strings.Index(html, `id="app-context"`)
	if switcher > context {
		t.Error("the switcher popover must be rendered outside #app-context")
	}
}

// TestAppLayoutShipsComponentScripts guards the hx-boost invariant: a boosted
// navigation swaps #content and leaves the <head> of the first loaded page in
// place, so a script only some pages declare is missing on all the others. Every
// script a page may need is therefore registered by the layout itself.
func TestAppLayoutShipsComponentScripts(t *testing.T) {
	user := model.NewUser(testTenantID, "test", "subject", "user@xolo.test", "Ada Lovelace", true, authz.RoleUser)
	ctx := testContext(user, nil, nil)

	var out strings.Builder
	if err := AppLayout(AppLayoutVModel{}).Render(ctx, &out); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := out.String()

	for _, script := range []string{"popover", "dialog", "selectbox", "chart", "sidebar", "toast"} {
		if !strings.Contains(html, "/assets/js/"+script+".min.js") {
			t.Errorf("every application page must ship %s.min.js", script)
		}
	}
}

func TestFilterSwitcherEntries(t *testing.T) {
	entries := []SwitcherEntry{
		{Label: "Espace personnel"},
		{Label: "ACME Corp", Slug: "acme"},
		{Label: "Zebra", Slug: "zeb"},
	}

	if got := FilterSwitcherEntries(entries, "  "); len(got) != 3 {
		t.Errorf("a blank query keeps everything, got %d entries", len(got))
	}
	if got := FilterSwitcherEntries(entries, "ACM"); len(got) != 1 || got[0].Label != "ACME Corp" {
		t.Errorf("matching on the label should be case-insensitive, got %v", got)
	}
	if got := FilterSwitcherEntries(entries, "zeb"); len(got) != 1 || got[0].Label != "Zebra" {
		t.Errorf("the slug should match too, got %v", got)
	}
	if got := FilterSwitcherEntries(entries, "nothing"); len(got) != 0 {
		t.Errorf("expected no match, got %v", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestInitials pins the three shapes of name a pastille is built from — display
// name, login and slug — because every avatar and every organisation square of
// the interface goes through this one function.
func TestInitials(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"ACME Corp", "AC"},
		{"Ville de Lyon", "VL"}, // the particle in the middle is skipped
		{"Sanofi R&D", "SR"},
		{"ville-lyon", "VL"},
		{"acme-corp", "AC"},
		{"a.leroy", "AL"},
		{"m.bernard@acme.example", "MB"},
		{"cmsassot", "CM"},
		{"X", "X"},
		{"", "?"},
		{"   ", "?"},
	} {
		if got := Initials(tc.name); got != tc.want {
			t.Errorf("Initials(%q): got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestSwitcherGroupLabel checks that a heading is drawn once per context, and
// that the console — a single row separated by a rule — gets none.
func TestSwitcherGroupLabel(t *testing.T) {
	entries := []SwitcherEntry{
		{Context: ContextPersonal},
		{Context: ContextOrg},
		{Context: ContextOrg},
		{Context: ContextPlatform},
	}
	want := []string{"Espace personnel", "Organisations · 2", "", ""}
	for i, w := range want {
		if got := switcherGroupLabel(entries, i); got != w {
			t.Errorf("entry %d: got %q, want %q", i, got, w)
		}
	}
}

// testTenantID is the tenant every fixture of this package belongs to.
// Tenancy is not what these tests exercise: they only need a stable, shared
// owner so the tenant-scoped unique keys behave like the pre-tenant ones.
const testTenantID = model.TenantID("test-tenant")
