package gorm

import (
	"strconv"
	"time"

	"github.com/pkg/errors"
	"gorm.io/gorm"
)

// migrateQuotaUsageCounters creates the quota_usages table and fills it from
// the usage history, so budgets keep the totals they had before enforcement
// stopped aggregating over usage_records.
func migrateQuotaUsageCounters(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&QuotaUsage{}); err != nil {
		return errors.WithStack(err)
	}
	return backfillQuotaUsage(tx)
}

// RebuildQuotaUsage rebuilds the budget counters from the usage history. It is
// what the migration runs, exported for the tooling that writes usage records
// in bulk rather than through the store — the seeder — and would otherwise
// leave every budget reading zero.
func RebuildQuotaUsage(db *gorm.DB) error {
	return backfillQuotaUsage(db)
}

// backfillQuotaUsage rebuilds every counter from usage_records in a single
// pass per scope. It is written as INSERT ... SELECT rather than a loop in Go
// because the history it walks is exactly the one the change exists to stop
// reading row by row.
//
// The table is emptied first so the migration can be re-run on a partially
// filled table without doubling the totals.
func backfillQuotaUsage(tx *gorm.DB) error {
	if err := tx.Exec("DELETE FROM " + quotaUsageTable).Error; err != nil {
		return errors.WithStack(err)
	}

	dayExpr, args := localDayExpr(tx)

	// The conditions restate model.FeedsMonetaryBudget, which the store applies
	// in Go, so the two produce the same totals.
	//
	// The user scope skips an empty user_id rather than grouping it. In
	// production an application authenticates through a shadow user and its
	// records do carry that user's id, so they get a user counter like any
	// other; the filter is for records written without any principal at all.
	orgSQL := `
		INSERT INTO ` + quotaUsageTable + ` (scope, scope_id, org_id, currency, day, cost)
		SELECT 'org', org_id, org_id, currency, ` + dayExpr + `, SUM(cost)
		  FROM usage_records
		 WHERE plan_covered = 0 AND org_id <> '' AND cost <> 0
		 GROUP BY org_id, currency, ` + dayExpr
	if err := tx.Exec(orgSQL, append(append([]any{}, args...), args...)...).Error; err != nil {
		return errors.WithStack(err)
	}

	userSQL := `
		INSERT INTO ` + quotaUsageTable + ` (scope, scope_id, org_id, currency, day, cost)
		SELECT 'user', user_id, org_id, currency, ` + dayExpr + `, SUM(cost)
		  FROM usage_records
		 WHERE plan_covered = 0 AND org_id <> '' AND cost <> 0 AND user_id <> ''
		 GROUP BY user_id, org_id, currency, ` + dayExpr
	if err := tx.Exec(userSQL, append(append([]any{}, args...), args...)...).Error; err != nil {
		return errors.WithStack(err)
	}

	return nil
}

// localDayExpr returns a SQL expression bucketing created_at into the server's
// local calendar day, the timezone the budget windows are computed in, along
// with the bind arguments it needs.
//
// The offset is the one in effect now, applied to the whole history: a row
// recorded on the other side of a DST switch can land in the neighbouring day.
// That is immaterial for a backfill — it moves a few rows between two adjacent
// day buckets of the past, and month and year totals are unaffected — while
// resolving each row's own offset would mean walking the history in Go.
func localDayExpr(tx *gorm.DB) (string, []any) {
	_, offsetSeconds := time.Now().Zone()

	if isSQLite(tx) {
		// strftime normalizes the stored value to UTC whatever offset it carries,
		// so shifting by the local offset lands on the local day. The modifier is
		// a signed seconds string, e.g. '+7200 seconds'.
		return "strftime('%Y-%m-%d', created_at, ?)", []any{formatSecondsModifier(offsetSeconds)}
	}

	// PostgreSQL: AT TIME ZONE 'UTC' turns the timestamptz into a plain UTC
	// timestamp regardless of the session timezone — without it the result would
	// depend on how the connection is configured, and could be shifted twice.
	return "to_char(created_at AT TIME ZONE 'UTC' + make_interval(secs => ?), 'YYYY-MM-DD')", []any{offsetSeconds}
}

// formatSecondsModifier renders a seconds offset the way SQLite's date
// functions expect it, sign included.
func formatSecondsModifier(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign = "-"
		seconds = -seconds
	}
	return sign + strconv.Itoa(seconds) + " seconds"
}
