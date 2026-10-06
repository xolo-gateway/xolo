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

var resourceTables = map[string]string{"tenant": "tenants", "organization": "organizations", "user": "users", "membership": "memberships", "role": "roles"}

// Explicit allowlists exclude secrets and technical timestamps, including future columns.
var mutationColumns = map[string]string{
	"tenant":       "id, slug, name, description, active",
	"organization": "id, tenant_id, slug, name, description, active, currency, share_quota_equally",
	"user":         "id, tenant_id, provider, subject, email, display_name, active",
	"membership":   "id, org_id, user_id",
	"role":         "id, org_id, name, description, builtin, builtin_kind",
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
	if err := db.Table(table).Select(columns).Where("id = ?", key.id).Find(&rows).Error; err != nil {
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
		if err := tx.db.Table(resourceTables[resource]).Where(column+" = ?", id).Order("id").Pluck("id", &ids).Error; err != nil {
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
