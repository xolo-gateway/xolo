package gorm

import (
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

type UsageRecord struct {
	ID string `gorm:"primaryKey;autoIncrement:false"`
	// Composite indexes cover the time-ranged aggregations (SumCostSince*, chart
	// GROUP BYs) that filter on org/user + created_at, avoiding full table scans.
	//
	// idx_usage_org_payg_cost is a covering index for the PAYG cost sums run by
	// the quota enforcer on every proxy request (org, plan_covered, created_at
	// range, then user_id / currency / cost read from the index itself): on a
	// yearly budget it otherwise visited every row of the org since January.
	//
	// idx_usage_org_prov_plan is its subscription counterpart. It leads on
	// provider_id, which the PAYG index does not carry, and serves the plan-wide
	// reads of the fair-share allocator. The DISTINCT count of active users and
	// the per-user presence check are answered from the index alone; the window
	// totals use it for the range only and then read total_tokens and
	// provider_cost from the table.
	// It does not serve the caller's own totals — user_id sits after the
	// created_at range, so an equality on it cannot be used as a prefix; that
	// query stays on idx_usage_user_org_created.
	CreatedAt         time.Time `gorm:"index:idx_usage_org_created,priority:2;index:idx_usage_user_org_created,priority:3;index:idx_usage_org_payg_cost,priority:3;index:idx_usage_org_prov_plan,priority:4"`
	UserID            string    `gorm:"index;index:idx_usage_user_org_created,priority:1;index:idx_usage_org_payg_cost,priority:4;index:idx_usage_org_prov_plan,priority:5"`
	ApplicationID     string    `gorm:"index"`
	OrgID             string    `gorm:"index;not null;index:idx_usage_org_created,priority:1;index:idx_usage_user_org_created,priority:2;index:idx_usage_org_payg_cost,priority:1;index:idx_usage_org_prov_plan,priority:1"`
	ProviderID        string    `gorm:"index;not null;index:idx_usage_org_prov_plan,priority:2"`
	ModelID           string    `gorm:"index;not null"`
	ProxyModelName    string `gorm:"not null"`
	ResolvedModelName string `gorm:""`      // actual model used when virtual model was resolved
	AuthTokenID       string `gorm:"index"` // empty = web session
	PromptTokens      int
	CachedTokens      int
	CompletionTokens  int
	TotalTokens       int
	Cost              int64  `gorm:"index:idx_usage_org_payg_cost,priority:6"` // microcents, frozen at recording time (converted to org currency)
	Currency          string `gorm:"index:idx_usage_org_payg_cost,priority:5"` // frozen from provider
	CostSource        string // "provider" or "computed", see model.CostSource
	PlanCovered       int    `gorm:"index;default:0;index:idx_usage_org_payg_cost,priority:2;index:idx_usage_org_prov_plan,priority:3"` // 1 if served by a subscription provider
	ProviderCost      int64  // equivalent PAYG cost in provider currency (microcents), for plan value budgets
}

type wrappedUsageRecord struct {
	r *UsageRecord
}

func (w *wrappedUsageRecord) ID() model.UsageRecordID { return model.UsageRecordID(w.r.ID) }
func (w *wrappedUsageRecord) UserID() model.UserID    { return model.UserID(w.r.UserID) }
func (w *wrappedUsageRecord) ApplicationID() model.ApplicationID {
	return model.ApplicationID(w.r.ApplicationID)
}
func (w *wrappedUsageRecord) OrgID() model.OrgID           { return model.OrgID(w.r.OrgID) }
func (w *wrappedUsageRecord) ProviderID() model.ProviderID { return model.ProviderID(w.r.ProviderID) }
func (w *wrappedUsageRecord) ModelID() model.LLMModelID    { return model.LLMModelID(w.r.ModelID) }
func (w *wrappedUsageRecord) ProxyModelName() string       { return w.r.ProxyModelName }
func (w *wrappedUsageRecord) AuthTokenID() string          { return w.r.AuthTokenID }
func (w *wrappedUsageRecord) PromptTokens() int            { return w.r.PromptTokens }
func (w *wrappedUsageRecord) CachedTokens() int            { return w.r.CachedTokens }
func (w *wrappedUsageRecord) CompletionTokens() int        { return w.r.CompletionTokens }
func (w *wrappedUsageRecord) TotalTokens() int             { return w.r.TotalTokens }
func (w *wrappedUsageRecord) Cost() int64                  { return w.r.Cost }
func (w *wrappedUsageRecord) Currency() string             { return w.r.Currency }
func (w *wrappedUsageRecord) CostSource() model.CostSource { return model.CostSource(w.r.CostSource) }
func (w *wrappedUsageRecord) ResolvedModelName() string    { return w.r.ResolvedModelName }
func (w *wrappedUsageRecord) CreatedAt() time.Time         { return w.r.CreatedAt }
func (w *wrappedUsageRecord) PlanCovered() bool            { return w.r.PlanCovered != 0 }
func (w *wrappedUsageRecord) ProviderCost() int64          { return w.r.ProviderCost }

var _ model.UsageRecord = &wrappedUsageRecord{}

func fromUsageRecord(r model.UsageRecord) *UsageRecord {
	return &UsageRecord{
		ID:                string(r.ID()),
		UserID:            string(r.UserID()),
		ApplicationID:     string(r.ApplicationID()),
		OrgID:             string(r.OrgID()),
		ProviderID:        string(r.ProviderID()),
		ModelID:           string(r.ModelID()),
		ProxyModelName:    r.ProxyModelName(),
		ResolvedModelName: r.ResolvedModelName(),
		AuthTokenID:       r.AuthTokenID(),
		PromptTokens:      r.PromptTokens(),
		CachedTokens:      r.CachedTokens(),
		CompletionTokens:  r.CompletionTokens(),
		TotalTokens:       r.TotalTokens(),
		Cost:              r.Cost(),
		Currency:          r.Currency(),
		CostSource:        string(r.CostSource()),
		PlanCovered:       boolToInt(r.PlanCovered()),
		ProviderCost:      r.ProviderCost(),
	}
}
