package gorm

import (
	"regexp"
	"strings"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// rowAlias matches the alias r of the inventory expressions, and nothing else
// ending with an r.
var rowAlias = regexp.MustCompile(`\br\.`)

// lifecycleTable designates the scope of each row of a table: the tenant, the
// organization and the member it belongs to, as SQL expressions over the row
// aliased r. Expressions are trusted schema constants, never client input; an
// empty one means the table carries no such link. A tenant left empty is
// derived from the organization or the member.
type lifecycleTable struct {
	table, tenant, org, member string
	// unguarded tables belong to a scope, for the export and the purge, but
	// stay writable once it is frozen: the webhook worker finishes the leases
	// it holds, and preparation and claims leave frozen tenants out.
	unguarded bool
	// family is the provisioning family the table stores, if any: freezing a
	// scope holding its rows requires the write authority over it.
	family string
}

// lifecycleTables is the inventory of every table holding data of a tenant,
// an organization or a member. A new table carrying such a link must be added
// here, or the freeze would not protect it.
var lifecycleTables = []lifecycleTable{
	{table: "tenants", tenant: "r.id", family: model.FamilyTenant},
	{table: "domains", tenant: "r.tenant_id", family: model.FamilyTenantDomain},
	{table: "organizations", tenant: "r.tenant_id", org: "r.id", family: model.FamilyOrganization},
	// An application authenticates through a shadow user of its organization.
	{table: "users", tenant: "r.tenant_id", org: "CASE WHEN r.provider = 'application' THEN (SELECT org_id FROM applications WHERE id = r.subject) END", member: "r.id", family: model.FamilyMember},
	{table: "user_roles", member: "r.user_id"},
	{table: "user_preferences", member: "r.user_id"},
	{table: "personal_virtual_models", member: "r.user_id"},
	{table: "memberships", org: "r.org_id", member: "r.user_id", family: model.FamilyOrganizationMembership},
	{table: "membership_roles", org: "(SELECT org_id FROM memberships WHERE id = r.membership_id)", member: "(SELECT user_id FROM memberships WHERE id = r.membership_id)"},
	{table: "roles", org: "r.org_id", family: model.FamilyCustomRole},
	{table: "role_permissions", org: "(SELECT org_id FROM roles WHERE id = r.role_id)"},
	{table: "role_models", org: "(SELECT org_id FROM roles WHERE id = r.role_id)"},
	{table: "applications", org: "r.org_id", family: model.FamilyApplication},
	{table: "application_roles", org: "(SELECT org_id FROM applications WHERE id = r.application_id)"},
	{table: "auth_tokens", org: "r.org_id", member: "r.owner_id"},
	{table: "invite_tokens", org: "r.org_id"},
	{table: "providers", org: "r.org_id", family: model.FamilyProvider},
	{table: "llm_models", org: "r.org_id"},
	{table: "virtual_models", org: "r.org_id"},
	{table: "middlewares", org: "r.org_id"},
	{table: "event_settings", org: "r.org_id"},
	// A personal secret is scoped "~:<user id>" instead of an organization.
	{table: "plugin_node_secrets", org: "CASE WHEN r.org_id NOT LIKE '~:%' THEN r.org_id END", member: "CASE WHEN r.org_id LIKE '~:%' THEN substr(r.org_id, 3) END"},
	{table: "alerts", org: "r.org_id", member: "CASE WHEN r.scope = 'personal' THEN r.owner_id END", family: model.FamilyAlert},
	{table: "alert_incidents", org: "r.org_id"},
	{table: "quota", org: "CASE WHEN r.scope = 'org' THEN r.scope_id WHEN r.scope = 'application' THEN (SELECT org_id FROM applications WHERE id = r.scope_id) END", member: "CASE WHEN r.scope = 'user' THEN r.scope_id END", family: model.FamilyQuota},
	{table: "quota_usages", org: "r.org_id", member: "CASE WHEN r.scope = 'user' THEN r.scope_id END"},
	{table: "usage_records", org: "r.org_id", member: "r.user_id"},
	{table: "events", org: "r.org_id", member: "r.user_id"},
	{table: "oidc_sessions", tenant: "r.tenant_id"},
	{table: "webhook_subscriptions", tenant: "r.tenant_id", family: model.FamilySubscription},
	{table: "webhook_deliveries", tenant: "r.tenant_id", unguarded: true},
}

// expression returns expr over the row alias, NULL when the table has no
// such link.
func (t lifecycleTable) expression(expr, alias string) string {
	if expr == "" {
		return "NULL"
	}
	return "NULLIF(" + rowAlias.ReplaceAllString(expr, alias+".") + ", '')"
}

// tenantExpression derives the tenant of a row from its organization or its
// member when it carries none.
func (t lifecycleTable) tenantExpression(alias string) string {
	return "COALESCE(" + t.expression(t.tenant, alias) +
		", (SELECT tenant_id FROM organizations WHERE id = " + t.expression(t.org, alias) + ")" +
		", (SELECT tenant_id FROM users WHERE id = " + t.expression(t.member, alias) + "))"
}

// frozenPredicate matches a recorded deletion of the scope of the row.
func (t lifecycleTable) frozenPredicate(alias string) string {
	tests := []string{"(d.family = 'tenant' AND d.resource_id = " + t.tenantExpression(alias) + ")"}
	if t.org != "" {
		tests = append(tests, "(d.family = 'organization' AND d.resource_id = "+t.expression(t.org, alias)+")")
	}
	if t.member != "" {
		tests = append(tests, "(d.family = 'member' AND d.resource_id = "+t.expression(t.member, alias)+")")
	}
	return "EXISTS (SELECT 1 FROM resource_deletions d WHERE " + strings.Join(tests, " OR ") + ")"
}

// lifecycleTableOf returns the inventory entry of table. The names are
// schema constants: an unknown one is a programming error, which would
// otherwise turn a scope filter into a no-op.
func lifecycleTableOf(table string) lifecycleTable {
	for _, t := range lifecycleTables {
		if t.table == table {
			return t
		}
	}
	panic("gorm: table " + table + " is missing from the lifecycle inventory")
}

// notFrozen matches the rows of table, aliased alias, outside every frozen
// scope: background workers leave the frozen scopes alone instead of failing
// on their guards.
func notFrozen(table, alias string) string {
	return "NOT " + lifecycleTableOf(table).frozenPredicate(alias)
}
