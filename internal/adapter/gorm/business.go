package gorm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/crypto"
	"gorm.io/gorm"
)

// ConfigureBusiness runs before any writer starts. Credentials use the existing
// provider encryption format, so public proxy adapters require no translation.
func (s *Store) ConfigureBusiness(enabled bool, key string) {
	s.businessEnabled = enabled
	s.businessSecretKey = key
}
func (s *Store) PutBusiness(ctx context.Context, scope model.CommonScope, key string, c model.MatchCondition, p model.BusinessSettings) (model.CommonItem, error) {
	if !s.businessEnabled {
		return model.CommonItem{}, port.ErrNotAllowed
	}
	return s.WriteCommon(ctx, scope, key, c, func(bound port.ProvisioningTx) error {
		tx := bound.(*Store)
		db, _ := tx.getDatabase(ctx)
		if !model.IsBusinessFamily(scope.Family) || !businessKey(key) {
			return port.ErrInvalid
		}
		if err := commonParents(db, scope); err != nil {
			return err
		}
		if err := requireLive(db, scope, key); err != nil {
			return err
		}
		kind := businessKind(scope.Family)
		raw, err := mutationSnapshot(db, mutationKey{kind, key})
		if err != nil {
			return err
		}
		exists := string(raw) != "null"
		var previous *CommonRecord
		if exists {
			previous, err = businessProjection(db, mutationKey{kind, key}, raw)
			if err != nil {
				return err
			}
			if previous == nil || previous.TenantID != scope.TenantID || previous.OrganizationID != scope.OrganizationID {
				return port.ErrNotFound
			}
		}
		if !c.Matches(tx.mutations.etag) {
			return port.ErrPreconditionFailed
		}
		oid := model.OrgID(scope.OrganizationID)
		switch scope.Family {
		case "custom_role":
			v := p.Role
			if v == nil {
				return port.ErrInvalid
			}
			grants := []model.ModelGrant{}
			for _, g := range v.ModelGrants {
				table := "llm_models"
				if g.Kind == rbac.ModelKindVirtual {
					table = "virtual_models"
				}
				var n int64
				q := db.Table(table).Where(table+".id = ? AND "+table+".org_id = ?", g.ModelID, scope.OrganizationID)
				if g.Kind == rbac.ModelKindLLM {
					q = q.Joins("JOIN providers bp ON bp.id = llm_models.provider_id AND bp.org_id = llm_models.org_id")
				}
				if err := q.Count(&n).Error; err != nil {
					return err
				}
				if n != 1 {
					return port.ErrParentNotFound
				}
				grants = append(grants, model.ModelGrant{ModelID: g.ModelID, Kind: g.Kind})
			}
			if businessUnchanged(previous, v) {
				return nil
			}
			role := model.NewRole(oid, v.Name, v.Description)
			role.SetID(model.RoleID(key))
			role.SetPermissions(v.Permissions)
			role.SetModelGrants(grants)
			if exists {
				return tx.SaveRole(ctx, role)
			}
			return tx.CreateRole(ctx, role)
		case "application":
			v := p.Application
			if v == nil {
				return port.ErrInvalid
			}
			roles := []model.RoleID{}
			for _, id := range v.RoleIDs {
				role, err := tx.GetRoleByID(ctx, model.RoleID(id))
				if err != nil {
					return err
				}
				if role.OrgID() != oid {
					return port.ErrParentNotFound
				}
				roles = append(roles, role.ID())
			}
			if businessUnchanged(previous, v) {
				return nil
			}
			app := model.NewApplication(oid, v.Name, v.Description, v.Active)
			app.SetID(model.ApplicationID(key))
			if exists {
				err = tx.UpdateApplication(ctx, app)
			} else {
				err = tx.CreateApplication(ctx, app)
			}
			if err != nil {
				return err
			}
			return tx.SetApplicationRoles(ctx, app.ID(), roles)
		case "quota":
			v := p.Quota
			if v == nil {
				return port.ErrInvalid
			}
			parent := model.CommonScope{TenantID: scope.TenantID}
			parentKey := v.ScopeID
			switch v.Scope {
			case model.QuotaScopeUser:
				parent.Family = "member"
			case model.QuotaScopeOrg:
				parent.Family = "organization"
			case model.QuotaScopeApplication:
				var app Application
				if err = db.First(&app, "id = ?", v.ScopeID).Error; err != nil {
					return invitationReadError(err)
				}
				parent.Family = "organization"
				parentKey = app.OrgID
			default:
				return port.ErrInvalid
			}
			if _, err = readCommon(db, parent, parentKey); err != nil {
				return port.ErrParentNotFound
			}
			if err = requireLive(db, parent, parentKey); err != nil {
				return err
			}
			var old Quota
			err = db.Where("scope = ? AND scope_id = ?", v.Scope, v.ScopeID).First(&old).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err == nil && old.ID != key {
				return port.ErrAlreadyExists
			}
			if exists {
				var prev Quota
				if err = db.First(&prev, "id = ?", key).Error; err != nil {
					return err
				}
				if prev.Scope != string(v.Scope) || prev.ScopeID != v.ScopeID {
					return port.ErrNotAllowed
				}
			}
			if businessUnchanged(previous, v) {
				return nil
			}
			q := model.NewQuota(v.Scope, v.ScopeID, v.Currency, v.DailyBudget, v.MonthlyBudget, v.YearlyBudget)
			q.SetID(model.QuotaID(key))
			return tx.SetQuota(ctx, q)
		case "alert":
			v := p.Alert
			if v == nil {
				return port.ErrInvalid
			}
			if v.OwnerID != "" {
				var n int64
				if err = db.Model(&User{}).Where("id = ? AND tenant_id = ?", v.OwnerID, scope.TenantID).Count(&n).Error; err != nil {
					return err
				}
				if n != 1 {
					return port.ErrParentNotFound
				}
				if err = requireLive(db, model.CommonScope{Family: "member", TenantID: scope.TenantID}, v.OwnerID); err != nil {
					return err
				}
			}
			if businessUnchanged(previous, v) {
				return nil
			}
			alert := model.NewAlert(oid, model.UserID(v.OwnerID), v.Name, model.WithAlertDescription(v.Description), model.WithAlertScope(v.Scope), model.WithAlertQuery(v.Query), model.WithAlertAggregation(v.Aggregation), model.WithAlertWindow(time.Duration(v.WindowSeconds)*time.Second), model.WithAlertComparator(v.Comparator), model.WithAlertThreshold(v.Threshold), model.WithAlertFor(time.Duration(v.ForSeconds)*time.Second), model.WithAlertEnabled(v.Enabled))
			alert.SetID(model.AlertID(key))
			if exists {
				old, err := tx.GetAlertByID(ctx, alert.ID())
				if err != nil {
					return err
				}
				if old.OwnerID() != alert.OwnerID() || old.Scope() != alert.Scope() {
					return port.ErrNotAllowed
				}
				alert.SetState(old.State())
				alert.SetPendingSince(old.PendingSince())
				alert.SetLastEvaluatedAt(old.LastEvaluatedAt())
				return tx.UpdateAlert(ctx, alert)
			}
			return tx.CreateAlert(ctx, alert)
		case "provider":
			v := p.Provider
			if v == nil {
				return port.ErrInvalid
			}
			var current Provider
			if exists {
				if err = db.First(&current, "id = ? AND org_id = ?", key, scope.OrganizationID).Error; err != nil {
					return invitationReadError(err)
				}
			}
			encrypted := current.APIKey
			if v.APIKey != nil {
				same := false
				if encrypted != "" {
					plain, e := crypto.Decrypt(tx.businessSecretKey, encrypted)
					if e != nil {
						return e
					}
					same = plain == *v.APIKey
				}
				if !same {
					encrypted, err = crypto.Encrypt(tx.businessSecretKey, *v.APIKey)
					if err != nil {
						return err
					}
				}
			} else if !exists {
				return port.ErrInvalid
			}
			if encrypted == current.APIKey && businessUnchanged(previous, v) {
				return nil
			}
			row := Provider{ID: key, OrgID: scope.OrganizationID, Name: v.Name, Type: v.Type, BaseURL: v.BaseURL, APIKey: encrypted, Active: boolToInt(v.Active), Currency: v.Currency, CloudTier: v.CloudTier, BillingMode: string(v.BillingMode), SubscriptionPlan: JSONColumn[model.SubscriptionPlan]{Val: v.SubscriptionPlan}, RetryConfig: JSONColumn[model.RetryConfig]{Val: v.RetryConfig}, RateLimitConfig: JSONColumn[model.RateLimitConfig]{Val: v.RateLimitConfig}}
			if exists {
				return tx.SaveProvider(ctx, &wrappedProvider{&row})
			}
			return tx.CreateProvider(ctx, &wrappedProvider{&row})
		}
		return port.ErrInvalid
	})
}

// Configuration snapshots omit evaluation state. This prevents workers from
// changing configuration validators or generating spurious update events.
func businessSnapshot(db *gorm.DB, key mutationKey, row map[string]any) error {
	if key.kind == "application" {
		roles := []string{}
		if err := db.Table("application_roles").Where("application_id = ?", key.id).Order("role_id").Pluck("role_id", &roles).Error; err != nil {
			return err
		}
		row["role_ids"] = roles
	}
	if key.kind == "alert" {
		for _, field := range []string{"state", "pending_since", "last_evaluated_at"} {
			delete(row, field)
		}
	}
	if key.kind == "provider" {
		// Ciphertext is private audit data; events/GET never project this field.
		for _, field := range []string{"retry_config", "rate_limit_config", "subscription_plan"} {
			if raw, ok := row[field].(json.RawMessage); ok {
				row[field] = string(raw)
			}
		}
	}
	return nil
}

// Compare the canonical public configuration after parent validation. A repeated
// PUT must not touch underlying timestamps, grants, audit or publication.
func businessUnchanged(previous *CommonRecord, settings any) bool {
	if previous == nil {
		return false
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return false
	}
	var desired, old map[string]any
	if json.Unmarshal(raw, &desired) != nil || json.Unmarshal([]byte(previous.Representation), &old) != nil {
		return false
	}
	delete(desired, "api_key")
	a, _ := json.Marshal(desired)
	b, _ := json.Marshal(old)
	return bytes.Equal(a, b)
}
