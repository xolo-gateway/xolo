package gorm

import (
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// quotaUsageTable pins the table name: it is spelled out in the upsert and in
// the backfill, which GORM does not build from the model.
const quotaUsageTable = "quota_usages"

// QuotaUsage is a running PAYG cost total for one budget scope, currency and
// day. It exists so budget enforcement no longer aggregates over
// usage_records: the yearly window covered the whole table, so every proxied
// request rescanned the entire usage history, and adding gateway replicas only
// added concurrent scans of the same rows.
//
// One row is written per (scope, scope id, org, currency, day) as usage is
// recorded, in the same transaction as the usage record itself. A daily budget
// then reads one row, a monthly one at most 31 and a yearly one at most 366,
// whatever the size of usage_records.
//
// Only PAYG usage is counted: subscription-covered records do not consume a
// monetary budget and are governed by the subscription enforcer instead.
type QuotaUsage struct {
	// Scope is "user" or "org", see model.QuotaScope. Application principals
	// are accounted for at the org scope only: they have no user budget.
	Scope string `gorm:"primaryKey"`
	// ScopeID is the user id or the org id, matching Scope.
	ScopeID string `gorm:"primaryKey"`
	// OrgID is carried for both scopes: a user's budget is per organization, so
	// the same user in two organizations keeps two separate totals.
	OrgID string `gorm:"primaryKey"`
	// Currency is the currency the costs were frozen in, as on the usage record.
	Currency string `gorm:"primaryKey"`
	// Day is the server-local calendar day, "YYYY-MM-DD". A string rather than a
	// date so the comparison against a window start behaves identically on
	// SQLite and PostgreSQL, and so the bucket is computed once, in Go, in the
	// same timezone the budget windows use.
	Day  string `gorm:"primaryKey"`
	Cost int64
}

func (QuotaUsage) TableName() string { return quotaUsageTable }

// quotaUsageDay formats the bucket a timestamp falls into. It uses the
// timestamp's own location, which is the server's local time for a record just
// created — the timezone model.StartOfDay and friends compute budget windows
// in, so a row lands in the window an operator expects.
func quotaUsageDay(t time.Time) string {
	return t.Format("2006-01-02")
}

// quotaUsageRows returns the counter rows a usage record contributes to: one
// for the org, one for the user when the call was made by a user rather than
// by an application. A record that feeds no monetary budget yields no row, per
// model.FeedsMonetaryBudget, the rule the backfill restates in SQL.
func quotaUsageRows(r model.UsageRecord) []*QuotaUsage {
	if !model.FeedsMonetaryBudget(r) {
		return nil
	}

	day := quotaUsageDay(r.CreatedAt())
	rows := []*QuotaUsage{
		{
			Scope:    string(model.QuotaScopeOrg),
			ScopeID:  string(r.OrgID()),
			OrgID:    string(r.OrgID()),
			Currency: r.Currency(),
			Day:      day,
			Cost:     r.Cost(),
		},
	}

	if userID := r.UserID(); userID != "" {
		rows = append(rows, &QuotaUsage{
			Scope:    string(model.QuotaScopeUser),
			ScopeID:  string(userID),
			OrgID:    string(r.OrgID()),
			Currency: r.Currency(),
			Day:      day,
			Cost:     r.Cost(),
		})
	}

	return rows
}
