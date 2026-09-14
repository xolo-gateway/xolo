package gorm_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/ncruces/go-sqlite3/gormlite"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"gorm.io/driver/postgres"
	gormpkg "gorm.io/gorm"
)

// testPostgresDSNEnv points the PostgreSQL backend at an already running server
// instead of letting the integration build spin one up with testcontainers.
// Useful to iterate against a local instance, or in CI environments where a
// database service is provisioned by the pipeline.
const testPostgresDSNEnv = "XOLO_TEST_POSTGRES_DSN"

// backend names one storage backend the store suite can run against.
type backend struct {
	name string
	// newStore returns a store bound to an empty database, torn down when the
	// test ends.
	newStore func(t *testing.T) *xologorm.Store
}

// eachBackend runs fn as a subtest against every backend enabled for this
// build. SQLite always runs; PostgreSQL runs under the "integration" build tag
// (see backend_postgres_integration_test.go), so the default `go test ./...`
// needs no Docker daemon.
//
// Every store test goes through this helper: the two backends speak different
// SQL dialects, report constraint violations differently and disagree on type
// coercion, so a behaviour is only pinned once it is asserted on both.
func eachBackend(t *testing.T, fn func(t *testing.T, store *xologorm.Store)) {
	t.Helper()

	backends := append([]backend{sqliteBackend()}, postgresBackends(t)...)

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			fn(t, b.newStore(t))
		})
	}
}

// newTestStore returns a SQLite-backed store. Prefer eachBackend; this stays
// for the few tests that assert on SQLite-specific behaviour.
func newTestStore(t *testing.T) *xologorm.Store {
	t.Helper()
	return sqliteBackend().newStore(t)
}

func sqliteBackend() backend {
	return backend{
		name: "sqlite",
		newStore: func(t *testing.T) *xologorm.Store {
			t.Helper()
			db, err := gormpkg.Open(gormlite.Open(":memory:"), &gormpkg.Config{})
			if err != nil {
				t.Fatalf("open db: %v", err)
			}
			return newStoreOn(t, db)
		},
	}
}

// newStoreOn binds a store to a database the caller keeps a handle on, for the
// few tests that assert on the schema itself rather than on stored data.
func newStoreOn(t *testing.T, db *gormpkg.DB) *xologorm.Store {
	t.Helper()
	return xologorm.NewStore(db)
}

// newPostgresBackend builds a backend that isolates each test in its own
// schema of the server at dsn, giving the same clean-slate semantics as a
// fresh in-memory SQLite database.
func newPostgresBackend(dsn string) backend {
	return backend{
		name: "postgres",
		newStore: func(t *testing.T) *xologorm.Store {
			t.Helper()

			schema := testSchemaName(t)

			admin, err := gormpkg.Open(postgres.Open(dsn), &gormpkg.Config{})
			if err != nil {
				t.Fatalf("open postgres: %v", err)
			}
			if err := admin.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %q CASCADE`, schema)).Error; err != nil {
				t.Fatalf("drop schema: %v", err)
			}
			if err := admin.Exec(fmt.Sprintf(`CREATE SCHEMA %q`, schema)).Error; err != nil {
				t.Fatalf("create schema: %v", err)
			}
			t.Cleanup(func() {
				if err := admin.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %q CASCADE`, schema)).Error; err != nil {
					t.Logf("drop schema: %v", err)
				}
				closeDB(t, admin)
			})

			db, err := gormpkg.Open(postgres.Open(withSearchPath(dsn, schema)), &gormpkg.Config{})
			if err != nil {
				t.Fatalf("open postgres schema: %v", err)
			}
			t.Cleanup(func() { closeDB(t, db) })

			return xologorm.NewStore(db)
		},
	}
}

// testSchemaName derives a PostgreSQL identifier from the test name. Subtest
// separators and case are folded away, and the result is truncated to stay
// under the 63-byte identifier limit.
func testSchemaName(t *testing.T) string {
	t.Helper()

	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '_'
		}
	}, t.Name())

	schema := "test_" + name
	if len(schema) > 60 {
		schema = schema[:60]
	}

	return schema
}

func withSearchPath(dsn, schema string) string {
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	return dsn + separator + "search_path=" + schema
}

func closeDB(t *testing.T, db *gormpkg.DB) {
	t.Helper()
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
}

// postgresDSNFromEnv returns the DSN of an externally provisioned server, if any.
func postgresDSNFromEnv() string {
	return os.Getenv(testPostgresDSNEnv)
}
