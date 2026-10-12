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
	// key lists the primary key columns, id when empty: the export orders
	// the rows by it, and the purge removes them through it.
	key []string
	// secret lists the columns the export leaves out.
	secret []string
	// unexported tables are purged but not exported: they hold technical
	// state, or copies of what the export already holds.
	unexported bool
}

// lifecycleTables is the inventory of every table holding data of a tenant,
// an organization or a member. A new table carrying such a link must be added
// here, or the freeze would not protect it.
var lifecycleTables = []lifecycleTable{
	{table: "tenants", tenant: "r.id", family: model.FamilyTenant},
	{table: "domains", tenant: "r.tenant_id", family: model.FamilyTenantDomain, key: []string{"hostname"}},
	{table: "organizations", tenant: "r.tenant_id", org: "r.id", family: model.FamilyOrganization},
	// An application authenticates through a shadow user of its organization.
	{table: "users", tenant: "r.tenant_id", org: "CASE WHEN r.provider = 'application' THEN (SELECT org_id FROM applications WHERE id = r.subject) END", member: "r.id", family: model.FamilyMember},
	{table: "user_roles", member: "r.user_id"},
	{table: "user_preferences", member: "r.user_id"},
	{table: "personal_virtual_models", member: "r.user_id"},
	{table: "memberships", org: "r.org_id", member: "r.user_id", family: model.FamilyOrganizationMembership},
	{table: "membership_roles", org: "(SELECT org_id FROM memberships WHERE id = r.membership_id)", member: "(SELECT user_id FROM memberships WHERE id = r.membership_id)", key: []string{"membership_id", "role_id"}},
	{table: "roles", org: "r.org_id", family: model.FamilyCustomRole},
	{table: "role_permissions", org: "(SELECT org_id FROM roles WHERE id = r.role_id)"},
	{table: "teams", org: "r.org_id", family: model.FamilyTeam},
	{table: "team_model_grants", org: "(SELECT org_id FROM teams WHERE id = r.team_id)", key: []string{"team_id", "model_id", "model_kind"}},
	{table: "team_middlewares", org: "(SELECT org_id FROM teams WHERE id = r.team_id)", key: []string{"team_id", "middleware_id"}},
	{table: "team_members", org: "(SELECT org_id FROM teams WHERE id = r.team_id)", member: "(SELECT user_id FROM memberships WHERE id = r.membership_id)", key: []string{"team_id", "membership_id"}},
	{table: "team_member_roles", org: "(SELECT org_id FROM teams WHERE id = r.team_id)", member: "(SELECT user_id FROM memberships WHERE id = r.membership_id)", key: []string{"team_id", "membership_id", "role_id"}},
	{table: "applications", org: "r.org_id", family: model.FamilyApplication},
	{table: "application_roles", org: "(SELECT org_id FROM applications WHERE id = r.application_id)", key: []string{"application_id", "role_id"}},
	{table: "auth_tokens", org: "r.org_id", member: "r.owner_id", secret: []string{"value"}},
	{table: "invite_tokens", org: "r.org_id"},
	{table: "providers", org: "r.org_id", family: model.FamilyProvider, secret: []string{"api_key"}},
	{table: "llm_models", org: "r.org_id"},
	{table: "virtual_models", org: "r.org_id"},
	{table: "middlewares", org: "r.org_id"},
	{table: "event_settings", org: "r.org_id", key: []string{"org_id"}},
	// A personal secret is scoped "~:<user id>" instead of an organization.
	{table: "plugin_node_secrets", org: "CASE WHEN r.org_id NOT LIKE '~:%' THEN r.org_id END", member: "CASE WHEN r.org_id LIKE '~:%' THEN substr(r.org_id, 3) END", secret: []string{"value_encrypted"}},
	{table: "alerts", org: "r.org_id", member: "CASE WHEN r.scope = 'personal' THEN r.owner_id END", family: model.FamilyAlert},
	{table: "alert_incidents", org: "r.org_id"},
	// A team member budget is scoped "<team id>:<user id>". The LIKE patterns
	// below match exactly only because no team or user id contains ":", "%" or
	// "_" (they are xids or UUIDs): the ":" they anchor on is the separator. An
	// id format allowing these characters must change them (see #143).
	{table: "quota", org: "CASE WHEN r.scope = 'org' THEN r.scope_id WHEN r.scope = 'application' THEN (SELECT org_id FROM applications WHERE id = r.scope_id)" +
		" WHEN r.scope = 'team' THEN (SELECT org_id FROM teams WHERE id = r.scope_id)" +
		" WHEN r.scope = 'team_member' THEN (SELECT org_id FROM teams WHERE r.scope_id LIKE teams.id || ':%') END",
		member: "CASE WHEN r.scope = 'user' THEN r.scope_id WHEN r.scope = 'team_member' THEN (SELECT users.id FROM users WHERE r.scope_id LIKE '%:' || users.id) END", family: model.FamilyQuota},
	{table: "quota_usages", org: "r.org_id", member: "CASE WHEN r.scope = 'user' THEN r.scope_id WHEN r.scope = 'team_member' THEN (SELECT users.id FROM users WHERE r.scope_id LIKE '%:' || users.id) END", key: []string{"scope", "scope_id", "org_id", "currency", "day"}},
	{table: "usage_records", org: "r.org_id", member: "r.user_id"},
	{table: "events", org: "r.org_id", member: "r.user_id"},
	{table: "oidc_sessions", tenant: "r.tenant_id", unexported: true},
	{table: "webhook_subscriptions", tenant: "r.tenant_id", family: model.FamilySubscription, secret: []string{"encrypted_secrets"}},
	{table: "webhook_deliveries", tenant: "r.tenant_id", unguarded: true, unexported: true},
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

// frozenPredicate matches a recorded deletion of the scope of the row. A
// deletion being purged is skipped: only the purge transaction sees it so.
func (t lifecycleTable) frozenPredicate(alias string) string {
	tests := []string{"(d.family = 'tenant' AND d.resource_id = " + t.tenantExpression(alias) + ")"}
	if t.org != "" {
		tests = append(tests, "(d.family = 'organization' AND d.resource_id = "+t.expression(t.org, alias)+")")
	}
	if t.member != "" {
		tests = append(tests, "(d.family = 'member' AND d.resource_id = "+t.expression(t.member, alias)+")")
	}
	return "EXISTS (SELECT 1 FROM resource_deletions d WHERE NOT d.purging AND (" + strings.Join(tests, " OR ") + "))"
}

// keyColumns returns the primary key columns of the table.
func (t lifecycleTable) keyColumns() []string {
	if len(t.key) == 0 {
		return []string{"id"}
	}
	return t.key
}

// simpleColumn matches an inventory expression designating a column of the
// row itself, which an index may serve.
var simpleColumn = regexp.MustCompile(`^r\.[a-z_]+$`)

// scopePredicate selects the rows of the table in the scope of a deletion,
// over the row aliased r. Unlike the guards, it compares the columns
// themselves whenever it can, so that the indexes serve the export and the
// purge of large tables.
func (t lifecycleTable) scopePredicate(d ResourceDeletion) (string, []any) {
	column := func(expr string) string {
		if simpleColumn.MatchString(expr) {
			return expr
		}
		return t.expression(expr, "r")
	}
	var predicate string
	var args []any
	switch d.Family {
	case model.FamilyTenant:
		var tests []string
		if t.tenant != "" {
			tests = append(tests, column(t.tenant)+" = ?")
			args = append(args, d.ResourceID)
		}
		if t.tenant == "" && t.org != "" {
			tests = append(tests, column(t.org)+" IN (SELECT id FROM organizations WHERE tenant_id = ?)")
			args = append(args, d.ResourceID)
		}
		if t.tenant == "" && t.member != "" {
			tests = append(tests, column(t.member)+" IN (SELECT id FROM users WHERE tenant_id = ?)")
			args = append(args, d.ResourceID)
		}
		predicate = "(" + strings.Join(tests, " OR ") + ")"
	case model.FamilyOrganization:
		if t.org == "" {
			return "1 = 0", nil
		}
		predicate, args = column(t.org)+" = ?", []any{d.ResourceID}
	default:
		if t.member == "" {
			return "1 = 0", nil
		}
		predicate, args = column(t.member)+" = ?", []any{d.ResourceID}
	}
	return predicate, args
}

// purgeOrder lists the inventory tables in the order the purge empties them:
// children before parents, and every table before those its inventory
// expressions read. Application shadow users, whose organization is read from
// their application, go before the applications.
var purgeOrder = []string{
	"webhook_deliveries", "webhook_subscriptions", "oidc_sessions",
	"events", "usage_records", "quota_usages", "alert_incidents", "alerts", "quota",
	"plugin_node_secrets", "event_settings",
	"team_member_roles", "team_members", "team_middlewares", "team_model_grants", "teams",
	"middlewares", "virtual_models", "role_permissions", "llm_models", "providers",
	"invite_tokens", "auth_tokens", "application_roles", "membership_roles",
	"memberships", "personal_virtual_models", "user_preferences", "user_roles",
	"users", "applications", "roles", "organizations", "domains", "tenants",
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
