package gorm

import (
	"encoding/json"
	"fmt"

	"github.com/rs/xid"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
)

func businessKind(f string) string {
	if f == "custom_role" {
		return "role"
	}
	return f
}
func businessFamily(k string) string {
	if k == "role" {
		return "custom_role"
	}
	return k
}
func businessKey(key string) bool {
	if _, e := model.ParseTenantID(key); e == nil {
		return true
	}
	id, e := xid.FromString(key)
	return e == nil && id.String() == key
}
func businessProjection(db *gorm.DB, key mutationKey, raw []byte) (*CommonRecord, error) {
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	str := func(k string) string { v, _ := row[k].(string); return v }
	boolean := func(k string) bool { return row[k] == true || row[k] == float64(1) }
	if key.kind == "role" && boolean("builtin") {
		return nil, nil
	}
	family := businessFamily(key.kind)
	oid, uid, tid := str("org_id"), "", str("tenant_id")
	if key.kind == "quota" {
		switch str("scope") {
		case "org":
			oid = str("scope_id")
		case "user":
			uid = str("scope_id")
		case "application":
			if err := db.Table("applications").Where("id = ?", str("scope_id")).Pluck("org_id", &oid).Error; err != nil {
				return nil, err
			}
		}
	}
	if oid != "" {
		if err := db.Table("organizations").Where("id = ?", oid).Pluck("tenant_id", &tid).Error; err != nil {
			return nil, err
		}
	}
	if uid != "" {
		if err := db.Table("users").Where("id = ?", uid).Pluck("tenant_id", &tid).Error; err != nil {
			return nil, err
		}
	}
	// Historical low-level fixtures can be unscoped; they are not discoverable.
	if tid == "" {
		return nil, nil
	}
	ref := model.CommonKey{TenantID: tid, OrganizationID: oid, MemberID: uid, ResourceID: key.id}
	rec := &CommonRecord{Family: family, TenantID: tid, OrganizationID: oid, Key: key.id}
	if family == "quota" {
		rec.OrganizationID = ""
	}
	fields := map[string][]string{
		"role":        {"name", "description", "permissions"},
		"application": {"name", "description", "active", "role_ids"},
		"quota":       {"scope", "scope_id", "currency", "daily_budget", "monthly_budget", "yearly_budget"},
		"alert":       {"name", "description", "scope", "owner_id", "query", "aggregation", "window_seconds", "comparator", "threshold", "for_seconds", "enabled"},
		"provider":    {"name", "type", "base_url", "active", "currency", "cloud_tier", "billing_mode", "subscription_plan", "retry_config", "rate_limit_config"},
	}
	rep := map[string]any{}
	for _, field := range fields[key.kind] {
		rep[field] = row[field]
	}
	if key.kind == "application" || key.kind == "provider" {
		rep["active"] = boolean("active")
	}
	if key.kind == "alert" {
		rep["enabled"] = boolean("enabled")
		if str("scope") == "personal" {
			ref.MemberID = str("owner_id")
		}
	}
	if key.kind == "role" {
		grants := []model.ModelGrantSettings{}
		if rows, ok := row["grants"].([]any); ok {
			for _, v := range rows {
				r := v.(map[string]any)
				grants = append(grants, model.ModelGrantSettings{ModelID: fmt.Sprint(r["model_id"]), Kind: fmt.Sprint(r["model_kind"])})
			}
		}
		rep["model_grants"] = grants
	}
	if key.kind == "provider" {
		for _, field := range []string{"subscription_plan", "retry_config", "rate_limit_config"} {
			v := str(field)
			if v == "" {
				rep[field] = nil
			} else {
				var decoded any
				if err := json.Unmarshal([]byte(v), &decoded); err != nil {
					return nil, err
				}
				rep[field] = decoded
			}
		}
	}
	encoded, err := json.Marshal(rep)
	if err != nil {
		return nil, err
	}
	rec.Representation = string(encoded)
	encoded, err = json.Marshal(ref)
	if err != nil {
		return nil, err
	}
	rec.Reference = string(encoded)
	if err := commonParents(db, model.CommonScope{Family: family, TenantID: tid, OrganizationID: oid}); err != nil {
		return nil, port.ErrParentNotFound
	}
	return rec, nil
}
