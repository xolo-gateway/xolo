package gorm

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// recordUsagePostgres writes a usage record and adds it to every counter it
// feeds — the budget counters, the hourly and per-minute plan sums, the daily
// sums — in one
// statement, atomic on its own.
//
// Every request of an organization updates the same rows: its counters for
// the day. Each upsert holds their row lock until commit. With one statement
// per counter in a transaction, an organization's throughput was bounded by
// several round trips per request — and by the time its instance took to send
// the next one, CPU-throttled under load — whatever the number of instances
// (about 200 req/s measured, issue #223). In one autocommitted statement with
// data-modifying CTEs, the locks are held while the statement runs and
// commits, without waiting on the client.
//
// The budget counters are written in a fixed order, so concurrent requests
// take their locks in the same order.
func recordUsagePostgres(ctx context.Context, db *gorm.DB, record model.UsageRecord) error {
	dry := db.Session(&gorm.Session{DryRun: true, NewDB: true})
	row := fromUsageRecord(record)

	statements := []*gorm.Statement{dry.Create(row).Statement}
	if counters := quotaUsageRows(record); len(counters) != 0 {
		slices.SortFunc(counters, func(a, b *QuotaUsage) int {
			return cmp.Or(cmp.Compare(a.Scope, b.Scope), cmp.Compare(a.ScopeID, b.ScopeID))
		})
		statements = append(statements, dry.Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "scope"}, {Name: "scope_id"}, {Name: "org_id"}, {Name: "currency"}, {Name: "day"},
			},
			// excluded is the row being inserted: each counter adds its own cost.
			DoUpdates: clause.Assignments(map[string]any{
				"cost": gorm.Expr(quotaUsageTable + ".cost + excluded.cost"),
			}),
		}).Create(&counters).Statement)
	}
	if row.PlanCovered != 0 {
		statements = append(statements, upsertPlanUsageHourly(dry, row).Statement, upsertPlanUsageMinutely(dry, row).Statement)
	}
	statements = append(statements, upsertUsageDaily(dry, row, record.Energy()).Statement)

	query, vars, err := combineStatements(statements)
	if err != nil {
		return err
	}
	// Run on the connection pool directly — gorm's Exec would rewrite the
	// $n placeholders — but traced through its logger, like any statement,
	// for the SQL and slow query logs.
	begin := time.Now()
	result, err := db.Statement.ConnPool.ExecContext(ctx, query, vars...)
	db.Logger.Trace(ctx, begin, func() (string, int64) {
		var rows int64
		if result != nil {
			rows, _ = result.RowsAffected()
		}
		return db.Dialector.Explain(query, vars...), rows
	}, err)
	return errors.WithStack(err)
}

var placeholder = regexp.MustCompile(`\$(\d+)`)

// combineStatements chains rendered PostgreSQL statements into one: all but
// the last become data-modifying CTEs. Their $n placeholders are renumbered
// after the ones before them. The statements hold no literal: every value is
// a bound variable, so a "$" in their text is always a placeholder.
func combineStatements(statements []*gorm.Statement) (string, []any, error) {
	var (
		query strings.Builder
		vars  []any
	)
	for i, statement := range statements {
		if statement.Error != nil {
			return "", nil, errors.WithStack(statement.Error)
		}
		offset := len(vars)
		sql := placeholder.ReplaceAllStringFunc(statement.SQL.String(), func(match string) string {
			n, _ := strconv.Atoi(match[1:])
			return "$" + strconv.Itoa(n+offset)
		})
		vars = append(vars, statement.Vars...)

		switch {
		case i == len(statements)-1:
			if i > 0 {
				query.WriteString(" ")
			}
			query.WriteString(sql)
		case i == 0:
			fmt.Fprintf(&query, "WITH c%d AS (%s)", i, sql)
		default:
			fmt.Fprintf(&query, ", c%d AS (%s)", i, sql)
		}
	}
	return query.String(), vars, nil
}
