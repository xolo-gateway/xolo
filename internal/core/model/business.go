package model

// Business extension representations intentionally exclude runtime state and
// provider credentials. APIKey is accepted only as a write-only replacement.
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
	APIKey           *string           `json:"api_key,omitempty"`
}
type BusinessSettings struct {
	Role        *CustomRoleSettings
	Application *ApplicationSettings
	Quota       *QuotaSettings
	Alert       *AlertSettings
	Provider    *ProviderSettings
}

func IsBusinessFamily(family string) bool {
	switch family {
	case "custom_role", "application", "quota", "alert", "provider":
		return true
	}
	return false
}
