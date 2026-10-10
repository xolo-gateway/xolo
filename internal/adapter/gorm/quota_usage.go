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
	// Scope is one of model.QuotaScope. Application principals are accounted
	// for under their own scope (and the org scope), never under the shadow
	// user's user scope; see model.QuotaCounters.
	Scope string `gorm:"primaryKey"`
	// ScopeID is the user id, the application id, or the org id, matching
	// Scope. An application id is the principal the proxy attributes the call
	// to when an application token authenticates through a shadow user.
	ScopeID string `gorm:"primaryKey"`
	// OrgID is carried for every scope: a user's budget is per organization, so
	// the same user in two organizations keeps two separate totals, and an
	// application's spend likewise stays partitioned by organization.
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

// quotaUsageRows returns the counter rows a usage record contributes to, as
// listed by model.QuotaCounters.
func quotaUsageRows(r model.UsageRecord) []*QuotaUsage {
	counters := model.QuotaCounters(r)
	rows := make([]*QuotaUsage, 0, len(counters))
	for _, c := range counters {
		rows = append(rows, &QuotaUsage{
			Scope:    string(c.Scope),
			ScopeID:  c.ScopeID,
			OrgID:    string(r.OrgID()),
			Currency: r.Currency(),
			Day:      quotaUsageDay(r.CreatedAt()),
			Cost:     r.Cost(),
		})
	}
	return rows
}
