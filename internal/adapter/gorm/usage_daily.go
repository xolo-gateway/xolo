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
	return backfillUsageDaily(db)
}

// backfillUsageDaily rebuilds every row from usage_records in a single
// INSERT ... SELECT, after emptying the table so a re-run does not double the
// sums.
//
// The day is the one the usage charts bucket a record in (dimensionGroupExpr),
// so a backfilled day holds the records the charts showed for it.
func backfillUsageDaily(tx *gorm.DB) error {
	if err := tx.Exec("DELETE FROM " + usageDailyTable).Error; err != nil {
		return errors.WithStack(err)
	}
	dayExpr, err := dimensionGroupExpr(tx, "day")
	if err != nil {
		return err
	}
	modelExpr := "COALESCE(NULLIF(resolved_model_name, ''), proxy_model_name)"
	return errors.WithStack(tx.Exec(`
		INSERT INTO ` + usageDailyTable + ` (org_id, day, user_id, application_id, team_id, provider_id, model_id, model_name, currency, plan_covered,
			requests, cost, prompt_tokens, cached_tokens, completion_tokens, total_tokens)
		SELECT org_id, ` + dayExpr + `, COALESCE(user_id, ''), COALESCE(application_id, ''), COALESCE(team_id, ''), provider_id, model_id, ` + modelExpr + `, COALESCE(currency, ''), plan_covered,
			COUNT(*), COALESCE(SUM(cost), 0), COALESCE(SUM(prompt_tokens), 0), COALESCE(SUM(cached_tokens), 0), COALESCE(SUM(completion_tokens), 0), COALESCE(SUM(total_tokens), 0)
		  FROM usage_records
		 GROUP BY org_id, ` + dayExpr + `, COALESCE(user_id, ''), COALESCE(application_id, ''), COALESCE(team_id, ''), provider_id, model_id, ` + modelExpr + `, COALESCE(currency, ''), plan_covered`).Error)
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
// first day, the daily sums of the whole days after it, and the records of
// today. A filter the daily sums cannot answer — on an API token, a requested
// model name or an end of period — reads the records alone, as does a period
// shorter than two days.
//
// The days are server-local, as usage_daily writes them.
func usageSources(filter port.UsageFilter, now time.Time) []usageSource {
	all := []usageSource{{filter: filter}}
	if filter.Since == nil || filter.Until != nil || filter.AuthTokenID != nil || filter.ProxyModelName != nil {
		return all
	}

	firstDayEnd := startOfLocalDay(*filter.Since).AddDate(0, 0, 1)
	today := startOfLocalDay(now)
	if !firstDayEnd.Before(today) {
		return all
	}

	wholeDays := filter
	wholeDays.Since = nil
	fromDay, toDay := usageDay(firstDayEnd), usageDay(today)

	todayOnward := filter
	todayOnward.Since = &today

	return []usageSource{
		{filter: filter, bound: func(q *gorm.DB) *gorm.DB { return q.Where("created_at < ?", firstDayEnd) }},
		{daily: true, filter: wholeDays, bound: func(q *gorm.DB) *gorm.DB { return q.Where("day >= ? AND day < ?", fromDay, toDay) }},
		{filter: todayOnward},
	}
}

// startOfLocalDay returns the server-local midnight starting t's day.
func startOfLocalDay(t time.Time) time.Time {
	y, m, d := t.In(time.Local).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}
