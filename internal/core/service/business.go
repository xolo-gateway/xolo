package service

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/eventql"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/crypto"
)

// WithSecretKey sets the key encrypting the provider credentials, the one
// of XOLO_SECRET_KEY.
func WithSecretKey(key string) ProvisioningServiceOptionFunc {
	return func(s *ProvisioningService) { s.secretKey = key }
}

const (
	maxBusinessNameLength        = 200
	maxBusinessDescriptionLength = 4000
	maxAlertQueryLength          = 8192
	maxAlertDuration             = 31 * 24 * time.Hour
	maxProviderKeyLength         = 16384
)

// PutCustomRole creates the custom role or brings it to the given
// representation. Builtin roles are not writable.
func (s *ProvisioningService) PutCustomRole(ctx context.Context, tenantID model.TenantID, orgID model.OrgID, key string, condition model.MatchCondition, v model.CustomRoleSettings) (model.CommonItem, error) {
	if err := normalizeCustomRole(&v); err != nil {
		return model.CommonItem{}, err
	}
	scope := model.CommonScope{Family: model.FamilyCustomRole, TenantID: string(tenantID), OrganizationID: string(orgID)}
	return s.PutCommon(ctx, scope, key, condition, func(ctx context.Context, tx *ProvisioningService) error {
		target, err := tx.businessTarget(ctx, scope, key, v, func() (string, error) {
			role, err := tx.roleStore.GetRoleByID(ctx, model.RoleID(key))
			if err != nil {
				return "", err
			}
			if role.Builtin() && role.OrgID() == orgID {
				return "", errors.Wrap(port.ErrNotAllowed, "builtin roles can not be modified")
			}
			return string(role.OrgID()), nil
		})
		if err != nil || target.unchanged {
			return err
		}
		grants := make([]model.ModelGrant, 0, len(v.ModelGrants))
		for _, grant := range v.ModelGrants {
			if err := tx.checkModelGrant(ctx, orgID, grant); err != nil {
				return err
			}
			grants = append(grants, model.ModelGrant{ModelID: grant.ModelID, Kind: grant.Kind})
		}
		role := model.NewRole(orgID, v.Name, v.Description)
		role.SetID(model.RoleID(key))
		role.SetPermissions(v.Permissions)
		role.SetModelGrants(grants)
		if target.exists {
			return errors.WithStack(tx.roleStore.SaveRole(ctx, role))
		}
		return errors.WithStack(tx.roleStore.CreateRole(ctx, role))
	})
}

// PutApplication creates the application or brings it to the given
// representation, roles included, in one transaction. Its tokens are local.
func (s *ProvisioningService) PutApplication(ctx context.Context, tenantID model.TenantID, orgID model.OrgID, key string, condition model.MatchCondition, v model.ApplicationSettings) (model.CommonItem, error) {
	if !validBusinessText(v.Name, v.Description) || v.RoleIDs == nil {
		return model.CommonItem{}, errors.WithStack(port.ErrInvalid)
	}
	v.RoleIDs = sortedUnique(v.RoleIDs)
	scope := model.CommonScope{Family: model.FamilyApplication, TenantID: string(tenantID), OrganizationID: string(orgID)}
	return s.PutCommon(ctx, scope, key, condition, func(ctx context.Context, tx *ProvisioningService) error {
		target, err := tx.businessTarget(ctx, scope, key, v, func() (string, error) {
			app, err := tx.tx.GetApplication(ctx, model.ApplicationID(key))
			if err != nil {
				return "", err
			}
			return string(app.OrgID()), nil
		})
		if err != nil || target.unchanged {
			return err
		}
		roles := make([]model.RoleID, 0, len(v.RoleIDs))
		for _, id := range v.RoleIDs {
			role, err := tx.roleStore.GetRoleByID(ctx, model.RoleID(id))
			if errors.Is(err, port.ErrNotFound) || (err == nil && role.OrgID() != orgID) {
				return errors.Wrapf(port.ErrParentNotFound, "role %q not found", id)
			}
			if err != nil {
				return errors.WithStack(err)
			}
			roles = append(roles, role.ID())
		}
		app := model.NewApplication(orgID, v.Name, v.Description, v.Active)
		app.SetID(model.ApplicationID(key))
		if target.exists {
			err = tx.tx.UpdateApplication(ctx, app)
		} else {
			err = tx.tx.CreateApplication(ctx, app)
		}
		if err != nil {
			return errors.WithStack(err)
		}
		return errors.WithStack(tx.tx.SetApplicationRoles(ctx, app.ID(), roles))
	})
}

// PutQuota creates the quota of a scope or changes its budgets. A quota hangs
// from its tenant; its scope never changes, and the spend already recorded is
// kept.
func (s *ProvisioningService) PutQuota(ctx context.Context, tenantID model.TenantID, key string, condition model.MatchCondition, v model.QuotaSettings) (model.CommonItem, error) {
	if !slices.Contains(model.SupportedCurrencies, v.Currency) || v.ScopeID == "" ||
		(v.Scope != model.QuotaScopeOrg && v.Scope != model.QuotaScopeUser && v.Scope != model.QuotaScopeApplication) {
		return model.CommonItem{}, errors.WithStack(port.ErrInvalid)
	}
	for _, budget := range []*int64{v.DailyBudget, v.MonthlyBudget, v.YearlyBudget} {
		if budget != nil && *budget < 0 {
			return model.CommonItem{}, errors.WithStack(port.ErrInvalid)
		}
	}
	scope := model.CommonScope{Family: model.FamilyQuota, TenantID: string(tenantID)}
	return s.PutCommon(ctx, scope, key, condition, func(ctx context.Context, tx *ProvisioningService) error {
		target, err := tx.businessTarget(ctx, scope, key, v, func() (string, error) {
			// The tenant of an existing quota is the one of its projection:
			// found in another scope, it is foreign.
			if _, err := tx.tx.GetQuotaByID(ctx, model.QuotaID(key)); err != nil {
				return "", err
			}
			return "", nil
		})
		if err != nil || target.unchanged {
			return err
		}
		if err := tx.checkQuotaScope(ctx, tenantID, v.Scope, v.ScopeID); err != nil {
			return err
		}
		if target.exists {
			var current model.QuotaSettings
			if err := json.Unmarshal(target.current, &current); err != nil {
				return errors.WithStack(err)
			}
			if current.Scope != v.Scope || current.ScopeID != v.ScopeID {
				return errors.Wrap(port.ErrNotAllowed, "the scope of a quota can not change")
			}
		}
		existing, err := tx.tx.GetQuota(ctx, v.Scope, v.ScopeID)
		if err == nil && string(existing.ID()) != key {
			return errors.Wrap(port.ErrAlreadyExists, "the scope already has a quota")
		}
		if err != nil && !errors.Is(err, port.ErrNotFound) {
			return errors.WithStack(err)
		}
		quota := model.NewQuota(v.Scope, v.ScopeID, v.Currency, v.DailyBudget, v.MonthlyBudget, v.YearlyBudget)
		quota.SetID(model.QuotaID(key))
		return errors.WithStack(tx.tx.SetQuota(ctx, quota))
	})
}

// PutAlert creates the alert or brings it to the given representation. Its
// scope and owner never change. A changed alert restarts from the ok state,
// as an edit from the web UI does.
func (s *ProvisioningService) PutAlert(ctx context.Context, tenantID model.TenantID, orgID model.OrgID, key string, condition model.MatchCondition, v model.AlertSettings) (model.CommonItem, error) {
	if err := validateAlert(v); err != nil {
		return model.CommonItem{}, err
	}
	scope := model.CommonScope{Family: model.FamilyAlert, TenantID: string(tenantID), OrganizationID: string(orgID)}
	return s.PutCommon(ctx, scope, key, condition, func(ctx context.Context, tx *ProvisioningService) error {
		target, err := tx.businessTarget(ctx, scope, key, v, func() (string, error) {
			alert, err := tx.tx.GetAlertByID(ctx, model.AlertID(key))
			if err != nil {
				return "", err
			}
			return string(alert.OrgID()), nil
		})
		if err != nil || target.unchanged {
			return err
		}
		if v.OwnerID != "" {
			membership, err := tx.orgStore.GetUserOrgMembership(ctx, model.UserID(v.OwnerID), orgID)
			if errors.Is(err, port.ErrNotFound) || (err == nil && membership.Status() != model.StatusActive) {
				return errors.Wrap(port.ErrParentNotFound, "owner is no active member of the organization")
			}
			if err != nil {
				return errors.WithStack(err)
			}
		}
		alert := model.NewAlert(orgID, model.UserID(v.OwnerID), v.Name,
			model.WithAlertDescription(v.Description), model.WithAlertScope(v.Scope), model.WithAlertQuery(v.Query),
			model.WithAlertAggregation(v.Aggregation), model.WithAlertWindow(time.Duration(v.WindowSeconds)*time.Second),
			model.WithAlertComparator(v.Comparator), model.WithAlertThreshold(v.Threshold),
			model.WithAlertFor(time.Duration(v.ForSeconds)*time.Second), model.WithAlertEnabled(v.Enabled))
		alert.SetID(model.AlertID(key))
		if !target.exists {
			return errors.WithStack(tx.tx.CreateAlert(ctx, alert))
		}
		current, err := tx.tx.GetAlertByID(ctx, alert.ID())
		if err != nil {
			return errors.WithStack(err)
		}
		if current.OwnerID() != alert.OwnerID() || current.Scope() != alert.Scope() {
			return errors.Wrap(port.ErrNotAllowed, "the scope and owner of an alert can not change")
		}
		alert.SetState(model.AlertStateOK)
		alert.SetPendingSince(nil)
		alert.SetLastEvaluatedAt(current.LastEvaluatedAt())
		return errors.WithStack(tx.tx.UpdateAlert(ctx, alert))
	})
}

// PutProvider creates the provider or brings it to the given representation.
// Its key is write-only: required at creation, kept when omitted, encrypted
// at rest and never returned.
func (s *ProvisioningService) PutProvider(ctx context.Context, tenantID model.TenantID, orgID model.OrgID, key string, condition model.MatchCondition, v model.ProviderSettings) (model.CommonItem, error) {
	if err := validateProvider(v); err != nil {
		return model.CommonItem{}, err
	}
	scope := model.CommonScope{Family: model.FamilyProvider, TenantID: string(tenantID), OrganizationID: string(orgID)}
	return s.PutCommon(ctx, scope, key, condition, func(ctx context.Context, tx *ProvisioningService) error {
		target, err := tx.businessTarget(ctx, scope, key, v, func() (string, error) {
			provider, err := tx.tx.GetProviderByID(ctx, model.ProviderID(key))
			if err != nil {
				return "", err
			}
			return string(provider.OrgID()), nil
		})
		if err != nil {
			return err
		}
		var current model.Provider
		encrypted := ""
		if target.exists {
			if current, err = tx.tx.GetProviderByID(ctx, model.ProviderID(key)); err != nil {
				return errors.WithStack(err)
			}
			encrypted = current.APIKey()
		} else if v.APIKey == nil {
			return errors.Wrap(port.ErrInvalid, "api_key is required to create a provider")
		}
		if v.APIKey != nil {
			same := false
			if encrypted != "" {
				plain, err := crypto.Decrypt(s.secretKey, encrypted)
				if err != nil {
					return errors.WithStack(err)
				}
				same = plain == *v.APIKey
			}
			if !same {
				if encrypted, err = crypto.Encrypt(s.secretKey, *v.APIKey); err != nil {
					return errors.WithStack(err)
				}
			}
		}
		if target.unchanged && (current == nil || encrypted == current.APIKey()) {
			return nil
		}
		provider := model.NewProvider(orgID, v.Name, v.Type, v.BaseURL, encrypted, v.Currency)
		provider.SetID(model.ProviderID(key))
		provider.SetActive(v.Active)
		provider.SetCloudTier(v.CloudTier)
		provider.SetBillingMode(v.BillingMode)
		provider.SetSubscriptionPlan(v.SubscriptionPlan)
		provider.SetRetryConfig(v.RetryConfig)
		provider.SetRateLimitConfig(v.RateLimitConfig)
		if target.exists {
			return errors.WithStack(tx.tx.SaveProvider(ctx, provider))
		}
		return errors.WithStack(tx.tx.CreateProvider(ctx, provider))
	})
}

// businessResource is the state of the resource a business PUT targets.
type businessResource struct {
	exists, unchanged bool
	current           json.RawMessage
}

// businessTarget resolves the resource a business PUT targets, within the
// transaction. A resource of another organization or tenant is reported as
// not found, like a missing one; only a UUID creates a resource. unchanged
// tells that the representation already is the desired one. owner returns the
// organization of the resource stored under key, ErrNotFound when none is.
func (tx *ProvisioningService) businessTarget(ctx context.Context, scope model.CommonScope, key string, desired any, owner func() (string, error)) (businessResource, error) {
	var target businessResource
	_, isUUID, err := model.ParseBusinessKey(key)
	if err != nil {
		return target, errors.WithStack(port.ErrInvalid)
	}
	if _, err := tx.commonParentTenant(ctx, model.TenantID(scope.TenantID)); err != nil {
		return target, err
	}
	if scope.OrganizationID != "" {
		org, err := tx.orgStore.GetOrgByID(ctx, model.OrgID(scope.OrganizationID))
		if errors.Is(err, port.ErrNotFound) || (err == nil && string(org.TenantID()) != scope.TenantID) {
			return target, errors.Wrap(port.ErrParentNotFound, "organization not found")
		}
		if err != nil {
			return target, errors.WithStack(err)
		}
	}
	item, err := tx.tx.ReadProjection(ctx, scope, key)
	switch {
	case err == nil:
		target.exists, target.current = true, item.Representation
		target.unchanged, err = sameRepresentation(desired, item.Representation)
		return target, err
	case !errors.Is(err, port.ErrNotFound):
		return target, errors.WithStack(err)
	}
	// Not in this scope: the key is free, or held by a resource elsewhere.
	switch _, err := owner(); {
	case err == nil:
		return target, errors.Wrap(port.ErrNotFound, "resource not found")
	case errors.Is(err, port.ErrNotAllowed):
		return target, err
	case !errors.Is(err, port.ErrNotFound):
		return target, errors.WithStack(err)
	}
	if !isUUID {
		return target, errors.Wrap(port.ErrInvalid, "a new resource is identified by a UUID")
	}
	return target, nil
}

// sameRepresentation compares a desired representation with a projection,
// field by field, the provider key aside.
func sameRepresentation(desired any, projection json.RawMessage) (bool, error) {
	raw, err := json.Marshal(desired)
	if err != nil {
		return false, errors.WithStack(err)
	}
	var want, have map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		return false, errors.WithStack(err)
	}
	if err := json.Unmarshal(projection, &have); err != nil {
		return false, errors.WithStack(err)
	}
	delete(want, "api_key")
	a, err := json.Marshal(want)
	if err != nil {
		return false, errors.WithStack(err)
	}
	b, err := json.Marshal(have)
	if err != nil {
		return false, errors.WithStack(err)
	}
	return bytes.Equal(a, b), nil
}

// checkModelGrant asserts the granted model belongs to the organization, and
// so does the provider of an LLM model.
func (tx *ProvisioningService) checkModelGrant(ctx context.Context, orgID model.OrgID, grant model.ModelGrantSettings) error {
	notFound := errors.Wrapf(port.ErrParentNotFound, "model %q not found", grant.ModelID)
	if grant.Kind == rbac.ModelKindVirtual {
		vm, err := tx.tx.GetVirtualModelByID(ctx, model.VirtualModelID(grant.ModelID))
		if errors.Is(err, port.ErrNotFound) || (err == nil && vm.OrgID() != orgID) {
			return notFound
		}
		return errors.WithStack(err)
	}
	llm, err := tx.tx.GetLLMModelByID(ctx, model.LLMModelID(grant.ModelID))
	if errors.Is(err, port.ErrNotFound) || (err == nil && llm.OrgID() != orgID) {
		return notFound
	}
	if err != nil {
		return errors.WithStack(err)
	}
	provider, err := tx.tx.GetProviderByID(ctx, llm.ProviderID())
	if errors.Is(err, port.ErrNotFound) || (err == nil && provider.OrgID() != orgID) {
		return notFound
	}
	return errors.WithStack(err)
}

// checkQuotaScope asserts the capped principal belongs to the tenant: an
// organization, a member, or an application of one of its organizations.
func (tx *ProvisioningService) checkQuotaScope(ctx context.Context, tenantID model.TenantID, scope model.QuotaScope, scopeID string) error {
	notFound := errors.Wrapf(port.ErrParentNotFound, "%s %q not found", scope, scopeID)
	orgID := model.OrgID(scopeID)
	switch scope {
	case model.QuotaScopeUser:
		user, err := tx.userStore.GetUserByID(ctx, model.UserID(scopeID))
		if errors.Is(err, port.ErrNotFound) || (err == nil && (user.TenantID() != tenantID || user.Provider() == model.ApplicationProvider)) {
			return notFound
		}
		return errors.WithStack(err)
	case model.QuotaScopeApplication:
		app, err := tx.tx.GetApplication(ctx, model.ApplicationID(scopeID))
		if errors.Is(err, port.ErrNotFound) {
			return notFound
		}
		if err != nil {
			return errors.WithStack(err)
		}
		orgID = app.OrgID()
	}
	org, err := tx.orgStore.GetOrgByID(ctx, orgID)
	if errors.Is(err, port.ErrNotFound) || (err == nil && org.TenantID() != tenantID) {
		return notFound
	}
	return errors.WithStack(err)
}

func validBusinessText(name, description string) bool {
	return validCommonText(name, 1, maxBusinessNameLength) && strings.TrimSpace(name) == name &&
		validCommonText(description, 0, maxBusinessDescriptionLength)
}

func sortedUnique(values []string) []string {
	out := slices.Clone(values)
	slices.Sort(out)
	return slices.Compact(out)
}

func normalizeCustomRole(v *model.CustomRoleSettings) error {
	if !validBusinessText(v.Name, v.Description) || v.Permissions == nil || v.ModelGrants == nil {
		return errors.WithStack(port.ErrInvalid)
	}
	if err := validatePermissions(v.Permissions); err != nil {
		return err
	}
	v.Permissions = sortedUnique(v.Permissions)
	grants := make([]model.ModelGrant, 0, len(v.ModelGrants))
	for _, g := range v.ModelGrants {
		grants = append(grants, model.ModelGrant{ModelID: g.ModelID, Kind: g.Kind})
	}
	if err := validateModelGrants(grants); err != nil {
		return err
	}
	v.ModelGrants = slices.Clone(v.ModelGrants)
	slices.SortFunc(v.ModelGrants, func(a, b model.ModelGrantSettings) int {
		if c := strings.Compare(a.ModelID, b.ModelID); c != 0 {
			return c
		}
		return strings.Compare(a.Kind, b.Kind)
	})
	v.ModelGrants = slices.Compact(v.ModelGrants)
	return nil
}

func validateAlert(v model.AlertSettings) error {
	window := time.Duration(v.WindowSeconds) * time.Second
	dwell := time.Duration(v.ForSeconds) * time.Second
	if !validBusinessText(v.Name, v.Description) || v.WindowSeconds < 1 || window > maxAlertDuration ||
		v.ForSeconds < 0 || dwell > maxAlertDuration || math.IsNaN(v.Threshold) || math.IsInf(v.Threshold, 0) ||
		v.Aggregation != model.AggregationCount || len(v.Query) > maxAlertQueryLength {
		return errors.WithStack(port.ErrInvalid)
	}
	switch v.Scope {
	case model.AlertScopeOrg:
	case model.AlertScopePersonal:
		if v.OwnerID == "" {
			return errors.Wrap(port.ErrInvalid, "a personal alert needs an owner")
		}
	default:
		return errors.WithStack(port.ErrInvalid)
	}
	if v.OwnerID != "" {
		if _, err := model.ParseUserID(v.OwnerID); err != nil {
			return errors.WithStack(port.ErrInvalid)
		}
	}
	switch v.Comparator {
	case model.ComparatorGT, model.ComparatorGTE, model.ComparatorLT, model.ComparatorLTE, model.ComparatorEQ:
	default:
		return errors.WithStack(port.ErrInvalid)
	}
	if _, err := eventql.Compile(v.Query); err != nil {
		return errors.Wrap(port.ErrInvalid, "invalid query")
	}
	return nil
}

func validateProvider(v model.ProviderSettings) error {
	if !validBusinessText(v.Name, "") || !model.IsKnownProviderType(v.Type) ||
		!slices.Contains(model.SupportedCurrencies, v.Currency) || v.CloudTier < 0 || v.CloudTier > 2 ||
		(v.BillingMode != model.BillingModePayg && v.BillingMode != model.BillingModeSubscription) {
		return errors.WithStack(port.ErrInvalid)
	}
	if v.BaseURL != "" {
		u, err := url.Parse(v.BaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.Wrap(port.ErrInvalid, "invalid base_url")
		}
	}
	if v.APIKey != nil && (*v.APIKey == "" || len(*v.APIKey) > maxProviderKeyLength) {
		return errors.Wrap(port.ErrInvalid, "invalid api_key")
	}
	if c := v.RetryConfig; c != nil && c.Enabled && (c.Delay <= 0 || c.MaxAttempts < 1 || c.MaxAttempts > 100) {
		return errors.Wrap(port.ErrInvalid, "invalid retry_config")
	}
	if c := v.RateLimitConfig; c != nil && c.Enabled && (c.Interval <= 0 || c.MaxBurst < 1) {
		return errors.Wrap(port.ErrInvalid, "invalid rate_limit_config")
	}
	if plan := v.SubscriptionPlan; plan != nil {
		if !validCommonText(plan.Label, 0, maxBusinessNameLength) || len(plan.Constraints) > 100 {
			return errors.Wrap(port.ErrInvalid, "invalid subscription_plan")
		}
		for _, c := range plan.Constraints {
			if err := validatePlanConstraint(c); err != nil {
				return err
			}
		}
	}
	return nil
}

func validatePlanConstraint(c model.PlanConstraint) error {
	invalid := errors.Wrap(port.ErrInvalid, "invalid subscription_plan constraint")
	if !validCommonText(c.Label, 0, maxBusinessNameLength) {
		return invalid
	}
	switch c.Kind {
	case model.ConstraintRollingWindow:
		if c.Duration.Duration() <= 0 || c.Duration.Duration() > 3650*24*time.Hour {
			return invalid
		}
	case model.ConstraintConcurrency:
		if c.MaxConcurrent == nil || *c.MaxConcurrent < 1 {
			return invalid
		}
	default:
		return invalid
	}
	for _, budget := range []*int64{c.TokenBudget, c.ValueBudget} {
		if budget != nil && *budget < 0 {
			return invalid
		}
	}
	for _, ratio := range []*float64{c.ReserveRatio, c.PaceSlack, c.HappyHourStart} {
		if ratio != nil && (math.IsNaN(*ratio) || *ratio < 0 || *ratio > 1) {
			return invalid
		}
	}
	if c.HappyHourStart != nil && *c.HappyHourStart == 0 {
		return invalid
	}
	if c.HappyHourMaxLead != nil && c.HappyHourMaxLead.Duration() <= 0 {
		return invalid
	}
	return nil
}
