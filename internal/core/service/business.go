package service

import (
	"context"
	"math"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/eventql"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
)

func (s *ProvisioningService) PutBusiness(ctx context.Context, scope model.CommonScope, key string, c model.MatchCondition, p model.BusinessSettings) (model.CommonItem, error) {
	if err := validateBusiness(scope.Family, p); err != nil {
		return model.CommonItem{}, err
	}
	store, ok := s.transactions.(port.BusinessStore)
	if !ok {
		return model.CommonItem{}, port.ErrNotAllowed
	}
	return store.PutBusiness(ctx, scope, key, c, p)
}
func validateBusiness(family string, p model.BusinessSettings) error {
	text := func(name, description string) bool {
		return validCommonText(name, 1, 200) && strings.TrimSpace(name) == name && validCommonText(description, 0, 4000)
	}
	currency := func(v string) bool {
		return slices.Contains(model.SupportedCurrencies, v)
	}
	switch family {
	case "custom_role":
		v := p.Role
		if v == nil || !text(v.Name, v.Description) || v.Permissions == nil || v.ModelGrants == nil {
			return port.ErrInvalid
		}
		for _, code := range v.Permissions {
			if !rbac.IsKnown(code) {
				return port.ErrInvalid
			}
		}
		slices.Sort(v.Permissions)
		v.Permissions = slices.Compact(v.Permissions)
		for _, g := range v.ModelGrants {
			if g.ModelID == "" || (g.Kind != rbac.ModelKindLLM && g.Kind != rbac.ModelKindVirtual) {
				return port.ErrInvalid
			}
		}
		slices.SortFunc(v.ModelGrants, func(a, b model.ModelGrantSettings) int {
			if a.ModelID != b.ModelID {
				return strings.Compare(a.ModelID, b.ModelID)
			}
			return strings.Compare(a.Kind, b.Kind)
		})
		v.ModelGrants = slices.Compact(v.ModelGrants)
	case "application":
		v := p.Application
		if v == nil || !text(v.Name, v.Description) || v.RoleIDs == nil {
			return port.ErrInvalid
		}
		slices.Sort(v.RoleIDs)
		v.RoleIDs = slices.Compact(v.RoleIDs)
	case "quota":
		v := p.Quota
		if v == nil || !currency(v.Currency) || v.ScopeID == "" || (v.Scope != model.QuotaScopeOrg && v.Scope != model.QuotaScopeUser && v.Scope != model.QuotaScopeApplication) {
			return port.ErrInvalid
		}
		for _, b := range []*int64{v.DailyBudget, v.MonthlyBudget, v.YearlyBudget} {
			if b != nil && *b < 0 {
				return port.ErrInvalid
			}
		}
	case "alert":
		v := p.Alert
		if v == nil || !text(v.Name, v.Description) || v.WindowSeconds < 1 || v.WindowSeconds > 31*86400 || v.ForSeconds < 0 || v.ForSeconds > 31*86400 || math.IsNaN(v.Threshold) || math.IsInf(v.Threshold, 0) || v.Threshold < 0 || v.Aggregation != model.AggregationCount || (v.Scope != model.AlertScopeOrg && v.Scope != model.AlertScopePersonal) {
			return port.ErrInvalid
		}
		if v.Scope == model.AlertScopePersonal && v.OwnerID == "" {
			return port.ErrInvalid
		}
		if len(v.Query) > 8192 {
			return port.ErrInvalid
		}
		if _, err := eventql.Compile(v.Query); err != nil {
			return port.ErrInvalid
		}
		switch v.Comparator {
		case model.ComparatorGT, model.ComparatorGTE, model.ComparatorLT, model.ComparatorLTE, model.ComparatorEQ:
		default:
			return port.ErrInvalid
		}
	case "provider":
		v := p.Provider
		if v == nil || !text(v.Name, "") || !currency(v.Currency) || v.CloudTier < 0 || v.CloudTier > 2 {
			return port.ErrInvalid
		}
		if !model.IsKnownProviderType(v.Type) {
			return port.ErrInvalid
		}
		if v.BaseURL != "" {
			u, err := url.Parse(v.BaseURL)
			if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return port.ErrInvalid
			}
		}
		if v.RetryConfig != nil && v.RetryConfig.Enabled && (v.RetryConfig.Delay <= 0 || v.RetryConfig.MaxAttempts < 1 || v.RetryConfig.MaxAttempts > 100) {
			return port.ErrInvalid
		}
		if v.RateLimitConfig != nil && v.RateLimitConfig.Enabled && (v.RateLimitConfig.Interval <= 0 || v.RateLimitConfig.MaxBurst < 1) {
			return port.ErrInvalid
		}
		if v.APIKey != nil && len(*v.APIKey) > 16384 {
			return port.ErrInvalid
		}
		if v.SubscriptionPlan != nil {
			if !validCommonText(v.SubscriptionPlan.Label, 0, 200) || len(v.SubscriptionPlan.Constraints) > 100 {
				return port.ErrInvalid
			}
			for _, c := range v.SubscriptionPlan.Constraints {
				if !validCommonText(c.Label, 0, 200) {
					return port.ErrInvalid
				}
				switch c.Kind {
				case model.ConstraintRollingWindow:
					if c.Duration.Duration() <= 0 || c.Duration.Duration() > 3650*24*time.Hour {
						return port.ErrInvalid
					}
				case model.ConstraintConcurrency:
					if c.MaxConcurrent == nil || *c.MaxConcurrent < 1 {
						return port.ErrInvalid
					}
				default:
					return port.ErrInvalid
				}
				for _, b := range []*int64{c.TokenBudget, c.ValueBudget} {
					if b != nil && *b < 0 {
						return port.ErrInvalid
					}
				}
				for _, ratio := range []*float64{c.ReserveRatio, c.PaceSlack, c.HappyHourStart} {
					if ratio != nil && (math.IsNaN(*ratio) || *ratio < 0 || *ratio > 1) {
						return port.ErrInvalid
					}
				}
				if c.HappyHourStart != nil && *c.HappyHourStart == 0 {
					return port.ErrInvalid
				}
				if c.HappyHourMaxLead != nil && c.HappyHourMaxLead.Duration() <= 0 {
					return port.ErrInvalid
				}
			}
		}
		if v.BillingMode != model.BillingModePayg && v.BillingMode != model.BillingModeSubscription {
			return port.ErrInvalid
		}
	default:
		return port.ErrInvalid
	}
	return nil
}
