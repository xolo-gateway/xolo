package gorm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"gorm.io/gorm"
)

// businessSnapshot completes the snapshot of a business resource with what
// its row alone does not hold: its parents, its assignments and decoded
// configurations. A provider key never leaves the database: the snapshot
// keeps a fingerprint of its ciphertext, so that the audit records a
// rotation without the secret.
func businessSnapshot(db *gorm.DB, key mutationKey, row map[string]any) error {
	switch key.kind {
	case "application":
		roles := []string{}
		if err := db.Table("application_roles").Where("application_id = ?", key.id).Order("role_id").Pluck("role_id", &roles).Error; err != nil {
			return err
		}
		row["role_ids"] = roles
	case "provider":
		fingerprint := ""
		if ciphertext, _ := row["api_key"].(string); ciphertext != "" {
			sum := sha256.Sum256([]byte(ciphertext))
			fingerprint = hex.EncodeToString(sum[:8])
		}
		delete(row, "api_key")
		row["api_key_fingerprint"] = fingerprint
		for _, column := range []string{"subscription_plan", "retry_config", "rate_limit_config", "request_headers"} {
			raw, _ := row[column].(string)
			if raw == "" || raw == "null" {
				row[column] = nil
				continue
			}
			var decoded any
			if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
				return err
			}
			row[column] = decoded
		}
	case "team_settings":
		// Its tenant is resolved through the organization of the team, as
		// teamMemberSnapshot does for a team member.
		grants := []map[string]any{}
		var grantRows []TeamModelGrant
		if err := db.Table("team_model_grants").Where("team_id = ?", key.id).Order("model_id, model_kind").Find(&grantRows).Error; err != nil {
			return err
		}
		for _, g := range grantRows {
			grants = append(grants, map[string]any{"model_id": g.ModelID, "kind": g.ModelKind})
		}
		row["model_grants"] = grants
		middlewares := []string{}
		if err := db.Table("team_middlewares").Where("team_id = ?", key.id).Order("middleware_id").Pluck("middleware_id", &middlewares).Error; err != nil {
			return err
		}
		row["middleware_ids"] = middlewares
		var memberRows []struct {
			MembershipID string
			UserID       string
		}
		if err := db.Table("team_members").Select("team_members.membership_id, memberships.user_id").
			Joins("JOIN memberships ON memberships.id = team_members.membership_id").
			Where("team_members.team_id = ?", key.id).Order("memberships.user_id").Scan(&memberRows).Error; err != nil {
			return err
		}
		// Members without a team role are left out: a settings PUT lists
		// only the members holding roles.
		members := []map[string]any{}
		for _, m := range memberRows {
			roles := []string{}
			if err := db.Table("team_member_roles").Where("team_id = ? AND membership_id = ?", key.id, m.MembershipID).Order("role_id").Pluck("role_id", &roles).Error; err != nil {
				return err
			}
			if len(roles) > 0 {
				members = append(members, map[string]any{"user_id": m.UserID, "role_ids": roles})
			}
		}
		row["member_roles"] = members
	case "alert":
		// Rows created before personal alerts carry no scope.
		if scope, _ := row["scope"].(string); scope == "" {
			row["scope"] = string(model.AlertScopeOrg)
		}
	case "quota":
		// A quota has no parent column: its scope designates it.
		scopeID, _ := row["scope_id"].(string)
		var orgID, tenantID string
		switch scope, _ := row["scope"].(string); model.QuotaScope(scope) {
		case model.QuotaScopeOrg:
			orgID = scopeID
		case model.QuotaScopeApplication:
			if err := db.Table("applications").Where("id = ?", scopeID).Pluck("org_id", &orgID).Error; err != nil {
				return err
			}
		case model.QuotaScopeUser:
			if err := db.Table("users").Where("id = ?", scopeID).Pluck("tenant_id", &tenantID).Error; err != nil {
				return err
			}
		case model.QuotaScopeTeam:
			if err := db.Table("teams").Where("id = ?", scopeID).Pluck("org_id", &orgID).Error; err != nil {
				return err
			}
		case model.QuotaScopeTeamMember:
			if teamID, _, ok := model.ParseTeamMemberScopeID(scopeID); ok {
				if err := db.Table("teams").Where("id = ?", string(teamID)).Pluck("org_id", &orgID).Error; err != nil {
					return err
				}
			}
		}
		if orgID != "" {
			if err := db.Table("organizations").Where("id = ?", orgID).Pluck("tenant_id", &tenantID).Error; err != nil {
				return err
			}
		}
		row["tenant_id"] = tenantID
	}
	return nil
}

// businessFamilyOf designates the family of a business resource snapshot.
// Builtin roles are created with their organization and never change: they
// are not part of the contract.
func businessFamilyOf(key mutationKey, row snapshotRow) (string, bool) {
	switch key.kind {
	case "role":
		if builtin, _ := row["builtin"].(bool); builtin {
			return "", false
		}
		return model.FamilyCustomRole, true
	case "application":
		return model.FamilyApplication, true
	case "quota":
		return model.FamilyQuota, true
	case "alert":
		return model.FamilyAlert, true
	case "provider":
		return model.FamilyProvider, true
	case "team_settings":
		return model.FamilyTeamSettings, true
	}
	return "", false
}

// businessRepresentationFields lists the fields of each business family, as
// named by the model settings: a PUT body and its projection are comparable.
var businessRepresentationFields = map[string][]string{
	model.FamilyCustomRole:   {"name", "description", "permissions"},
	model.FamilyApplication:  {"name", "description", "active", "role_ids", "team_id"},
	model.FamilyQuota:        {"scope", "scope_id", "currency", "daily_budget", "monthly_budget", "yearly_budget", "cascade"},
	model.FamilyTeamSettings: {"description", "share_quota_equally", "model_grants", "middleware_ids", "member_roles"},
	model.FamilyAlert:        {"name", "description", "scope", "owner_id", "query", "aggregation", "window_seconds", "comparator", "threshold", "for_seconds", "enabled"},
	model.FamilyProvider:     {"name", "type", "base_url", "active", "currency", "cloud_tier", "billing_mode", "subscription_plan", "retry_config", "rate_limit_config", "request_headers"},
}

func businessRepresentation(family string, row snapshotRow) map[string]any {
	rep := map[string]any{}
	for _, field := range businessRepresentationFields[family] {
		rep[field] = row[field]
	}
	// Sorted here rather than by the database, whose collation varies: the
	// projection is the same on both backends.
	for _, field := range []string{"permissions", "role_ids"} {
		if values, ok := rep[field].([]any); ok {
			sorted := make([]string, 0, len(values))
			for _, value := range values {
				text, _ := value.(string)
				sorted = append(sorted, text)
			}
			slices.Sort(sorted)
			rep[field] = sorted
		}
	}
	if family == model.FamilyTeamSettings {
		rep["middleware_ids"] = sortedStrings(rep["middleware_ids"])
		grants, _ := rep["model_grants"].([]any)
		slices.SortFunc(grants, func(a, b any) int {
			ga, _ := a.(map[string]any)
			gb, _ := b.(map[string]any)
			return strings.Compare(fmt.Sprint(ga["model_id"], "\x00", ga["kind"]), fmt.Sprint(gb["model_id"], "\x00", gb["kind"]))
		})
		if grants == nil {
			grants = []any{}
		}
		rep["model_grants"] = grants
		members, _ := rep["member_roles"].([]any)
		for _, member := range members {
			if m, ok := member.(map[string]any); ok {
				m["role_ids"] = sortedStrings(m["role_ids"])
			}
		}
		slices.SortFunc(members, func(a, b any) int {
			ma, _ := a.(map[string]any)
			mb, _ := b.(map[string]any)
			return strings.Compare(fmt.Sprint(ma["user_id"]), fmt.Sprint(mb["user_id"]))
		})
		if members == nil {
			members = []any{}
		}
		rep["member_roles"] = members
	}
	if family == model.FamilyApplication {
		// Applications created before teams carry no team.
		if team, _ := rep["team_id"].(string); team == "" {
			rep["team_id"] = ""
		}
	}
	if family == model.FamilyQuota {
		// Quotas set before cascades existed are free.
		if cascade, _ := rep["cascade"].(string); cascade == "" {
			rep["cascade"] = string(model.QuotaCascadeFree)
		}
	}
	return rep
}

// sortedStrings sorts a decoded JSON array of strings, empty when absent.
func sortedStrings(value any) []string {
	values, _ := value.([]any)
	sorted := make([]string, 0, len(values))
	for _, v := range values {
		text, _ := v.(string)
		sorted = append(sorted, text)
	}
	slices.Sort(sorted)
	return sorted
}

// trackQuota designates the quota of a scope, if any.
func (r *mutationRecorder) trackQuota(scope model.QuotaScope, scopeID string) error {
	var ids []string
	if err := r.db.Table("quota").Where("scope = ? AND scope_id = ?", string(scope), scopeID).Order("id").Pluck("id", &ids).Error; err != nil {
		return err
	}
	for _, id := range ids {
		if err := r.track("quota", id); err != nil {
			return err
		}
	}
	return nil
}
