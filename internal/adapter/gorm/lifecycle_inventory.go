package gorm

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// One explicit inventory drives frozen-row guards, export and purge. Expressions
// are trusted schema constants, never client input. The order is child first.
type lifecycleTable struct{ table, tenant, org, member string }

func lifecycleTables(db *gorm.DB) []lifecycleTable {
	eventMember := "json_extract(r.attributes, '$.member_user_id')"
	eventActor := "json_extract(r.attributes, '$.actor_id')"
	eventEmail := "json_extract(r.attributes, '$.email')"
	if isPostgres(db) {
		eventMember = "(r.attributes::jsonb ->> 'member_user_id')"
		eventActor = "(r.attributes::jsonb ->> 'actor_id')"
		eventEmail = "(r.attributes::jsonb ->> 'email')"
	}
	org := func(table, expr string) string { return "(SELECT org_id FROM " + table + " WHERE id = " + expr + ")" }
	return []lifecycleTable{
		{"leaf_versions", "r.tenant_id", "r.organization_id", "CASE WHEN r.family = 'organization_membership' THEN r.key ELSE '' END"},
		{"membership_roles", "", org("memberships", "r.membership_id"), "(SELECT user_id FROM memberships WHERE id = r.membership_id)"},
		{"application_roles", "", org("applications", "r.application_id"), ""},
		{"role_permissions", "", org("roles", "r.role_id"), ""},
		{"role_models", "", org("roles", "r.role_id"), ""},
		{"auth_tokens", "", "r.org_id", "r.owner_id"},
		{"user_roles", "", "", "r.user_id"},
		{"user_preferences", "", "", "r.user_id"},
		{"personal_virtual_models", "", "", "r.user_id"},
		{"plugin_node_secrets", "", "r.org_id", "CASE WHEN r.org_id LIKE '~:%' THEN substr(r.org_id, 3) ELSE '' END"},
		{"alert_incidents", "", "r.org_id", "(SELECT owner_id FROM alerts WHERE id = r.alert_id AND scope = 'personal')"},
		{"alerts", "", "r.org_id", "CASE WHEN r.scope = 'personal' THEN r.owner_id ELSE '' END"},
		{"invite_tokens", "", "r.org_id", "r.created_by_user_id"},
		{"usage_records", "", "r.org_id", "r.user_id"},
		{"events", "", "r.org_id", "CASE WHEN d.family = 'member' AND (" + eventActor + " = d.resource_id OR EXISTS (SELECT 1 FROM users eu WHERE eu.id = d.resource_id AND LOWER(eu.email) = LOWER(" + eventEmail + "))) AND EXISTS (SELECT 1 FROM organizations eo WHERE eo.id = r.org_id AND eo.tenant_id = d.tenant_id) THEN d.resource_id ELSE COALESCE(NULLIF(r.user_id, ''), " + eventMember + ", '') END"},
		{"quota_usages", "", "r.org_id", "CASE WHEN r.scope = 'user' THEN r.scope_id ELSE '' END"},
		{"quota", "", "CASE WHEN r.scope = 'org' THEN r.scope_id WHEN r.scope = 'application' THEN (SELECT org_id FROM applications WHERE id = r.scope_id) ELSE '' END", "CASE WHEN r.scope = 'user' THEN r.scope_id ELSE '' END"},
		{"event_settings", "", "r.org_id", ""},
		{"virtual_models", "", "r.org_id", ""},
		{"middlewares", "", "r.org_id", ""},
		{"llm_models", "", "r.org_id", ""},
		{"providers", "", "r.org_id", ""},
		{"memberships", "", "r.org_id", "r.user_id"},
		{"roles", "", "r.org_id", ""},
		{"users", "r.tenant_id", "CASE WHEN r.provider = 'application' THEN (SELECT org_id FROM applications WHERE id = r.subject) ELSE '' END", "r.id"},
		{"applications", "", "r.org_id", ""},
		{"domains", "r.tenant_id", "", ""},
		{"organizations", "r.tenant_id", "r.id", ""},
		{"tenants", "r.id", "", ""},
	}
}
func (t lifecycleTable) predicate(alias string) string {
	var tests []string
	if t.tenant != "" {
		tests = append(tests, "(d.family = 'tenant' AND d.resource_id = "+t.tenant+")")
	}
	if t.org != "" {
		tests = append(tests, "(d.family = 'organization' AND d.resource_id = "+t.org+")", "(d.family = 'tenant' AND EXISTS (SELECT 1 FROM organizations lo WHERE lo.id = "+t.org+" AND lo.tenant_id = d.resource_id))")
	}
	if t.member != "" {
		tests = append(tests, "(d.family = 'organization' AND EXISTS (SELECT 1 FROM users lu JOIN applications la ON la.id = lu.subject WHERE lu.provider = 'application' AND lu.id = "+t.member+" AND la.org_id = d.resource_id))")
		tests = append(tests, "(d.family = 'member' AND d.resource_id = "+t.member+")", "(d.family = 'tenant' AND EXISTS (SELECT 1 FROM users lu WHERE lu.id = "+t.member+" AND lu.tenant_id = d.resource_id))")
	}
	// Keep indirect parent references protected even after their owning row has
	// been physically removed. Purge records minimal tombstones for these IDs.
	refs := map[string][][2]string{
		"application_roles": {{"application", "r.application_id"}, {"custom_role", "r.role_id"}},
		"membership_roles":  {{"membership", "r.membership_id"}, {"custom_role", "r.role_id"}},
		"role_permissions":  {{"custom_role", "r.role_id"}},
		"role_models":       {{"custom_role", "r.role_id"}},
		"alert_incidents":   {{"alert", "r.alert_id"}},
		"auth_tokens":       {{"application", "r.application_id"}},
		"quota":             {{"application", "CASE WHEN r.scope = 'application' THEN r.scope_id ELSE '' END"}},
	}
	for _, ref := range refs[t.table] {
		tests = append(tests, "(d.family = '"+ref[0]+"' AND d.resource_id = "+ref[1]+")")
	}
	if t.table == "invite_tokens" {
		tests = append(tests, "(d.family = 'member' AND EXISTS (SELECT 1 FROM users lu JOIN organizations lo ON lo.tenant_id = lu.tenant_id WHERE lu.id = d.resource_id AND lo.id = r.org_id AND LOWER(r.invitee_email) = LOWER(lu.email)))")
	}
	return strings.ReplaceAll("("+strings.Join(tests, " OR ")+")", "r.", alias+".")
}
func lifecycleQuery(db *gorm.DB, t lifecycleTable, d ResourceDeletion) *gorm.DB {
	return db.Table(t.table).Where("EXISTS (SELECT 1 FROM resource_deletions d WHERE d.family = ? AND d.resource_id = ? AND "+t.predicate(t.table)+")", d.Family, d.ResourceID)
}

// Database guards also protect workers and direct adapter writes. PostgreSQL
// locks the publication clock BEFORE STATEMENT (before any row lock); SQLite
// already owns its database writer lock. The bypass is changed and reset inside
// the same clock-locked transaction, so no other connection can observe it.
func installLifecycleGuards(db *gorm.DB) error {
	if isPostgres(db) {
		if err := db.Exec(`CREATE OR REPLACE FUNCTION xolo_lifecycle_lock() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE publication_clocks SET sequence = sequence WHERE id = 1; RETURN NULL; END $$`).Error; err != nil {
			return err
		}
	}
	for _, t := range lifecycleTables(db) {
		if isPostgres(db) {
			name := "xolo_lifecycle_" + t.table
			if err := db.Exec("CREATE OR REPLACE FUNCTION " + name + `() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
    IF (SELECT bypass FROM lifecycle_controls WHERE id = 1) = 0 THEN
     IF TG_OP <> 'INSERT' THEN IF EXISTS (SELECT 1 FROM resource_deletions d WHERE ` + t.predicate("OLD") + `) THEN RAISE EXCEPTION 'xolo_resource_deleted'; END IF; END IF;
     IF TG_OP <> 'DELETE' THEN IF EXISTS (SELECT 1 FROM resource_deletions d WHERE ` + t.predicate("NEW") + `) THEN RAISE EXCEPTION 'xolo_resource_deleted'; END IF; END IF;
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF;
   END $$`).Error; err != nil {
				return err
			}
			for _, statement := range []string{
				"DROP TRIGGER IF EXISTS xolo_lifecycle_lock ON " + t.table,
				"CREATE TRIGGER xolo_lifecycle_lock BEFORE INSERT OR UPDATE OR DELETE ON " + t.table + " FOR EACH STATEMENT EXECUTE FUNCTION xolo_lifecycle_lock()",
				"DROP TRIGGER IF EXISTS xolo_lifecycle_guard ON " + t.table,
				"CREATE TRIGGER xolo_lifecycle_guard BEFORE INSERT OR UPDATE OR DELETE ON " + t.table + " FOR EACH ROW EXECUTE FUNCTION " + name + "()",
			} {
				if err := db.Exec(statement).Error; err != nil {
					return err
				}
			}
		} else {
			for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
				var predicates []string
				if op != "INSERT" {
					predicates = append(predicates, "EXISTS (SELECT 1 FROM resource_deletions d WHERE "+t.predicate("OLD")+")")
				}
				if op != "DELETE" {
					predicates = append(predicates, "EXISTS (SELECT 1 FROM resource_deletions d WHERE "+t.predicate("NEW")+")")
				}
				stmt := fmt.Sprintf("CREATE TRIGGER IF NOT EXISTS xolo_lifecycle_%s_%s BEFORE %s ON %s WHEN (SELECT bypass FROM lifecycle_controls WHERE id = 1) = 0 AND (%s) BEGIN SELECT RAISE(ABORT, 'xolo_resource_deleted'); END", t.table, strings.ToLower(op), op, t.table, strings.Join(predicates, " OR "))
				if err := db.Exec(stmt).Error; err != nil {
					return err
				}
			}
		}
	}
	return nil
}
