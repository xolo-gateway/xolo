package gorm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
		for _, column := range []string{"subscription_plan", "retry_config", "rate_limit_config"} {
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
	}
	return "", false
}

// businessRepresentationFields lists the fields of each business family, as
// named by the model settings: a PUT body and its projection are comparable.
var businessRepresentationFields = map[string][]string{
	model.FamilyCustomRole:  {"name", "description", "permissions"},
	model.FamilyApplication: {"name", "description", "active", "role_ids"},
	model.FamilyQuota:       {"scope", "scope_id", "currency", "daily_budget", "monthly_budget", "yearly_budget"},
	model.FamilyAlert:       {"name", "description", "scope", "owner_id", "query", "aggregation", "window_seconds", "comparator", "threshold", "for_seconds", "enabled"},
	model.FamilyProvider:    {"name", "type", "base_url", "active", "currency", "cloud_tier", "billing_mode", "subscription_plan", "retry_config", "rate_limit_config"},
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
	if family == model.FamilyCustomRole {
		grants := []model.ModelGrantSettings{}
		if rows, ok := row["grants"].([]any); ok {
			for _, value := range rows {
				grant, _ := value.(map[string]any)
				modelID, _ := grant["model_id"].(string)
				kind, _ := grant["model_kind"].(string)
				grants = append(grants, model.ModelGrantSettings{ModelID: modelID, Kind: kind})
			}
		}
		slices.SortFunc(grants, func(a, b model.ModelGrantSettings) int {
			if c := strings.Compare(a.ModelID, b.ModelID); c != 0 {
				return c
			}
			return strings.Compare(a.Kind, b.Kind)
		})
		rep["model_grants"] = grants
	}
	return rep
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
