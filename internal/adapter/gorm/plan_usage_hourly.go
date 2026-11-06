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

// usageHourLayout spells a UTC hour, the key of plan_usage_hourly. It sorts
// as the hours do, so a range of hours is a range of strings.
const usageHourLayout = "2006-01-02T15"

// PlanUsageHourly sums, per UTC hour, the subscription-covered usage records
// sharing every attribute a plan constraint filters or groups on. The plan
// checks run on every proxied request and their window spans days or weeks:
// they read the whole hours of it here and the records for the rest, the
// start of the window and the current hour, see planUsageSources.
//
// A row is written as usage is recorded, in the same transaction as the
// record. The records stay the source of truth; RebuildPlanUsageHourly
// rebuilds this table from them.
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

// usageHour formats the UTC hour t falls into.
func usageHour(t time.Time) string {
	return t.UTC().Format(usageHourLayout)
}

// incrementPlanUsageHourly adds a stored record to its hour when a plan
// covered it. The upsert is a single statement so two concurrent requests add
// up instead of overwriting each other; the columns are qualified by the
// table, see incrementQuotaUsage.
func incrementPlanUsageHourly(db *gorm.DB, r *UsageRecord) error {
	if r.PlanCovered == 0 {
		return nil
	}
	row := &PlanUsageHourly{
		OrgID:         r.OrgID,
		ProviderID:    r.ProviderID,
		Hour:          usageHour(r.CreatedAt),
		ProviderKeyID: r.ProviderKeyID,
		ModelID:       r.ModelID,
		UserID:        r.UserID,
		Requests:      1,
		TotalTokens:   int64(r.TotalTokens),
		ProviderCost:  r.ProviderCost,
	}
	add := func(column string, value int64) clause.Expr {
		return gorm.Expr(planUsageHourlyTable+"."+column+" + ?", value)
	}
	return errors.WithStack(db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "org_id"}, {Name: "provider_id"}, {Name: "hour"}, {Name: "provider_key_id"}, {Name: "model_id"}, {Name: "user_id"},
		},
		DoUpdates: clause.Assignments(map[string]any{
			"requests":      add("requests", row.Requests),
			"total_tokens":  add("total_tokens", row.TotalTokens),
			"provider_cost": add("provider_cost", row.ProviderCost),
		}),
	}).Create(row).Error)
}

// migratePlanUsageHourly creates the plan_usage_hourly table and fills it
// from the usage history.
//
// Like the usage_daily migration, it must run with the other instances
// stopped: a record an older instance writes after the backfill never reaches
// its hour, and the plan checks would under-count it until the window moves
// past it. RebuildPlanUsageHourly repairs that.
func migratePlanUsageHourly(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&PlanUsageHourly{}); err != nil {
		return errors.WithStack(err)
	}
	return errors.WithStack(tx.Transaction(backfillPlanUsageHourly))
}

// RebuildPlanUsageHourly rebuilds the hourly plan sums from the usage
// records, for the tooling that writes usage records in bulk rather than
// through the store — the seeder.
//
// Nothing else may record usage while it runs: it empties the table before
// summing the records, so the hourly increment of a request recorded in
// between can be lost, and its hour under-counted until the plan windows move
// past it.
func RebuildPlanUsageHourly(db *gorm.DB) error {
	return db.Transaction(backfillPlanUsageHourly)
}

// backfillPlanUsageHourly rebuilds every row from usage_records, after
// emptying the table so a re-run does not double the sums. The hour is the
// UTC one in SQL, as usageHour spells it in Go.
func backfillPlanUsageHourly(tx *gorm.DB) error {
	if err := tx.Exec("DELETE FROM " + planUsageHourlyTable).Error; err != nil {
		return errors.WithStack(err)
	}
	hour := utcHourExpr(tx)
	return errors.WithStack(tx.Exec(`
		INSERT INTO ` + planUsageHourlyTable + ` (org_id, provider_id, hour, provider_key_id, model_id, user_id, requests, total_tokens, provider_cost)
		SELECT org_id, provider_id, ` + hour + `, COALESCE(provider_key_id, ''), model_id, COALESCE(user_id, ''),
			COUNT(*), COALESCE(SUM(total_tokens), 0), COALESCE(SUM(provider_cost), 0)
		  FROM usage_records
		 WHERE plan_covered = 1
		 GROUP BY org_id, provider_id, ` + hour + `, COALESCE(provider_key_id, ''), model_id, COALESCE(user_id, '')`).Error)
}

// utcHourExpr returns the SQL expression of the UTC hour a record was created
// in, spelled as usageHourLayout.
func utcHourExpr(db *gorm.DB) string {
	if isPostgres(db) {
		return `to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24')`
	}
	// strftime normalizes the stored value to UTC whatever offset it carries.
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

// planUsageSource is one part of a plan scope's usage: records, or whole
// hours of plan_usage_hourly.
type planUsageSource func(db *gorm.DB) *gorm.DB

// planUsageSources splits the usage a plan scope counts since a time between
// the records and their hourly sums. Each window reads:
//
//   - the records from its start to the end of that hour, unless it starts on
//     the hour;
//   - the hourly sums of the whole hours after it;
//   - the records of the current hour.
//
// A window starting in the current hour reads the records alone. The
// conditions of every window share one query per source, bounded by the
// earliest and latest of their times so the indexes keep bounding the range.
func planUsageSources(scope model.PlanUsageScope, since, now time.Time) []planUsageSource {
	currentHour := now.Truncate(time.Hour).In(time.Local)

	type part struct {
		w    planUsageWindow
		from time.Time
		to   time.Time
	}
	var heads, hours, tails []part
	for _, w := range planUsageWindows(scope, since) {
		if !w.since.Before(currentHour) {
			tails = append(tails, part{w: w, from: w.since})
			continue
		}
		firstHour := w.since.Truncate(time.Hour).In(time.Local)
		if firstHour.Before(w.since) {
			firstHour = firstHour.Add(time.Hour)
			heads = append(heads, part{w: w, from: w.since, to: firstHour})
		}
		if firstHour.Before(currentHour) {
			hours = append(hours, part{w: w, from: firstHour})
		}
		tails = append(tails, part{w: w, from: currentHour})
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
	records := func(db *gorm.DB) *gorm.DB {
		return db.Model(&UsageRecord{}).
			Where("org_id = ? AND provider_id = ? AND plan_covered = 1", string(scope.OrgID), string(scope.ProviderID))
	}
	// within ORs the windows' conditions, or adds the single one as is.
	within := func(db, query *gorm.DB, parts []part, cond func(p part) (string, []any)) *gorm.DB {
		var group *gorm.DB
		for i, p := range parts {
			c, args := cond(p)
			if i == 0 {
				group = p.w.where(db, c, args...)
			} else {
				group = group.Or(p.w.where(db, c, args...))
			}
		}
		return query.Where(group)
	}

	var sources []planUsageSource
	if len(heads) > 0 {
		from, to := heads[0].from, heads[0].to
		for _, p := range heads[1:] {
			from, to = earliest(from, p.from), latest(to, p.to)
		}
		sources = append(sources, func(db *gorm.DB) *gorm.DB {
			query := records(db).Where("created_at >= ? AND created_at < ?", from, to)
			return filter(within(db, query, heads, func(p part) (string, []any) {
				return "created_at >= ? AND created_at < ?", []any{p.from, p.to}
			}))
		})
	}
	if len(hours) > 0 {
		from := hours[0].from
		for _, p := range hours[1:] {
			from = earliest(from, p.from)
		}
		sources = append(sources, func(db *gorm.DB) *gorm.DB {
			query := db.Model(&PlanUsageHourly{}).
				Where("org_id = ? AND provider_id = ? AND hour >= ? AND hour < ?",
					string(scope.OrgID), string(scope.ProviderID), usageHour(from), usageHour(currentHour))
			return filter(within(db, query, hours, func(p part) (string, []any) {
				return "hour >= ?", []any{usageHour(p.from)}
			}))
		})
	}
	from := tails[0].from
	for _, p := range tails[1:] {
		from = earliest(from, p.from)
	}
	sources = append(sources, func(db *gorm.DB) *gorm.DB {
		query := records(db).Where("created_at >= ?", from)
		return filter(within(db, query, tails, func(p part) (string, []any) {
			return "created_at >= ?", []any{p.from}
		}))
	})
	return sources
}

func earliest(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
