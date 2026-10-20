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
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
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
	// TeamID binds the application to a team of its organization, empty for
	// none.
	TeamID string `json:"team_id"`
}

// TeamSettings is what a team carries beyond the common contract, whose team
// resource holds its slug, name and status and whose team memberships hold
// its members. MemberRoles lists the team members holding roles on the team
// scope; a member left out holds none.
type TeamSettings struct {
	Description       string               `json:"description"`
	ShareQuotaEqually bool                 `json:"share_quota_equally"`
	ModelGrants       []ModelGrantSettings `json:"model_grants"`
	MiddlewareIDs     []string             `json:"middleware_ids"`
	MemberRoles       []TeamMemberSettings `json:"member_roles"`
}

// TeamMemberSettings is one member of a team, with the roles it holds on the
// team scope.
type TeamMemberSettings struct {
	UserID  string   `json:"user_id"`
	RoleIDs []string `json:"role_ids"`
}

type QuotaSettings struct {
	Scope         QuotaScope `json:"scope"`
	ScopeID       string     `json:"scope_id"`
	Currency      string     `json:"currency"`
	DailyBudget   *int64     `json:"daily_budget"`
	MonthlyBudget *int64     `json:"monthly_budget"`
	YearlyBudget  *int64     `json:"yearly_budget"`
	// Cascade constrains the children budgets of an org or team quota; free
	// when empty.
	Cascade QuotaCascade `json:"cascade"`
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
	// RequestHeaders is optional: an absent value is none.
	RequestHeaders *ProviderRequestHeaders `json:"request_headers"`
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
