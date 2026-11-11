package gorm_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rs/xid"
	"github.com/stretchr/testify/require"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	gormpkg "gorm.io/gorm"
)

// planRecord is a subscription usage record of the plan tests, kept in Go to
// compute what the records alone give.
type planRecord struct {
	at       time.Time
	org      string
	provider string
	key      string
	model    string
	user     string
	covered  bool
	tokens   int
	value    int64
}

func writePlanRecord(t *testing.T, db *gormpkg.DB, r planRecord) {
	t.Helper()
	covered := 0
	if r.covered {
		covered = 1
	}
	rec := &xologorm.UsageRecord{
		ID: xid.New().String(), CreatedAt: r.at, UserID: r.user, OrgID: r.org, ProviderID: r.provider, ModelID: r.model,
		ProxyModelName: r.model, TotalTokens: r.tokens, ProviderCost: r.value, ProviderKeyID: r.key,
		Currency: "EUR", PlanCovered: covered, Status: "ok",
	}
	require.NoError(t, db.Create(rec).Error)
}

// expectedPlanUsage filters the records the way a plan scope counts them.
func expectedPlanUsage(records []planRecord, scope model.PlanUsageScope, user string, since time.Time) []planRecord {
	var out []planRecord
	for _, r := range records {
		if !r.covered || r.org != string(scope.OrgID) || r.provider != string(scope.ProviderID) {
			continue
		}
		if user != "" && r.user != user {
			continue
		}
		if scope.ByKey && r.key != string(scope.KeyID) {
			continue
		}
		if len(scope.ModelIDs) > 0 {
			found := false
			for _, id := range scope.ModelIDs {
				found = found || string(id) == r.model
			}
			if !found {
				continue
			}
		}
		start := since
		if w, ok := scope.KeyWindows[model.ProviderKeyID(r.key)]; ok {
			start = w
		}
		if r.at.Before(start) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// The plan checks read the whole hours of their window from plan_usage_hourly,
// its first minutes from plan_usage_minutely and the seconds before them from
// usage_records: every aggregate must be the one the records alone give,
// whatever the window, key windows and filters.
func TestPlanUsageHourly_WindowReadsMatchTheRecords(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := context.Background()
		store := newStoreOn(t, db)
		migrateSchema(t, store)

		now := time.Now()
		hour := now.Truncate(time.Hour)
		// Records on hour boundaries and around them, over three days, with
		// the current hour included.
		offsets := []time.Duration{
			-70*time.Hour - 20*time.Minute, -50 * time.Hour, -30*time.Hour + 17*time.Minute,
			-26*time.Hour - 40*time.Second, -26*time.Hour - time.Second, -26 * time.Hour, -5*time.Hour - 10*time.Minute,
			-3*time.Hour + 5*time.Minute, -2 * time.Hour, -time.Hour + 59*time.Minute,
		}
		var records []planRecord
		for i, off := range offsets {
			at := hour.Add(off)
			for j, r := range []planRecord{
				{key: "", model: "m1", user: "u1", covered: true},
				{key: "k2", model: "m2", user: "u2", covered: true},
				{key: "k3", model: "m1", user: "u1", covered: true},
				{key: "", model: "m1", user: "", covered: true},
				// Neither PAYG traffic nor another provider or org may count.
				{key: "", model: "m1", user: "u3", covered: false},
				{key: "", model: "m1", user: "u4", covered: true, provider: "p2"},
				{key: "", model: "m1", user: "u5", covered: true, org: "org-2"},
			} {
				r.at = at
				if r.org == "" {
					r.org = "org-1"
				}
				if r.provider == "" {
					r.provider = "p1"
				}
				r.tokens = 10*(i+1) + j
				r.value = int64(1000*(i+1) + j)
				writePlanRecord(t, db, r)
				records = append(records, r)
			}
		}
		// The current hour, written through the store as the proxy does.
		live := model.NewUsageRecord("u2", "", "org-1", "", "p1", "m2", "m2", "", 1, 1, 7, 0, "EUR", model.CostSourceComputed, "")
		live.SetPlanCovered(true)
		live.SetProviderCost(77)
		live.SetProviderKeyID("k2")
		require.NoError(t, store.RecordUsage(ctx, live))
		records = append(records, planRecord{at: live.CreatedAt(), org: "org-1", provider: "p1", key: "k2", model: "m2", user: "u2", covered: true, tokens: live.TotalTokens(), value: 77})

		require.NoError(t, xologorm.RebuildPlanUsage(db))

		base := model.PlanUsageScope{OrgID: "org-1", ProviderID: "p1"}
		scopes := map[string]model.PlanUsageScope{
			"provider": base,
			"one key":  {OrgID: "org-1", ProviderID: "p1", ByKey: true, KeyID: "k2"},
			"own key":  {OrgID: "org-1", ProviderID: "p1", ByKey: true, KeyID: model.PrimaryProviderKey},
			"models":   {OrgID: "org-1", ProviderID: "p1", ModelIDs: []model.LLMModelID{"m1"}},
			"key windows": {OrgID: "org-1", ProviderID: "p1", KeyWindows: map[model.ProviderKeyID]time.Time{
				"k2": hour.Add(-3*time.Hour + 5*time.Minute),
				"k3": hour.Add(-26 * time.Hour),
			}},
			"key windows within a minute": {OrgID: "org-1", ProviderID: "p1", KeyWindows: map[model.ProviderKeyID]time.Time{
				"k2": hour.Add(-26*time.Hour - 30*time.Second),
				"k3": hour.Add(-3*time.Hour + 4*time.Minute + 59*time.Second),
			}},
			"key window in the current hour": {OrgID: "org-1", ProviderID: "p1", KeyWindows: map[model.ProviderKeyID]time.Time{
				"k2": hour.Add(time.Minute),
			}},
		}
		sinces := map[string]time.Time{
			"before everything":  hour.Add(-100 * time.Hour),
			"mid hour":           hour.Add(-30*time.Hour + 17*time.Minute),
			"on the hour":        hour.Add(-26 * time.Hour),
			"one second before":  hour.Add(-26*time.Hour - time.Second),
			"within a minute":    hour.Add(-26*time.Hour - 30*time.Second),
			"previous hour":      hour.Add(-time.Hour + 30*time.Minute),
			"current hour":       hour,
			"after every record": now.Add(time.Hour),
		}

		// Every read runs without a memo, then with the memo of a request of
		// u1, which serves the reads of a window from one read of it and
		// reads as usual for u2 (see port.PlanUsageMemo).
		for scopeName, scope := range scopes {
			for sinceName, since := range sinces {
				for _, memo := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s since %s (memo %v)", scopeName, sinceName, memo), func(t *testing.T) {
						ctx := ctx
						if memo {
							ctx = port.WithPlanUsageMemo(ctx, "u1")
						}
						want := expectedPlanUsage(records, scope, "", since)
						var wantTokens, wantValue int64
						wantByKey := map[model.ProviderKeyID]map[model.LLMModelID]port.PlanUsageTotals{}
						wantUsers := map[string]bool{}
						for _, r := range want {
							wantTokens += int64(r.tokens)
							wantValue += r.value
							key := model.ProviderKeyID(r.key)
							if wantByKey[key] == nil {
								wantByKey[key] = map[model.LLMModelID]port.PlanUsageTotals{}
							}
							totals := wantByKey[key][model.LLMModelID(r.model)]
							totals.Tokens += int64(r.tokens)
							totals.Value += r.value
							wantByKey[key][model.LLMModelID(r.model)] = totals
							if r.user != "" {
								wantUsers[r.user] = true
							}
						}

						tokens, value, err := store.SumPlanUsageSince(ctx, scope, since)
						require.NoError(t, err)
						require.Equal(t, wantTokens, tokens, "tokens")
						require.Equal(t, wantValue, value, "value")

						byKey, err := store.SumPlanUsageByKeySince(ctx, scope, since)
						require.NoError(t, err)
						require.Equal(t, wantByKey, byKey)

						wantByModel := map[model.LLMModelID]port.PlanUsageTotals{}
						for _, models := range wantByKey {
							for id, totals := range models {
								sum := wantByModel[id]
								sum.Tokens += totals.Tokens
								sum.Value += totals.Value
								wantByModel[id] = sum
							}
						}
						byModel, err := store.SumPlanUsageByModelSince(ctx, scope, "", since)
						require.NoError(t, err)
						require.Equal(t, wantByModel, byModel, "by model")

						count, err := store.CountActivePlanUsersSince(ctx, scope, since, "")
						require.NoError(t, err)
						require.Equal(t, int64(len(wantUsers)), count, "active users")

						count, err = store.CountActivePlanUsersSince(ctx, scope, since, "u1")
						require.NoError(t, err)
						delete(wantUsers, "u1")
						require.Equal(t, int64(len(wantUsers)), count, "active users but u1")

						for _, user := range []string{"u1", "u2"} {
							var userTokens, userValue int64
							wantByModel := map[model.LLMModelID]port.PlanUsageTotals{}
							for _, r := range expectedPlanUsage(records, scope, user, since) {
								userTokens += int64(r.tokens)
								userValue += r.value
								totals := wantByModel[model.LLMModelID(r.model)]
								totals.Tokens += int64(r.tokens)
								totals.Value += r.value
								wantByModel[model.LLMModelID(r.model)] = totals
							}
							tokens, value, err := store.SumUserPlanUsageSince(ctx, model.UserID(user), scope, since)
							require.NoError(t, err)
							require.Equal(t, userTokens, tokens, "%s tokens", user)
							require.Equal(t, userValue, value, "%s value", user)

							byModel, err := store.SumPlanUsageByModelSince(ctx, scope, model.UserID(user), since)
							require.NoError(t, err)
							require.Equal(t, wantByModel, byModel, "%s by model", user)
						}
					})
				}
			}
		}
	})
}

// Recording usage through the store must leave plan_usage_hourly and
// plan_usage_minutely as a rebuild
// from the records does: the checks would otherwise drift from the records
// until the window moves past the hour.
func TestPlanUsageHourly_RecordingMatchesARebuild(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := context.Background()
		store := newStoreOn(t, db)
		migrateSchema(t, store)

		for i, r := range []struct {
			user, key string
			covered   bool
		}{
			{"u1", "", true}, {"u1", "", true}, {"u2", "k2", true}, {"", "", true}, {"u1", "", false},
		} {
			rec := model.NewUsageRecord(model.UserID(r.user), "", "org-1", "", "p1", "m1", "m1", "", 1, 1, 10+i, 0, "EUR", model.CostSourceComputed, "")
			rec.SetPlanCovered(r.covered)
			rec.SetProviderCost(int64(100 * (i + 1)))
			rec.SetProviderKeyID(model.ProviderKeyID(r.key))
			require.NoError(t, store.RecordUsage(ctx, rec))
		}

		hours := func() []xologorm.PlanUsageHourly {
			var rows []xologorm.PlanUsageHourly
			require.NoError(t, db.Order("org_id, provider_id, hour, provider_key_id, model_id, user_id").Find(&rows).Error)
			return rows
		}
		minutes := func() []xologorm.PlanUsageMinutely {
			var rows []xologorm.PlanUsageMinutely
			require.NoError(t, db.Order("org_id, provider_id, minute, provider_key_id, model_id, user_id").Find(&rows).Error)
			return rows
		}
		recordedHours, recordedMinutes := hours(), minutes()
		require.Len(t, recordedHours, 3, "one row per user and key, PAYG left out")
		require.GreaterOrEqual(t, len(recordedMinutes), 3, "at least one row per user and key, PAYG left out")
		require.NoError(t, xologorm.RebuildPlanUsage(db))
		require.Equal(t, recordedHours, hours())
		require.Equal(t, recordedMinutes, minutes())
	})
}

// Every plan read is one statement, whatever its windows: the plan checks of
// a proxied request run several reads, and a statement per source of each
// multiplied the round trips (#236).
func TestPlanUsageReadsAreOneStatement(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := context.Background()
		store := newStoreOn(t, db)
		migrateSchema(t, store)

		var statements []string
		// GORM runs the callbacks in dry-run mode to render a subquery: only
		// the statements sent to the database count.
		count := func(tx *gormpkg.DB) {
			if !tx.DryRun {
				statements = append(statements, tx.Statement.SQL.String())
			}
		}
		require.NoError(t, db.Callback().Query().After("gorm:query").Register("test:count_query", count))
		require.NoError(t, db.Callback().Row().After("gorm:row").Register("test:count_row", count))
		t.Cleanup(func() {
			_ = db.Callback().Query().Remove("test:count_query")
			_ = db.Callback().Row().Remove("test:count_row")
		})

		now := time.Now()
		// A window starting mid-minute days ago, and a key window opened in
		// the current hour: records, minutes and hours for each.
		since := now.Add(-50*time.Hour - 90*time.Second)
		scope := model.PlanUsageScope{OrgID: "org-1", ProviderID: "p1", KeyWindows: map[model.ProviderKeyID]time.Time{
			"k2": now.Add(-90 * time.Second),
		}}
		reads := map[string]func() error{
			"sum":          func() error { _, _, err := store.SumPlanUsageSince(ctx, scope, since); return err },
			"user sum":     func() error { _, _, err := store.SumUserPlanUsageSince(ctx, "u1", scope, since); return err },
			"by model":     func() error { _, err := store.SumPlanUsageByModelSince(ctx, scope, "u1", since); return err },
			"by key":       func() error { _, err := store.SumPlanUsageByKeySince(ctx, scope, since); return err },
			"active users": func() error { _, err := store.CountActivePlanUsersSince(ctx, scope, since, "u1"); return err },
		}
		for name, read := range reads {
			statements = statements[:0]
			require.NoError(t, read(), name)
			require.Len(t, statements, 1, "%s ran %d statements: %v", name, len(statements), statements)
			require.Contains(t, statements[0], "UNION ALL", name)
		}

		// Within a request's memo, the reads of one window share one statement,
		// whether narrowed to one key of the pool or not.
		ctx = port.WithPlanUsageMemo(ctx, "u1")
		statements = statements[:0]
		for _, name := range []string{"sum", "user sum", "by model", "by key"} {
			require.NoError(t, reads[name](), name)
		}
		_, err := store.SumPlanUsageByModelSince(ctx, scope, "", since)
		require.NoError(t, err)
		oneKey := scope
		oneKey.ByKey, oneKey.KeyID = true, "k2"
		_, _, err = store.SumPlanUsageSince(ctx, oneKey, since)
		require.NoError(t, err)
		require.Len(t, statements, 1, "the reads of one window ran %d statements: %v", len(statements), statements)
	})
}
