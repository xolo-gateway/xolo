package gorm

import (
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

type Quota struct {
	ID            string `gorm:"primaryKey;autoIncrement:false"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
	Scope         string `gorm:"index:quota_scope_idx,unique;not null"`
	ScopeID       string `gorm:"index:quota_scope_idx,unique;not null"`
	Currency      string `gorm:"not null;default:'USD'"`
	DailyBudget   *int64
	MonthlyBudget *int64
	YearlyBudget  *int64
	// Cascade is the model.QuotaCascade of the scope over its children.
	Cascade string `gorm:"not null;default:'free'"`
}

type wrappedQuota struct {
	q *Quota
}

func (w *wrappedQuota) ID() model.QuotaID           { return model.QuotaID(w.q.ID) }
func (w *wrappedQuota) Scope() model.QuotaScope     { return model.QuotaScope(w.q.Scope) }
func (w *wrappedQuota) ScopeID() string             { return w.q.ScopeID }
func (w *wrappedQuota) Currency() string            { return w.q.Currency }
func (w *wrappedQuota) DailyBudget() *int64         { return w.q.DailyBudget }
func (w *wrappedQuota) MonthlyBudget() *int64       { return w.q.MonthlyBudget }
func (w *wrappedQuota) YearlyBudget() *int64        { return w.q.YearlyBudget }
func (w *wrappedQuota) Cascade() model.QuotaCascade {
	return model.QuotaCascade(w.q.Cascade).Normalize()
}
func (w *wrappedQuota) CreatedAt() time.Time        { return w.q.CreatedAt }
func (w *wrappedQuota) UpdatedAt() time.Time        { return w.q.UpdatedAt }

var _ model.Quota = &wrappedQuota{}

func fromQuota(q model.Quota) *Quota {
	return &Quota{
		ID:            string(q.ID()),
		Scope:         string(q.Scope()),
		ScopeID:       q.ScopeID(),
		Currency:      q.Currency(),
		DailyBudget:   q.DailyBudget(),
		MonthlyBudget: q.MonthlyBudget(),
		YearlyBudget:  q.YearlyBudget(),
		Cascade:       string(q.Cascade().Normalize()),
	}
}
