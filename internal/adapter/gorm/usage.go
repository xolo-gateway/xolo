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
	CreatedAt         time.Time `gorm:"index:idx_usage_org_created,priority:2;index:idx_usage_user_org_created,priority:3;index:idx_usage_org_payg_cost,priority:3"`
	UserID            string    `gorm:"index;index:idx_usage_user_org_created,priority:1;index:idx_usage_org_payg_cost,priority:4"`
	ApplicationID     string    `gorm:"index"`
	OrgID             string    `gorm:"index;not null;index:idx_usage_org_created,priority:1;index:idx_usage_user_org_created,priority:2;index:idx_usage_org_payg_cost,priority:1"`
	ProviderID        string    `gorm:"index;not null"`
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
	PlanCovered       int    `gorm:"index;default:0;index:idx_usage_org_payg_cost,priority:2"` // 1 if served by a subscription provider
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
