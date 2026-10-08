package model

import (
	"fmt"

	"github.com/rs/xid"
)

// The settings below are the representations of the business families, as
// published in projections and accepted by PUT: their JSON tags are the
// representation fields. They carry neither runtime state nor credentials;
// ProviderSettings.APIKey is a write-only replacement, never read back.

type CustomRoleSettings struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Permissions []string             `json:"permissions"`
	ModelGrants []ModelGrantSettings `json:"model_grants"`
}

type ModelGrantSettings struct {
	ModelID string `json:"model_id"`
	Kind    string `json:"kind"`
}

type ApplicationSettings struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Active      bool     `json:"active"`
	RoleIDs     []string `json:"role_ids"`
}

type QuotaSettings struct {
	Scope         QuotaScope `json:"scope"`
	ScopeID       string     `json:"scope_id"`
	Currency      string     `json:"currency"`
	DailyBudget   *int64     `json:"daily_budget"`
	MonthlyBudget *int64     `json:"monthly_budget"`
	YearlyBudget  *int64     `json:"yearly_budget"`
}

type AlertSettings struct {
	Name          string           `json:"name"`
	Description   string           `json:"description"`
	Scope         AlertScope       `json:"scope"`
	OwnerID       string           `json:"owner_id"`
	Query         string           `json:"query"`
	Aggregation   AlertAggregation `json:"aggregation"`
	WindowSeconds int64            `json:"window_seconds"`
	Comparator    AlertComparator  `json:"comparator"`
	Threshold     float64          `json:"threshold"`
	ForSeconds    int64            `json:"for_seconds"`
	Enabled       bool             `json:"enabled"`
}

type ProviderSettings struct {
	Name             string            `json:"name"`
	Type             string            `json:"type"`
	BaseURL          string            `json:"base_url"`
	Active           bool              `json:"active"`
	Currency         string            `json:"currency"`
	CloudTier        int               `json:"cloud_tier"`
	BillingMode      BillingMode       `json:"billing_mode"`
	SubscriptionPlan *SubscriptionPlan `json:"subscription_plan"`
	RetryConfig      *RetryConfig      `json:"retry_config"`
	RateLimitConfig  *RateLimitConfig  `json:"rate_limit_config"`
	// APIKey replaces the stored key; nil keeps it. Never returned.
	APIKey *string `json:"api_key,omitempty"`
}

// ParseBusinessKey accepts the key of a business resource: a canonical UUID,
// chosen by the provisioning client, or the xid of a resource created
// locally. Only a UUID may create a resource.
func ParseBusinessKey(s string) (key string, isUUID bool, err error) {
	if v, err := parseUUID(s); err == nil {
		return v, true, nil
	}
	id, err := xid.FromString(s)
	if err != nil || id.String() != s {
		return "", false, fmt.Errorf("invalid resource key")
	}
	return s, false, nil
}
