package gorm

import (
	"maps"
	"slices"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// planUsageHourlyTable pins the table name: it is spelled out in the upsert
// and in the backfill, which GORM does not build from the model.
const planUsageHourlyTable = "plan_usage_hourly"

// planUsageMinutelyTable is plan_usage_hourly's per-minute counterpart.
const planUsageMinutelyTable = "plan_usage_minutely"

// usageHourLayout spells a UTC hour, the key of plan_usage_hourly. It sorts
// as the hours do, so a range of hours is a range of strings.
const usageHourLayout = "2006-01-02T15"

// usageMinuteLayout spells a UTC minute, the key of plan_usage_minutely, and
// sorts the same way.
const usageMinuteLayout = "2006-01-02T15:04"

// PlanUsageHourly sums, per UTC hour, the subscription-covered usage records
// sharing every attribute a plan constraint filters or groups on. The plan
// checks run on every proxied request and their window spans days or weeks:
// they read its hours here, the current one included, and the part of an
// hour it starts with from PlanUsageMinutely, see planUsageSources.
//
// A row is written as usage is recorded, in the same transaction as the
// record. The records stay the source of truth; RebuildPlanUsage rebuilds
// this table from them.
type PlanUsageHourly struct {
	// OrgID, ProviderID and Hour lead the key so the window of a plan is a
	// range of it.
	OrgID      string `gorm:"primaryKey"`
	ProviderID string `gorm:"primaryKey"`
	Hour       string `gorm:"primaryKey"`
	// ProviderKeyID is the API key the requests went through, empty for the
	// provider's own, as on the records.
	ProviderKeyID string `gorm:"primaryKey"`
	ModelID       string `gorm:"primaryKey"`
	UserID        string `gorm:"primaryKey"`

	Requests int64
	// TotalTokens and ProviderCost carry the names of the record columns they
	// sum, so one SELECT reads both tables.
	TotalTokens  int64
	ProviderCost int64
}

func (PlanUsageHourly) TableName() string { return planUsageHourlyTable }

// PlanUsageMinutely sums the same records as PlanUsageHourly, per UTC minute.
// The plan checks read it for the part of an hour a window starts with: a
// window rarely starts on the hour, and that part of an hour, read from the
// records, cost as much as the traffic of the hour (#234). It is written with
// plan_usage_hourly, in the same transaction as the record.
type PlanUsageMinutely struct {
	OrgID         string `gorm:"primaryKey"`
	ProviderID    string `gorm:"primaryKey"`
	Minute        string `gorm:"primaryKey"`
	ProviderKeyID string `gorm:"primaryKey"`
	ModelID       string `gorm:"primaryKey"`
	UserID        string `gorm:"primaryKey"`

	Requests     int64
	TotalTokens  int64
	ProviderCost int64
}

func (PlanUsageMinutely) TableName() string { return planUsageMinutelyTable }

// usageHour formats the UTC hour t falls into.
func usageHour(t time.Time) string {
	return t.UTC().Format(usageHourLayout)
}

// usageMinute formats the UTC minute t falls into.
func usageMinute(t time.Time) string {
	return t.UTC().Format(usageMinuteLayout)
}

// incrementPlanUsage adds a stored record to its hour and its minute when a
// plan covered it. Each upsert is a single statement so two concurrent
// requests add up instead of overwriting each other; the columns are
// qualified by the table, see incrementQuotaUsage.
func incrementPlanUsage(db *gorm.DB, r *UsageRecord) error {
	if r.PlanCovered == 0 {
		return nil
	}
	if err := upsertPlanUsageHourly(db, r).Error; err != nil {
		return errors.WithStack(err)
	}
	return errors.WithStack(upsertPlanUsageMinutely(db, r).Error)
}

// upsertPlanUsageHourly runs (or, on a dry-run session, renders) the upsert of
// the hour a plan-covered record falls into.
func upsertPlanUsageHourly(db *gorm.DB, r *UsageRecord) *gorm.DB {
	return db.Clauses(planUsageUpsert(planUsageHourlyTable, "hour", r)).Create(&PlanUsageHourly{
		OrgID: r.OrgID, ProviderID: r.ProviderID, Hour: usageHour(r.CreatedAt),
		ProviderKeyID: r.ProviderKeyID, ModelID: r.ModelID, UserID: r.UserID,
		Requests: 1, TotalTokens: int64(r.TotalTokens), ProviderCost: r.ProviderCost,
	})
}

// upsertPlanUsageMinutely runs (or renders) the upsert of the minute a
// plan-covered record falls into.
func upsertPlanUsageMinutely(db *gorm.DB, r *UsageRecord) *gorm.DB {
	return db.Clauses(planUsageUpsert(planUsageMinutelyTable, "minute", r)).Create(&PlanUsageMinutely{
		OrgID: r.OrgID, ProviderID: r.ProviderID, Minute: usageMinute(r.CreatedAt),
		ProviderKeyID: r.ProviderKeyID, ModelID: r.ModelID, UserID: r.UserID,
		Requests: 1, TotalTokens: int64(r.TotalTokens), ProviderCost: r.ProviderCost,
	})
}

// planUsageUpsert adds a record to the existing row of its period in table,
// whose period column is named period.
func planUsageUpsert(table, period string, r *UsageRecord) clause.OnConflict {
	add := func(column string, value int64) clause.Expr {
		return gorm.Expr(table+"."+column+" + ?", value)
	}
	return clause.OnConflict{
		Columns: []clause.Column{
			{Name: "org_id"}, {Name: "provider_id"}, {Name: period}, {Name: "provider_key_id"}, {Name: "model_id"}, {Name: "user_id"},
		},
		DoUpdates: clause.Assignments(map[string]any{
			"requests":      add("requests", 1),
			"total_tokens":  add("total_tokens", int64(r.TotalTokens)),
			"provider_cost": add("provider_cost", r.ProviderCost),
		}),
	}
}

// migratePlanUsageHourly creates the plan_usage_hourly table and fills it
// from the usage history.
//
// Like the usage_daily migration, it must run with the other instances
// stopped: a record an older instance writes after the backfill never reaches
// its hour, and the plan checks would under-count it until the window moves
// past it. RebuildPlanUsage repairs that.
func migratePlanUsageHourly(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&PlanUsageHourly{}); err != nil {
		return errors.WithStack(err)
	}
	return errors.WithStack(tx.Transaction(backfillPlanUsageHourly))
}

// migratePlanUsageMinutely creates the plan_usage_minutely table and fills it
// from the usage history, under the same condition as
// migratePlanUsageHourly.
func migratePlanUsageMinutely(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&PlanUsageMinutely{}); err != nil {
		return errors.WithStack(err)
	}
	return errors.WithStack(tx.Transaction(backfillPlanUsageMinutely))
}

// RebuildPlanUsage rebuilds the hourly and per-minute plan sums from the
// usage records, for the tooling that writes usage records in bulk rather
// than through the store — the seeder.
//
// Nothing else may record usage while it runs: it empties the tables before
// summing the records, so the increments of a request recorded in between can
// be lost, and its hour and minute under-counted until the plan windows move
// past them.
func RebuildPlanUsage(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := backfillPlanUsageHourly(tx); err != nil {
			return err
		}
		return backfillPlanUsageMinutely(tx)
	})
}

func backfillPlanUsageHourly(tx *gorm.DB) error {
	return backfillPlanUsage(tx, planUsageHourlyTable, "hour", utcPeriodExpr(tx, false))
}

func backfillPlanUsageMinutely(tx *gorm.DB) error {
	return backfillPlanUsage(tx, planUsageMinutelyTable, "minute", utcPeriodExpr(tx, true))
}

// backfillPlanUsage rebuilds every row of table from usage_records, after
// emptying it so a re-run does not double the sums. period is the SQL
// expression of a record's period, the UTC one as usageHour and usageMinute
// spell it in Go.
func backfillPlanUsage(tx *gorm.DB, table, column, period string) error {
	if err := tx.Exec("DELETE FROM " + table).Error; err != nil {
		return errors.WithStack(err)
	}
	return errors.WithStack(tx.Exec(`
		INSERT INTO ` + table + ` (org_id, provider_id, ` + column + `, provider_key_id, model_id, user_id, requests, total_tokens, provider_cost)
		SELECT org_id, provider_id, ` + period + `, COALESCE(provider_key_id, ''), model_id, COALESCE(user_id, ''),
			COUNT(*), COALESCE(SUM(total_tokens), 0), COALESCE(SUM(provider_cost), 0)
		  FROM usage_records
		 WHERE plan_covered = 1
		 GROUP BY org_id, provider_id, ` + period + `, COALESCE(provider_key_id, ''), model_id, COALESCE(user_id, '')`).Error)
}

// utcPeriodExpr returns the SQL expression of the UTC hour, or minute, a
// record was created in, spelled as usageHourLayout or usageMinuteLayout.
func utcPeriodExpr(db *gorm.DB, minute bool) string {
	if isPostgres(db) {
		if minute {
			return `to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI')`
		}
		return `to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24')`
	}
	// strftime normalizes the stored value to UTC whatever offset it carries.
	if minute {
		return "strftime('%Y-%m-%dT%H:%M', created_at)"
	}
	return "strftime('%Y-%m-%dT%H', created_at)"
}

// planUsageWindow is a part of a plan scope counted from one start: the keys
// whose window an override moves each have their own, every other key shares
// the one asked for.
type planUsageWindow struct {
	since time.Time
	// keys narrows the window to these keys or, when exclude is set, to every
	// other key. Nil means every key.
	keys    []string
	exclude bool
}

// planUsageWindows splits a plan scope into its windows.
func planUsageWindows(scope model.PlanUsageScope, since time.Time) []planUsageWindow {
	if len(scope.KeyWindows) == 0 {
		return []planUsageWindow{{since: since}}
	}
	keys := slices.Sorted(maps.Keys(scope.KeyWindows))
	ids := make([]string, len(keys))
	for i, key := range keys {
		ids[i] = string(key)
	}
	windows := []planUsageWindow{{since: since, keys: ids, exclude: true}}
	for _, key := range keys {
		windows = append(windows, planUsageWindow{since: scope.KeyWindows[key], keys: []string{string(key)}})
	}
	return windows
}

// narrow restricts a query to the window's keys.
func (w planUsageWindow) narrow(query *gorm.DB) *gorm.DB {
	switch {
	case w.keys == nil:
		return query
	case w.exclude:
		return query.Where("provider_key_id NOT IN ?", w.keys)
	default:
		return query.Where("provider_key_id = ?", w.keys[0])
	}
}

// where adds the window's keys and the condition on its time column to a
// group of conditions.
func (w planUsageWindow) where(db *gorm.DB, cond string, args ...any) *gorm.DB {
	switch {
	case w.keys == nil:
		return db.Where(cond, args...)
	case w.exclude:
		return db.Where("provider_key_id NOT IN ?", w.keys).Where(cond, args...)
	default:
		return db.Where("provider_key_id = ?", w.keys[0]).Where(cond, args...)
	}
}

// planUsageSource is one part of a plan scope's usage: records, minutes of
// plan_usage_minutely or hours of plan_usage_hourly.
type planUsageSource func(db *gorm.DB) *gorm.DB

// planUsageSources splits the usage a plan scope counts since a time between
// the records and their sums. Each window reads:
//
//   - the records from its start to the next minute, unless it starts on the
//     minute;
//   - the minute sums from there to its first whole hour, or with no end for
//     a window opened in the current hour;
//   - the hourly sums of every hour after that, the current one included.
//
// The rows of the current minute and hour are written in the transaction of
// each record, so they hold every record so far. No part reads more than a
// minute of records: every proxied request runs these reads, and reading the
// part of an hour a window starts with, or the current hour, from the records
// made each check cost as much as the traffic of that hour (#232, #234).
//
// Each window reads its records and its minutes in queries of its own,
// bounded by its own times: the windows of a key and of the plan can start
// days apart, and one query bounded by both would read everything between
// them (#230). The hourly sums, which reach the current hour for every
// window, share one query bounded by the earliest of their starts.
//
// The minute and hour queries have no upper bound, as the records had none:
// a row a replica whose clock runs ahead wrote for the next minute or hour is
// counted, not lost.
func planUsageSources(scope model.PlanUsageScope, since, now time.Time) []planUsageSource {
	currentHour := now.Truncate(time.Hour).In(time.Local)
	since = since.In(time.Local)

	type part struct {
		w    planUsageWindow
		from time.Time
		// to bounds a part of the records or of the minutes; zero for the
		// minutes of a window opened in the current hour.
		to time.Time
	}
	var records, minutes, hours []part
	for _, w := range planUsageWindows(scope, since) {
		firstMinute := ceil(w.since, time.Minute)
		if firstMinute.After(w.since) {
			records = append(records, part{w: w, from: w.since, to: firstMinute})
		}
		if !w.since.Before(currentHour) {
			minutes = append(minutes, part{w: w, from: firstMinute})
			continue
		}
		firstHour := ceil(w.since, time.Hour)
		if firstHour.After(firstMinute) {
			minutes = append(minutes, part{w: w, from: firstMinute, to: firstHour})
		}
		hours = append(hours, part{w: w, from: firstHour})
	}

	// filter applies what every source shares: the models and the key a
	// scope narrows to.
	filter := func(query *gorm.DB) *gorm.DB {
		if len(scope.ModelIDs) > 0 {
			ids := make([]string, len(scope.ModelIDs))
			for i, id := range scope.ModelIDs {
				ids[i] = string(id)
			}
			query = query.Where("model_id IN ?", ids)
		}
		if scope.ByKey {
			query = query.Where("provider_key_id = ?", string(scope.KeyID))
		}
		return query
	}
	var sources []planUsageSource
	for _, p := range records {
		sources = append(sources, func(db *gorm.DB) *gorm.DB {
			query := db.Model(&UsageRecord{}).
				Where("org_id = ? AND provider_id = ? AND plan_covered = 1 AND created_at >= ? AND created_at < ?",
					string(scope.OrgID), string(scope.ProviderID), p.from, p.to)
			return filter(p.w.narrow(query))
		})
	}
	for _, p := range minutes {
		sources = append(sources, func(db *gorm.DB) *gorm.DB {
			query := db.Model(&PlanUsageMinutely{}).
				Where("org_id = ? AND provider_id = ? AND minute >= ?",
					string(scope.OrgID), string(scope.ProviderID), usageMinute(p.from))
			if !p.to.IsZero() {
				query = query.Where("minute < ?", usageMinute(p.to))
			}
			return filter(p.w.narrow(query))
		})
	}
	if len(hours) > 0 {
		from := hours[0].from
		for _, p := range hours[1:] {
			from = earliest(from, p.from)
		}
		sources = append(sources, func(db *gorm.DB) *gorm.DB {
			query := db.Model(&PlanUsageHourly{}).
				Where("org_id = ? AND provider_id = ? AND hour >= ?",
					string(scope.OrgID), string(scope.ProviderID), usageHour(from))
			// The windows' own starts, ORed: each counts its keys from its
			// first whole hour.
			var group *gorm.DB
			for i, p := range hours {
				cond := p.w.where(db, "hour >= ?", usageHour(p.from))
				if i == 0 {
					group = cond
				} else {
					group = group.Or(cond)
				}
			}
			return filter(query.Where(group))
		})
	}
	return sources
}

// ceil returns the first multiple of d at or after t, in the local time zone
// the records' times are compared in.
func ceil(t time.Time, d time.Duration) time.Time {
	c := t.Truncate(d)
	if c.Before(t) {
		c = c.Add(d)
	}
	return c.In(time.Local)
}

func earliest(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}
