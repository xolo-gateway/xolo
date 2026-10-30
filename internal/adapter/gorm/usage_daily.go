package gorm

import (
	"database/sql"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/estimator"
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
// usageSources.
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
	// EnergyWh and CO2Grams are the energy the requests were estimated to use,
	// mid estimates, each with the model it ran on as it was when it was
	// recorded or, for the history, when the column was added.
	EnergyWh float64
	CO2Grams float64
}

func (UsageDaily) TableName() string { return usageDailyTable }

// usageDay formats the day a record falls into, in the server's timezone.
func usageDay(t time.Time) string {
	return t.In(time.Local).Format("2006-01-02")
}

// usageDailyRow is the contribution of a stored record, and of the energy it
// was estimated to use, to its day.
func usageDailyRow(r *UsageRecord, energy model.UsageEnergy) *UsageDaily {
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
		EnergyWh:         energy.Wh,
		CO2Grams:         energy.CO2Grams,
	}
}

// incrementUsageDaily adds a stored record to its day. The upsert is a single
// statement so two concurrent requests add up instead of overwriting each
// other; the columns are qualified by the table, see incrementQuotaUsage.
func incrementUsageDaily(db *gorm.DB, record *UsageRecord, energy model.UsageEnergy) error {
	row := usageDailyRow(record, energy)
	add := func(column string, value any) clause.Expr {
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
			"energy_wh":         add("energy_wh", row.EnergyWh),
			"co2_grams":         add("co2_grams", row.CO2Grams),
		}),
	}).Create(row).Error)
}

// migrateUsageDaily creates the usage_daily table and fills it from the usage
// history.
//
// The migration must run with the other instances stopped: a record an older
// instance writes after the backfill never reaches its day, and the pages
// would under-report that day for good. The same goes for the energy of a
// record an instance predating usageEnergyMigrationID writes: its day keeps
// none. RebuildUsageDaily repairs both.
//
// The migrations do not run in a transaction: the backfill gets its own, so a
// failure does not leave the table half filled.
func migrateUsageDaily(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&UsageDaily{}); err != nil {
		return errors.WithStack(err)
	}
	return errors.WithStack(tx.Transaction(backfillUsageDaily))
}

// RebuildUsageDaily rebuilds the daily usage sums from the usage records. It
// is what the migration runs, exported for the tooling that writes usage
// records in bulk rather than through the store — the seeder.
//
// The energy of every day is estimated again, with the models as they are at
// the rebuild: the energy kept as the requests were recorded is lost.
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
//
// The energy is not kept on the records: it is estimated here, with the
// models as they are now, for each size of request — the estimate is not
// linear in the tokens — then multiplied by the requests of that size. There
// can be as many sizes as records, so the rows are read as they come rather
// than loaded at once; on PostgreSQL the transaction holds its snapshot for as
// long.
func backfillUsageDaily(tx *gorm.DB) error {
	if err := tx.Exec("DELETE FROM " + usageDailyTable).Error; err != nil {
		return errors.WithStack(err)
	}

	energyOf, err := requestEnergyEstimator(tx)
	if err != nil {
		return err
	}

	quarterExpr := utcQuarterExpr(tx)
	keys := "org_id, COALESCE(user_id, ''), COALESCE(application_id, ''), COALESCE(team_id, ''), provider_id, model_id, " +
		"COALESCE(NULLIF(resolved_model_name, ''), proxy_model_name), COALESCE(currency, ''), plan_covered"
	rows, err := tx.Raw(`
		SELECT ` + quarterExpr + ` as quarter, org_id, COALESCE(user_id, '') as user_id, COALESCE(application_id, '') as application_id,
			COALESCE(team_id, '') as team_id, provider_id, model_id, COALESCE(NULLIF(resolved_model_name, ''), proxy_model_name) as model_name,
			COALESCE(currency, '') as currency, plan_covered, prompt_tokens as request_prompt_tokens, completion_tokens as request_completion_tokens,
			COUNT(*) as requests, COALESCE(SUM(cost), 0) as cost, COALESCE(SUM(prompt_tokens), 0) as prompt_tokens,
			COALESCE(SUM(cached_tokens), 0) as cached_tokens, COALESCE(SUM(completion_tokens), 0) as completion_tokens,
			COALESCE(SUM(total_tokens), 0) as total_tokens
		  FROM usage_records
		 GROUP BY ` + quarterExpr + `, ` + keys + `, prompt_tokens, completion_tokens`).Rows()
	if err != nil {
		return errors.WithStack(err)
	}
	defer rows.Close()

	days := make(map[UsageDaily]*UsageDaily)
	for rows.Next() {
		var q struct {
			Quarter                 string
			RequestPromptTokens     int
			RequestCompletionTokens int
			UsageDaily
		}
		if err := tx.ScanRows(rows, &q); err != nil {
			return errors.WithStack(err)
		}
		day, err := localDayOfQuarter(q.Quarter)
		if err != nil {
			return err
		}
		key := UsageDaily{
			OrgID: q.OrgID, Day: day, UserID: q.UserID, ApplicationID: q.ApplicationID, TeamID: q.TeamID,
			ProviderID: q.ProviderID, ModelID: q.ModelID, ModelName: q.ModelName, Currency: q.Currency, PlanCovered: q.PlanCovered,
		}
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
		energy := energyOf(q.ModelID, q.RequestPromptTokens, q.RequestCompletionTokens)
		row.EnergyWh += energy.Wh * float64(q.Requests)
		row.CO2Grams += energy.CO2Grams * float64(q.Requests)
	}
	if err := rows.Err(); err != nil {
		return errors.WithStack(err)
	}
	if len(days) == 0 {
		return nil
	}
	daily := make([]*UsageDaily, 0, len(days))
	for _, row := range days {
		daily = append(daily, row)
	}
	return errors.WithStack(tx.CreateInBatches(daily, 500).Error)
}

// requestEnergyEstimator loads the models and providers once and returns the
// estimated energy of one request to a model, none for a model without an
// active parameter count or deleted since, as service.EstimateUsageEnergy
// does.
func requestEnergyEstimator(tx *gorm.DB) (func(modelID string, promptTokens, completionTokens int) model.UsageEnergy, error) {
	var models []LLMModel
	if err := tx.Select("id", "provider_id", "active_params", "tokens_per_sec_low", "tokens_per_sec_high").Find(&models).Error; err != nil {
		return nil, errors.WithStack(err)
	}
	var providers []Provider
	if err := tx.Select("id", "cloud_tier").Find(&providers).Error; err != nil {
		return nil, errors.WithStack(err)
	}
	tiers := make(map[string]estimator.CloudTier, len(providers))
	for _, p := range providers {
		tiers[p.ID] = estimator.CloudTier(p.CloudTier)
	}
	byID := make(map[string]LLMModel, len(models))
	for _, m := range models {
		byID[m.ID] = m
	}
	return func(modelID string, promptTokens, completionTokens int) model.UsageEnergy {
		m, ok := byID[modelID]
		if !ok {
			return model.UsageEnergy{}
		}
		// A provider deleted since is estimated as a hyperscaler, as the
		// service does.
		tier, ok := tiers[m.ProviderID]
		if !ok {
			tier = estimator.TierHyperscaler
		}
		wh, co2, ok := estimator.RequestEnergy(tier, m.ActiveParams, m.TokensPerSecLow, m.TokensPerSecHigh, promptTokens, completionTokens)
		if !ok {
			return model.UsageEnergy{}
		}
		return model.UsageEnergy{Wh: wh, CO2Grams: co2}
	}, nil
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
	daily bool
	// quarterDays buckets the records on the server's day through their UTC
	// quarter of an hour (utcQuarterExpr), as usage_daily writes its days, so
	// a period read from both does not depend on the database's timezone.
	quarterDays bool
	filter      port.UsageFilter
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

// dimensionExpr is dimensionGroupExpr for the source's table. A day of
// records bucketed by quarter of an hour is turned into the server's day with
// localDayOfQuarter.
func (src usageSource) dimensionExpr(db *gorm.DB, d port.UsageDimension) (string, error) {
	if src.quarterDays && d == port.UsageDimensionDay {
		return utcQuarterExpr(db), nil
	}
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
		sources = append(sources, usageSource{quarterDays: true, filter: filter, bound: func(q *gorm.DB) *gorm.DB { return q.Where("created_at < ?", firstDay) }})
	}
	return append(sources,
		usageSource{daily: true, filter: wholeDays, bound: func(q *gorm.DB) *gorm.DB { return q.Where("day >= ? AND day < ?", fromDay, toDay) }},
		usageSource{quarterDays: true, filter: todayOnward},
	)
}

// readPeriod runs fn, which reads every source of a period, on one snapshot of
// the database when there are several: a record committed between the read of
// the daily sums and the read of today's records would otherwise be counted by
// neither. SQLite gives a transaction its snapshot; PostgreSQL needs REPEATABLE
// READ for that.
func readPeriod(db *gorm.DB, sources []usageSource, fn func(db *gorm.DB) error) error {
	if len(sources) < 2 {
		return fn(db)
	}
	var opts []*sql.TxOptions
	if isPostgres(db) {
		opts = append(opts, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	}
	return errors.WithStack(db.Transaction(fn, opts...))
}

// startOfLocalDay returns the server-local midnight starting t's day.
func startOfLocalDay(t time.Time) time.Time {
	y, m, d := t.In(time.Local).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}
