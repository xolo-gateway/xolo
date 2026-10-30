package gorm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
)

// MutationAudit has no foreign keys: removing a resource must preserve its history.
// IDs identify facts; they do not order commits.
type MutationAudit struct {
	ID         string `gorm:"primaryKey;autoIncrement:false"`
	CreatedAt  time.Time
	Actor      string `gorm:"type:text"`
	RequestID  string `gorm:"index"`
	TenantID   string `gorm:"index"`
	OrgID      string `gorm:"index"`
	Resource   string
	ResourceID string `gorm:"index"`
	Before     string `gorm:"type:text"`
	After      string `gorm:"type:text"`
}

type mutationKey struct{ kind, id string }

var resourceTables = map[string]string{
	"tenant": "tenants", "organization": "organizations", "user": "users", "membership": "memberships", "role": "roles", "domain": "domains",
	// The quota table keeps the singular name GORM gave it.
	"application": "applications", "quota": "quota", "alert": "alerts", "provider": "providers", "llm_model": "llm_models",
	"team": "teams", "team_settings": "teams", "team_member": "team_members",
	"virtual_model": "virtual_models", "middleware": "middlewares", "personal_virtual_model": "personal_virtual_models",
}

// resourceKeys names the key column of the resources not keyed by id.
var resourceKeys = map[string]string{"domain": "hostname"}

func resourceKey(kind string) string {
	if column, ok := resourceKeys[kind]; ok {
		return column
	}
	return "id"
}

// Explicit allowlists exclude secrets and technical timestamps, including future columns.
var mutationColumns = map[string]string{
	"tenant":        "id, slug, name, description, active",
	"organization":  "id, tenant_id, slug, name, description, active, currency, share_quota_equally",
	"user":          "id, tenant_id, provider, subject, identity_issuer, identity_subject, email, display_name, active, tenant_role",
	"membership":    "id, org_id, user_id, status",
	"role":          "id, org_id, name, description, builtin, builtin_kind",
	"domain":        "hostname, tenant_id, status",
	"application":   "id, org_id, name, description, active, team_id",
	"quota":         "id, scope, scope_id, currency, daily_budget, monthly_budget, yearly_budget, cascade",
	"team":          "id, org_id, slug, name, status",
	"team_settings": "id, org_id, description, share_quota_equally",
	// The evaluation state (state, pending_since, last_evaluated_at) is left
	// out: the evaluator rewrites it on every tick, outside the recorder.
	"alert": "id, org_id, owner_id, scope, name, description, query, aggregation, window_seconds, comparator, threshold, for_seconds, enabled",
	// api_key is replaced by its fingerprint before leaving the snapshot.
	"provider": "id, org_id, name, type, base_url, api_key, active, currency, cloud_tier, billing_mode, subscription_plan, retry_config, rate_limit_config, request_headers",
	// Only what designates a model: its routing (real model, base URL, extra
	// body), pricing and catalog are configuration the API does not expose.
	"llm_model": "id, org_id, provider_id, proxy_name, kind, enabled",
	// The secrets of their plugin nodes are replaced by a fingerprint.
	"virtual_model": "id, org_id, name, description, graph_json, overrides_json",
	"middleware":    "id, org_id, name, description, enabled, priority, applies_to_all, targets_json, graph_json",
	// The secrets of its plugin nodes, scoped to the member, are left out.
	"personal_virtual_model": "id, user_id, name, description, team_id, graph_json, overrides_json",
}

// mutationRecorder keeps the state of every resource a transaction touches,
// as it was before the first change. Provisioning audits the differences;
// every writer publishes the projections they change.
type mutationRecorder struct {
	db     *gorm.DB
	before map[mutationKey][]byte
	// ownership is checked on every projection the transaction changes.
	ownership model.OwnershipPolicy
}

func newMutationRecorder(db *gorm.DB, ownership model.OwnershipPolicy) *mutationRecorder {
	return &mutationRecorder{db: db, before: make(map[mutationKey][]byte), ownership: ownership}
}

// mutationChange is a tracked resource whose snapshot changed.
type mutationChange struct {
	key           mutationKey
	before, after []byte
}

func (r *mutationRecorder) track(kind, id string) error {
	key := mutationKey{kind, id}
	if _, exists := r.before[key]; exists {
		return nil
	}
	before, err := mutationSnapshot(r.db, key)
	if err != nil {
		return err
	}
	r.before[key] = before
	return nil
}

// changes returns the tracked resources whose state changed, in key order.
func (r *mutationRecorder) changes() ([]mutationChange, error) {
	keys := make([]mutationKey, 0, len(r.before))
	for key := range r.before {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].kind != keys[j].kind {
			return keys[i].kind < keys[j].kind
		}
		return keys[i].id < keys[j].id
	})
	changes := make([]mutationChange, 0, len(keys))
	for _, key := range keys {
		before := r.before[key]
		after, err := mutationSnapshot(r.db, key)
		if err != nil {
			return nil, err
		}
		if bytes.Equal(before, after) {
			continue
		}
		changes = append(changes, mutationChange{key: key, before: before, after: after})
	}
	return changes, nil
}

// publish publishes the projections of changes, then takes the current state
// as the new reference, so a later flush in the same transaction only sees
// what happened since.
func (r *mutationRecorder) publish(ctx context.Context, changes []mutationChange) error {
	keys := make([]mutationKey, 0, len(changes))
	for _, change := range changes {
		keys = append(keys, change.key)
	}
	if err := publishProjections(ctx, r.db, r.ownership, r.before, keys); err != nil {
		return err
	}
	for _, change := range changes {
		r.before[change.key] = change.after
	}
	return nil
}

// flush publishes every pending change.
func (r *mutationRecorder) flush(ctx context.Context) error {
	changes, err := r.changes()
	if err != nil {
		return err
	}
	return r.publish(ctx, changes)
}

func mutationSnapshot(db *gorm.DB, key mutationKey) ([]byte, error) {
	if key.kind == "team_member" {
		return teamMemberSnapshot(db, key)
	}
	table, ok := resourceTables[key.kind]
	if !ok {
		return nil, fmt.Errorf("unknown mutation kind")
	}
	columns := mutationColumns[key.kind]
	if migratingSchema(db) {
		columns = existingColumns(db, table, columns)
	}
	var rows []map[string]any
	if err := db.Table(table).Select(columns).Where(resourceKey(key.kind)+" = ?", key.id).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []byte("null"), nil
	}
	row := rows[0]
	// Normalize driver values so byte/text representations cannot create facts.
	for k, v := range row {
		if b, ok := v.([]byte); ok {
			row[k] = string(b)
		}
	}
	for _, column := range []string{"active", "builtin", "share_quota_equally", "enabled", "applies_to_all"} {
		if value, ok := row[column]; ok {
			row[column] = fmt.Sprint(value) == "true" || fmt.Sprint(value) == "1"
		}
	}
	if err := businessSnapshot(db, key, row); err != nil {
		return nil, err
	}
	if key.kind == "membership" || key.kind == "role" || key.kind == "application" || key.kind == "alert" || key.kind == "provider" || key.kind == "team" || key.kind == "team_settings" ||
		key.kind == "virtual_model" || key.kind == "middleware" || key.kind == "llm_model" {
		var tenantID string
		if err := db.Table("organizations").Where("id = ?", row["org_id"]).Pluck("tenant_id", &tenantID).Error; err != nil {
			return nil, err
		}
		row["tenant_id"] = tenantID
	}
	if key.kind == "personal_virtual_model" {
		var tenantID string
		if err := db.Table("users").Where("id = ?", row["user_id"]).Pluck("tenant_id", &tenantID).Error; err != nil {
			return nil, err
		}
		row["tenant_id"] = tenantID
	}
	if key.kind == "membership" {
		roles := []string{}
		if err := db.Table("membership_roles").Where("membership_id = ?", key.id).Order("role_id").Pluck("role_id", &roles).Error; err != nil {
			return nil, err
		}
		row["roles"] = roles
		var kinds []string
		if err := db.Table("roles").Joins("JOIN membership_roles mr ON mr.role_id = roles.id").
			Where("mr.membership_id = ? AND roles.builtin_kind <> ''", key.id).Pluck("roles.builtin_kind", &kinds).Error; err != nil {
			return nil, err
		}
		row["common_role"] = string(model.CommonRoleOfKinds(kinds...))
	}
	if key.kind == "user" {
		roles := []string{}
		if err := db.Table("user_roles").Where("user_id = ?", key.id).Order("role").Pluck("role", &roles).Error; err != nil {
			return nil, err
		}
		row["platform_roles"] = roles
		locale, err := userLocale(db, key.id)
		if err != nil {
			return nil, err
		}
		// Absent rather than empty: the snapshots of members without one stay
		// those taken before the column existed.
		if locale != "" {
			row["locale"] = locale
		}
	}
	if key.kind == "role" {
		permissions := []string{}
		if err := db.Table("role_permissions").Where("role_id = ?", key.id).Order("code").Pluck("code", &permissions).Error; err != nil {
			return nil, err
		}
		row["permissions"] = permissions
	}
	return json.Marshal(row)
}

// teamMemberSnapshot reads a team member, keyed by "<team_id>:<membership_id>",
// with the user and the parents its projection hangs from. The team_settings
// case of businessSnapshot walks the same team-to-tenant chain.
func teamMemberSnapshot(db *gorm.DB, key mutationKey) ([]byte, error) {
	teamID, membershipID, ok := strings.Cut(key.id, ":")
	if !ok {
		return nil, fmt.Errorf("invalid team member key")
	}
	var rows []map[string]any
	if err := db.Table("team_members").
		Select("team_members.team_id, team_members.membership_id, team_members.status, memberships.user_id, teams.org_id, organizations.tenant_id").
		Joins("JOIN memberships ON memberships.id = team_members.membership_id").
		Joins("JOIN teams ON teams.id = team_members.team_id").
		Joins("JOIN organizations ON organizations.id = teams.org_id").
		Where("team_members.team_id = ? AND team_members.membership_id = ?", teamID, membershipID).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []byte("null"), nil
	}
	row := rows[0]
	for k, v := range row {
		if b, ok := v.([]byte); ok {
			row[k] = string(b)
		}
	}
	return json.Marshal(row)
}

// userLocale reads the locale a user chose, kept with their preferences.
// A migration running before the column exists sees none.
func userLocale(db *gorm.DB, userID string) (string, error) {
	if migratingSchema(db) && !db.Migrator().HasColumn("user_preferences", "locale") {
		return "", nil
	}
	var locales []string
	if err := db.Table("user_preferences").Where("user_id = ?", userID).Limit(1).Pluck("locale", &locales).Error; err != nil {
		return "", err
	}
	if len(locales) == 0 {
		return "", nil
	}
	return locales[0], nil
}

type migratingSchemaKey struct{}

// withMigratingSchema marks db as running a migration: the schema may predate
// columns of mutationColumns, added by a later migration that publishes the
// projections again.
func withMigratingSchema(db *gorm.DB) *gorm.DB {
	return db.WithContext(context.WithValue(db.Statement.Context, migratingSchemaKey{}, true))
}

func migratingSchema(db *gorm.DB) bool {
	migrating, _ := db.Statement.Context.Value(migratingSchemaKey{}).(bool)
	return migrating
}

// existingColumns keeps the columns of the list present in table.
func existingColumns(db *gorm.DB, table, columns string) string {
	kept := []string{}
	for _, column := range strings.Split(columns, ",") {
		if column = strings.TrimSpace(column); db.Migrator().HasColumn(table, column) {
			kept = append(kept, column)
		}
	}
	return strings.Join(kept, ", ")
}

// flushMutations audits every pending change, then publishes its projections.
func (tx *provisioningTx) flushMutations(ctx context.Context) error {
	changes, err := tx.recorder.changes()
	if err != nil {
		return err
	}
	actor := model.ActorFromContext(ctx)
	actorJSON, err := json.Marshal(actor)
	if err != nil {
		return err
	}
	for _, change := range changes {
		key, before, after := change.key, change.before, change.after
		if err := tx.checkOwnerTransition(key, before, after); err != nil {
			return err
		}
		source := after
		if string(after) == "null" {
			source = before
		}
		var scope struct {
			TenantID string `json:"tenant_id"`
			OrgID    string `json:"org_id"`
		}
		if err := json.Unmarshal(source, &scope); err != nil {
			return err
		}
		if key.kind == "tenant" {
			scope.TenantID = key.id
		}
		if key.kind == "organization" {
			scope.OrgID = key.id
		}
		audit := MutationAudit{ID: uuid.NewString(), Actor: string(actorJSON), RequestID: actor.RequestID, TenantID: scope.TenantID, OrgID: scope.OrgID, Resource: key.kind, ResourceID: key.id, Before: string(before), After: string(after)}
		if err := tx.db.Create(&audit).Error; err != nil {
			return err
		}
	}
	return tx.recorder.publish(ctx, changes)
}

func (r *mutationRecorder) trackDependents(kind, id string) error {
	trackIDs := func(resource, column string) error {
		var ids []string
		key := resourceKey(resource)
		if err := r.db.Table(resourceTables[resource]).Where(column+" = ?", id).Order(key).Pluck(key, &ids).Error; err != nil {
			return err
		}
		for _, child := range ids {
			if err := r.track(resource, child); err != nil {
				return err
			}
			if err := r.trackDependents(resource, child); err != nil {
				return err
			}
		}
		return nil
	}
	switch kind {
	case "tenant":
		if err := trackIDs("organization", "tenant_id"); err != nil {
			return err
		}
		if err := trackIDs("domain", "tenant_id"); err != nil {
			return err
		}
		return trackIDs("user", "tenant_id")
	case "organization":
		if err := trackIDs("membership", "org_id"); err != nil {
			return err
		}
		if err := trackIDs("role", "org_id"); err != nil {
			return err
		}
		for _, resource := range []string{"application", "alert", "provider", "llm_model", "team", "virtual_model", "middleware"} {
			if err := trackIDs(resource, "org_id"); err != nil {
				return err
			}
		}
		return r.trackQuota(model.QuotaScopeOrg, id)
	case "membership":
		// Removing a membership removes it from its teams, and its budgets
		// inside them.
		var teams []string
		if err := r.db.Table("team_members").Where("membership_id = ?", id).Order("team_id").Pluck("team_id", &teams).Error; err != nil {
			return err
		}
		for _, team := range teams {
			if err := trackingTeamMember(model.TeamID(team), model.MembershipID(id))(r); err != nil {
				return err
			}
			if err := r.trackTeamMemberQuotas(team, id); err != nil {
				return err
			}
		}
	case "team":
		if err := r.track("team_settings", id); err != nil {
			return err
		}
		var members []string
		if err := r.db.Table("team_members").Where("team_id = ?", id).Order("membership_id").Pluck("membership_id", &members).Error; err != nil {
			return err
		}
		for _, member := range members {
			if err := r.track("team_member", teamMemberKey(model.TeamID(id), model.MembershipID(member))); err != nil {
				return err
			}
		}
		if err := r.trackQuota(model.QuotaScopeTeam, id); err != nil {
			return err
		}
		var ids []string
		if err := r.db.Table("quota").Where("scope = ? AND scope_id LIKE ?", string(model.QuotaScopeTeamMember), id+":%").Order("id").Pluck("id", &ids).Error; err != nil {
			return err
		}
		for _, quota := range ids {
			if err := r.track("quota", quota); err != nil {
				return err
			}
		}
	case "user":
		if err := trackIDs("membership", "user_id"); err != nil {
			return err
		}
		if err := trackIDs("alert", "owner_id"); err != nil {
			return err
		}
		if err := trackIDs("personal_virtual_model", "user_id"); err != nil {
			return err
		}
		return r.trackQuota(model.QuotaScopeUser, id)
	case "application":
		return r.trackQuota(model.QuotaScopeApplication, id)
	case "provider":
		// Its models are removed with it.
		return trackIDs("llm_model", "provider_id")
	case "role":
		var apps []string
		if err := r.db.Table("application_roles").Where("role_id = ?", id).Order("application_id").Pluck("application_id", &apps).Error; err != nil {
			return err
		}
		for _, app := range apps {
			if err := r.track("application", app); err != nil {
				return err
			}
		}
		var ids []string
		if err := r.db.Table("membership_roles").Where("role_id = ?", id).Order("membership_id").Pluck("membership_id", &ids).Error; err != nil {
			return err
		}
		for _, member := range ids {
			if err := r.track("membership", member); err != nil {
				return err
			}
		}
		var teams []string
		if err := r.db.Table("team_member_roles").Where("role_id = ?", id).Distinct("team_id").Order("team_id").Pluck("team_id", &teams).Error; err != nil {
			return err
		}
		for _, team := range teams {
			if err := r.track("team_settings", team); err != nil {
				return err
			}
		}
	}
	return nil
}

// trackTeamMemberQuotas designates the budget of a membership inside a team,
// if any.
func (r *mutationRecorder) trackTeamMemberQuotas(teamID, membershipID string) error {
	var userID string
	if err := r.db.Table("memberships").Where("id = ?", membershipID).Pluck("user_id", &userID).Error; err != nil {
		return err
	}
	if userID == "" {
		return nil
	}
	return r.trackQuota(model.QuotaScopeTeamMember, model.TeamMemberScopeID(model.TeamID(teamID), model.UserID(userID)))
}

// checkOwnerTransition refuses a change that removes the last active owner of
// a tenant or an organization still present after the transaction. Deleting
// the tenant or organization itself is not a transition.
func (tx *provisioningTx) checkOwnerTransition(key mutationKey, before, after []byte) error {
	type ownerState struct {
		TenantID   string `json:"tenant_id"`
		OrgID      string `json:"org_id"`
		Active     *bool  `json:"active"`
		Status     string `json:"status"`
		TenantRole string `json:"tenant_role"`
		CommonRole string `json:"common_role"`
	}
	var was, is ownerState
	if err := json.Unmarshal(before, &was); err != nil {
		return err
	}
	if string(after) != "null" {
		if err := json.Unmarshal(after, &is); err != nil {
			return err
		}
	}
	switch key.kind {
	case "user":
		owner := func(s ownerState) bool {
			return s.TenantRole == string(model.TenantRoleOwner) && s.Active != nil && *s.Active
		}
		if !owner(was) || owner(is) {
			return nil
		}
		var tenants, owners int64
		if err := tx.db.Table("tenants").Where("id = ?", was.TenantID).Count(&tenants).Error; err != nil {
			return err
		}
		if tenants == 0 {
			return nil
		}
		if err := tx.db.Table("users").Where("tenant_id = ? AND tenant_role = ? AND active = ?", was.TenantID, string(model.TenantRoleOwner), true).Count(&owners).Error; err != nil {
			return err
		}
		if owners == 0 {
			return port.ErrLastOwner
		}
	case "membership":
		owner := func(s ownerState) bool {
			return s.CommonRole == string(model.MembershipRoleOwner) && s.Status == string(model.StatusActive)
		}
		if !owner(was) || owner(is) {
			return nil
		}
		var orgs, owners int64
		if err := tx.db.Table("organizations").Where("id = ?", was.OrgID).Count(&orgs).Error; err != nil {
			return err
		}
		if orgs == 0 {
			return nil
		}
		if err := tx.db.Table("memberships").
			Joins("JOIN membership_roles mr ON mr.membership_id = memberships.id").
			Joins("JOIN roles ON roles.id = mr.role_id").
			Where("memberships.org_id = ? AND memberships.status = ? AND roles.builtin_kind = ?", was.OrgID, string(model.StatusActive), model.BuiltinKindOwner).
			Count(&owners).Error; err != nil {
			return err
		}
		if owners == 0 {
			return port.ErrLastOwner
		}
	}
	return nil
}
