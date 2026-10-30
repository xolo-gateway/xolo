package gorm

import (
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// usageDailyTable pins the table name: it is spelled out in the upsert and in
// the backfill, which GORM does not build from the model.
const usageDailyTable = "usage_daily"

// UsageDaily sums the usage records of one server-local day sharing every
// attribute the usage pages filter or group on. It exists so those pages stop
// reading every record of their period: a year of an organization is a few
// thousand rows here, against hundreds of thousands of records.
//
// A row is written as usage is recorded, in the same transaction as the record
// itself. The pages read it for the whole days of their period and read the
// records for the rest: the start of the first day and today, see
// usageRollupWindows.
//
// The records stay the source of truth; this table is rebuilt from them by
// RebuildUsageDaily.
type UsageDaily struct {
	// OrgID leads the key so an organization's period is a range of it.
	OrgID string `gorm:"primaryKey"`
	// Day is the server-local calendar day, "YYYY-MM-DD", as quota_usages
	// spells it, and the day the usage charts bucket a record in.
	Day           string `gorm:"primaryKey;index:idx_usage_daily_user_day,priority:2"`
	UserID        string `gorm:"primaryKey;index:idx_usage_daily_user_day,priority:1"`
	ApplicationID string `gorm:"primaryKey"`
	TeamID        string `gorm:"primaryKey"`
	ProviderID    string `gorm:"primaryKey"`
	ModelID       string `gorm:"primaryKey"`
	// ModelName is the name the usage charts show: the resolved model when a
	// virtual model resolved to one, the requested name otherwise.
	ModelName   string `gorm:"primaryKey"`
	Currency    string `gorm:"primaryKey"`
	PlanCovered int    `gorm:"primaryKey"`

	Requests         int64
	Cost             int64
	PromptTokens     int64
	CachedTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

func (UsageDaily) TableName() string { return usageDailyTable }

// usageDay formats the day a record falls into, in the server's timezone.
func usageDay(t time.Time) string {
	return t.In(time.Local).Format("2006-01-02")
}

// usageDailyRow is the contribution of a stored record to its day.
func usageDailyRow(r *UsageRecord) *UsageDaily {
	modelName := r.ResolvedModelName
	if modelName == "" {
		modelName = r.ProxyModelName
	}
	return &UsageDaily{
		OrgID:            r.OrgID,
		Day:              usageDay(r.CreatedAt),
		UserID:           r.UserID,
		ApplicationID:    r.ApplicationID,
		TeamID:           r.TeamID,
		ProviderID:       r.ProviderID,
		ModelID:          r.ModelID,
		ModelName:        modelName,
		Currency:         r.Currency,
		PlanCovered:      r.PlanCovered,
		Requests:         1,
		Cost:             r.Cost,
		PromptTokens:     int64(r.PromptTokens),
		CachedTokens:     int64(r.CachedTokens),
		CompletionTokens: int64(r.CompletionTokens),
		TotalTokens:      int64(r.TotalTokens),
	}
}

// incrementUsageDaily adds a stored record to its day. The upsert is a single
// statement so two concurrent requests add up instead of overwriting each
// other; the columns are qualified by the table, see incrementQuotaUsage.
func incrementUsageDaily(db *gorm.DB, record *UsageRecord) error {
	row := usageDailyRow(record)
	add := func(column string, value int64) clause.Expr {
		return gorm.Expr(usageDailyTable+"."+column+" + ?", value)
	}
	return errors.WithStack(db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "org_id"}, {Name: "day"}, {Name: "user_id"}, {Name: "application_id"}, {Name: "team_id"},
			{Name: "provider_id"}, {Name: "model_id"}, {Name: "model_name"}, {Name: "currency"}, {Name: "plan_covered"},
		},
		DoUpdates: clause.Assignments(map[string]any{
			"requests":          add("requests", row.Requests),
			"cost":              add("cost", row.Cost),
			"prompt_tokens":     add("prompt_tokens", row.PromptTokens),
			"cached_tokens":     add("cached_tokens", row.CachedTokens),
			"completion_tokens": add("completion_tokens", row.CompletionTokens),
			"total_tokens":      add("total_tokens", row.TotalTokens),
		}),
	}).Create(row).Error)
}

// migrateUsageDaily creates the usage_daily table and fills it from the usage
// history.
//
// The migration must run with the other instances stopped: a record an older
// instance writes after the backfill never reaches its day, and the pages
// would under-report that day for good. RebuildUsageDaily repairs it.
func migrateUsageDaily(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&UsageDaily{}); err != nil {
		return errors.WithStack(err)
	}
	return backfillUsageDaily(tx)
}

// RebuildUsageDaily rebuilds the daily usage sums from the usage records. It
// is what the migration runs, exported for the tooling that writes usage
// records in bulk rather than through the store — the seeder.
func RebuildUsageDaily(db *gorm.DB) error {
	return db.Transaction(backfillUsageDaily)
}

// backfillUsageDaily rebuilds every row from usage_records, after emptying the
// table so a re-run does not double the sums.
//
// The records are summed by UTC quarter of an hour in SQL, and each quarter is
// turned into the server's local day in Go, as incrementUsageDaily does: the
// database's timezone may differ from the server's, and a record near midnight
// would then land on another day than the one the reads expect, which counts
// it twice or not at all.
func backfillUsageDaily(tx *gorm.DB) error {
	if err := tx.Exec("DELETE FROM " + usageDailyTable).Error; err != nil {
		return errors.WithStack(err)
	}

	quarterExpr := utcQuarterExpr(tx)
	keys := "org_id, COALESCE(user_id, ''), COALESCE(application_id, ''), COALESCE(team_id, ''), provider_id, model_id, " +
		"COALESCE(NULLIF(resolved_model_name, ''), proxy_model_name), COALESCE(currency, ''), plan_covered"
	var quarters []struct {
		Quarter string
		UsageDaily
	}
	err := tx.Raw(`
		SELECT ` + quarterExpr + ` as quarter, org_id, COALESCE(user_id, '') as user_id, COALESCE(application_id, '') as application_id,
			COALESCE(team_id, '') as team_id, provider_id, model_id, COALESCE(NULLIF(resolved_model_name, ''), proxy_model_name) as model_name,
			COALESCE(currency, '') as currency, plan_covered,
			COUNT(*) as requests, COALESCE(SUM(cost), 0) as cost, COALESCE(SUM(prompt_tokens), 0) as prompt_tokens,
			COALESCE(SUM(cached_tokens), 0) as cached_tokens, COALESCE(SUM(completion_tokens), 0) as completion_tokens,
			COALESCE(SUM(total_tokens), 0) as total_tokens
		  FROM usage_records
		 GROUP BY ` + quarterExpr + `, ` + keys).Scan(&quarters).Error
	if err != nil {
		return errors.WithStack(err)
	}

	days := make(map[UsageDaily]*UsageDaily)
	for _, q := range quarters {
		day, err := localDayOfQuarter(q.Quarter)
		if err != nil {
			return err
		}
		key := q.UsageDaily
		key.Day = day
		key.Requests, key.Cost, key.PromptTokens, key.CachedTokens, key.CompletionTokens, key.TotalTokens = 0, 0, 0, 0, 0, 0
		row, ok := days[key]
		if !ok {
			row = new(UsageDaily)
			*row = key
			days[key] = row
		}
		row.Requests += q.Requests
		row.Cost += q.Cost
		row.PromptTokens += q.PromptTokens
		row.CachedTokens += q.CachedTokens
		row.CompletionTokens += q.CompletionTokens
		row.TotalTokens += q.TotalTokens
	}
	if len(days) == 0 {
		return nil
	}
	rows := make([]*UsageDaily, 0, len(days))
	for _, row := range days {
		rows = append(rows, row)
	}
	return errors.WithStack(tx.CreateInBatches(rows, 500).Error)
}

// utcQuarterExpr returns the SQL expression of the UTC quarter of an hour a
// record was created in, "YYYY-MM-DD HH:MM".
func utcQuarterExpr(db *gorm.DB) string {
	if isPostgres(db) {
		return "to_char(date_trunc('hour', created_at AT TIME ZONE 'UTC') + floor(extract(minute FROM created_at AT TIME ZONE 'UTC') / 15) * interval '15 minutes', 'YYYY-MM-DD HH24:MI')"
	}
	// strftime normalizes the stored value to UTC whatever offset it carries.
	return "strftime('%Y-%m-%d %H:', created_at) || printf('%02d', CAST(strftime('%M', created_at) AS INTEGER) / 15 * 15)"
}

// localDayOfQuarter returns the server-local day of a UTC quarter of an hour.
// Every timezone in use is offset from UTC by whole quarters of an hour, so a
// quarter falls within one local day.
func localDayOfQuarter(quarter string) (string, error) {
	t, err := time.ParseInLocation("2006-01-02 15:04", quarter, time.UTC)
	if err != nil {
		return "", errors.Wrapf(err, "parse usage quarter %q", quarter)
	}
	return usageDay(t), nil
}

// usageSource is one part of a period the usage pages read: either usage
// records, or the daily sums of whole days.
type usageSource struct {
	daily  bool
	filter port.UsageFilter
	// bound narrows the source to its part of the period.
	bound func(*gorm.DB) *gorm.DB
}

// query selects the rows of the source matching its filter.
func (src usageSource) query(db *gorm.DB) *gorm.DB {
	var query *gorm.DB
	if src.daily {
		query = db.Model(&UsageDaily{})
	} else {
		query = db.Model(&UsageRecord{})
	}
	query = applyUsageFilter(query, src.filter)
	if src.bound != nil {
		query = src.bound(query)
	}
	return query
}

// dimensionExpr is dimensionGroupExpr for the source's table.
func (src usageSource) dimensionExpr(db *gorm.DB, d port.UsageDimension) (string, error) {
	if !src.daily {
		return dimensionGroupExpr(db, d)
	}
	switch d {
	case port.UsageDimensionDay:
		return "day", nil
	case port.UsageDimensionModel:
		return "model_name", nil
	case port.UsageDimensionUser:
		return "user_id", nil
	case port.UsageDimensionProvider:
		return "provider_id", nil
	default:
		return "", errors.Errorf("unsupported usage dimension %q", d)
	}
}

// usageSources splits the period of a filter between the usage records and
// their daily sums: the records from the start of the period to the end of its
// first day, unless it starts at midnight, the daily sums of the whole days
// after it, and the records of today. A filter the daily sums cannot answer — on an API token, a requested
// model name or an end of period — reads the records alone, as does a period
// shorter than two days.
//
// The days are server-local, as usage_daily writes them.
func usageSources(filter port.UsageFilter, now time.Time) []usageSource {
	all := []usageSource{{filter: filter}}
	if filter.Since == nil || filter.Until != nil || filter.AuthTokenID != nil || filter.ProxyModelName != nil {
		return all
	}

	// The first whole day: the day of the start of the period when it starts
	// at midnight, the next one otherwise.
	firstDay := startOfLocalDay(*filter.Since)
	partialFirstDay := !firstDay.Equal(*filter.Since)
	if partialFirstDay {
		firstDay = firstDay.AddDate(0, 0, 1)
	}
	today := startOfLocalDay(now)
	if !firstDay.Before(today) {
		return all
	}

	wholeDays := filter
	wholeDays.Since = nil
	fromDay, toDay := usageDay(firstDay), usageDay(today)

	todayOnward := filter
	todayOnward.Since = &today

	sources := make([]usageSource, 0, 3)
	if partialFirstDay {
		sources = append(sources, usageSource{filter: filter, bound: func(q *gorm.DB) *gorm.DB { return q.Where("created_at < ?", firstDay) }})
	}
	return append(sources,
		usageSource{daily: true, filter: wholeDays, bound: func(q *gorm.DB) *gorm.DB { return q.Where("day >= ? AND day < ?", fromDay, toDay) }},
		usageSource{filter: todayOnward},
	)
}

// startOfLocalDay returns the server-local midnight starting t's day.
func startOfLocalDay(t time.Time) time.Time {
	y, m, d := t.In(time.Local).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}
