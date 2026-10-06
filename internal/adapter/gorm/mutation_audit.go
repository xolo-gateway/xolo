package gorm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
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

var resourceTables = map[string]string{"tenant": "tenants", "organization": "organizations", "user": "users", "membership": "memberships", "role": "roles", "domain": "domains"}

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
	"tenant":       "id, slug, name, description, active",
	"organization": "id, tenant_id, slug, name, description, active, currency, share_quota_equally",
	"user":         "id, tenant_id, provider, subject, email, display_name, active, tenant_role",
	"membership":   "id, org_id, user_id, status",
	"role":         "id, org_id, name, description, builtin, builtin_kind",
	"domain":       "hostname, tenant_id, status",
}

func (tx *provisioningTx) track(kind, id string) error {
	key := mutationKey{kind, id}
	if _, exists := tx.before[key]; exists {
		return nil
	}
	before, err := mutationSnapshot(tx.db, key)
	if err != nil {
		return err
	}
	tx.before[key] = before
	return nil
}
func mutationSnapshot(db *gorm.DB, key mutationKey) ([]byte, error) {
	table, ok := resourceTables[key.kind]
	if !ok {
		return nil, fmt.Errorf("unknown mutation kind")
	}
	columns := mutationColumns[key.kind]
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
	for _, column := range []string{"active", "builtin", "share_quota_equally"} {
		if value, ok := row[column]; ok {
			row[column] = fmt.Sprint(value) == "true" || fmt.Sprint(value) == "1"
		}
	}
	if key.kind == "membership" || key.kind == "role" {
		var tenantID string
		if err := db.Table("organizations").Where("id = ?", row["org_id"]).Pluck("tenant_id", &tenantID).Error; err != nil {
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
	}
	if key.kind == "role" {
		permissions := []string{}
		if err := db.Table("role_permissions").Where("role_id = ?", key.id).Order("code").Pluck("code", &permissions).Error; err != nil {
			return nil, err
		}
		row["permissions"] = permissions
		grants := []struct {
			ModelID   string `json:"model_id"`
			ModelKind string `json:"model_kind"`
		}{}
		if err := db.Table("role_models").Select("model_id, model_kind").Where("role_id = ?", key.id).Order("model_id, model_kind").Find(&grants).Error; err != nil {
			return nil, err
		}
		row["grants"] = grants
	}
	return json.Marshal(row)
}

func (tx *provisioningTx) flushMutations(ctx context.Context) error {
	keys := make([]mutationKey, 0, len(tx.before))
	for key := range tx.before {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].kind != keys[j].kind {
			return keys[i].kind < keys[j].kind
		}
		return keys[i].id < keys[j].id
	})
	actor := model.ActorFromContext(ctx)
	actorJSON, err := json.Marshal(actor)
	if err != nil {
		return err
	}
	for _, key := range keys {
		before := tx.before[key]
		after, err := mutationSnapshot(tx.db, key)
		if err != nil {
			return err
		}
		if bytes.Equal(before, after) {
			continue
		}
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
	return nil
}

func (tx *provisioningTx) trackDependents(kind, id string) error {
	trackIDs := func(resource, column string) error {
		var ids []string
		key := resourceKey(resource)
		if err := tx.db.Table(resourceTables[resource]).Where(column+" = ?", id).Order(key).Pluck(key, &ids).Error; err != nil {
			return err
		}
		for _, child := range ids {
			if err := tx.track(resource, child); err != nil {
				return err
			}
			if err := tx.trackDependents(resource, child); err != nil {
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
		return trackIDs("role", "org_id")
	case "user":
		return trackIDs("membership", "user_id")
	case "role":
		var ids []string
		if err := tx.db.Table("membership_roles").Where("role_id = ?", id).Order("membership_id").Pluck("membership_id", &ids).Error; err != nil {
			return err
		}
		for _, member := range ids {
			if err := tx.track("membership", member); err != nil {
				return err
			}
		}
	}
	return nil
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
