package gorm_test

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type recoveryQueryCounter struct {
	logger.Interface
	queries, eventReads, eventWrites atomic.Int64
	foreignKeyChecks                 atomic.Int64
}

func (c *recoveryQueryCounter) Trace(_ context.Context, _ time.Time, sql func() (string, int64), _ error) {
	c.queries.Add(1)
	query, _ := sql()
	query = strings.ToLower(query)
	if query == "pragma foreign_key_check" {
		c.foreignKeyChecks.Add(1)
	}
	if strings.Contains(query, "events") {
		if strings.HasPrefix(query, "select") {
			c.eventReads.Add(1)
		}
		if strings.HasPrefix(query, "update") {
			c.eventWrites.Add(1)
		}
	}
}

// Opt in to the same fixture on both revisions and backends:
// XOLO_UUID_BENCHMARK=1 go test -run '^TestRecoveryLargeFixture$' -count=1 -v ./internal/adapter/gorm
func TestRecoveryLargeFixture(t *testing.T) {
	if os.Getenv("XOLO_UUID_BENCHMARK") != "1" {
		t.Skip("set XOLO_UUID_BENCHMARK=1 for the 300,000-event migration measurement")
	}
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		db = db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
		require.NoError(t, adapter.NewStore(db).Migrate(t.Context()))
		// Remove the deferred lot's schema when running this fixture on the old revision.
		for _, table := range []string{"publications", "mutation_audits", "publication_clocks", "domains", "reserved_domains"} {
			require.NoError(t, db.Migrator().DropTable(table))
		}
		for _, column := range [][2]string{{"users", "tenant_role"}, {"memberships", "common_role"}, {"memberships", "status"}} {
			if db.Migrator().HasColumn(column[0], column[1]) {
				require.NoError(t, db.Exec("ALTER TABLE "+column[0]+" DROP COLUMN "+column[1]).Error)
			}
		}
		require.NoError(t, db.Create(&adapter.Tenant{ID: "legacy-tenant", Slug: "benchmark", Name: "Benchmark", Active: 1}).Error)
		require.NoError(t, db.Create(&adapter.Organization{ID: "legacy-org", TenantID: "legacy-tenant", Slug: "benchmark", Name: "Benchmark", Active: 1}).Error)
		const users = 2000
		const events = 300000
		const graphs = 2000
		const nodesPerGraph = 10
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			for start := 0; start < users; start += 500 {
				rows := make([]map[string]any, 0, 500)
				for i := start; i < start+500 && i < users; i++ {
					id := fmt.Sprintf("legacy-user-%04d", i)
					rows = append(rows, map[string]any{"id": id, "tenant_id": "legacy-tenant", "provider": "fixture", "subject": id, "email": fmt.Sprintf("user%d@example.test", i), "active": true})
				}
				if err := tx.Table("users").Create(&rows).Error; err != nil {
					return err
				}
			}
			for start := 0; start < graphs; start += 100 {
				rows := make([]map[string]any, 0, 100)
				for i := start; i < start+100; i++ {
					user := fmt.Sprintf("legacy-user-%04d", i%users)
					node := fmt.Sprintf(`{"id":"node","data":{"value":%q,"notes":[%s"tail"],"script":"unmatched configuration"}}`,
						user, strings.Repeat(`"unmatched descriptive string",`, 16))
					graph := `{"nodes":[` + strings.Repeat(node+",", nodesPerGraph-1) + node + `],"edges":[]}`
					rows = append(rows, map[string]any{"id": fmt.Sprintf("graph-%04d", i), "org_id": "legacy-org", "name": fmt.Sprintf("graph-%04d", i), "graph_json": graph})
				}
				if err := tx.Table("virtual_models").Create(&rows).Error; err != nil {
					return err
				}
			}
			for start := 0; start < events; start += 500 {
				rows := make([]map[string]any, 0, 500)
				for i := start; i < start+500; i++ {
					user := fmt.Sprintf("legacy-user-%04d", i%users)
					rows = append(rows, map[string]any{"id": fmt.Sprintf("event-%06d", i), "org_id": "legacy-org", "user_id": user, "attributes": fmt.Sprintf(`{"actor_id":%q,"message":"fixture"}`, user)})
				}
				if err := tx.Table("events").Create(&rows).Error; err != nil {
					return err
				}
			}
			return tx.Exec("DELETE FROM migrations WHERE id = ?", "202610020001").Error
		}))
		counter := &recoveryQueryCounter{Interface: logger.Default.LogMode(logger.Silent)}
		db = db.Session(&gorm.Session{Logger: counter})
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		var peak atomic.Uint64
		peak.Store(before.HeapAlloc)
		stop, done := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(done)
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					var m runtime.MemStats
					runtime.ReadMemStats(&m)
					if m.HeapAlloc > peak.Load() {
						peak.Store(m.HeapAlloc)
					}
				}
			}
		}()
		started := time.Now()
		artifact, err := adapter.PlanCommonRecovery(t.Context(), db)
		if err == nil {
			err = adapter.MigrateDatabase(t.Context(), db, artifact)
		}
		elapsed := time.Since(started)
		close(stop)
		<-done
		runtime.ReadMemStats(&after)
		require.NoError(t, err)
		t.Logf("users=%d events=%d graphs=%d nodes_per_graph=%d duration=%s allocated_bytes=%d allocations=%d peak_heap_bytes=%d queries=%d event_reads=%d event_updates=%d", users, events, graphs, nodesPerGraph, elapsed, after.TotalAlloc-before.TotalAlloc, after.Mallocs-before.Mallocs, peak.Load(), counter.queries.Load(), counter.eventReads.Load(), counter.eventWrites.Load())
		var count int64
		require.NoError(t, db.Table("events").Where("user_id LIKE 'legacy-%' OR org_id = 'legacy-org' OR attributes LIKE '%legacy-user-%'").Count(&count).Error)
		require.Zero(t, count)
		require.NoError(t, db.Table("virtual_models").Where("org_id = 'legacy-org' OR graph_json LIKE '%legacy-user-%'").Count(&count).Error)
		require.Zero(t, count)
		require.NoError(t, db.Table("virtual_models").Count(&count).Error)
		require.EqualValues(t, graphs, count)
	})
}
