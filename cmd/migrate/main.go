// Command migrate diagnoses, plans and applies offline schema upgrades.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/ncruces/go-sqlite3/gormlite"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/setup"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: xolo-migrate diagnose [-plan file] | plan -out file | apply [-plan file] -writers-stopped; set XOLO_STORAGE_DATABASE_DSN")
	}
	action := args[0]
	if action != "diagnose" && action != "plan" && action != "apply" {
		return fmt.Errorf("unknown action %q", action)
	}
	flags := flag.NewFlagSet(action, flag.ContinueOnError)
	flags.SetOutput(out)
	planPath := flags.String("plan", "", "saved recovery artifact (diagnose or apply)")
	destination := flags.String("out", "", "new recovery artifact file (plan)")
	stopped := flags.Bool("writers-stopped", false, "confirm all servers, workers and database writers are stopped (apply)")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || (action == "plan" && (*destination == "" || *planPath != "")) || (action != "plan" && *destination != "") {
		return fmt.Errorf("invalid arguments; plan requires -out, diagnose/apply accept -plan")
	}
	if action == "apply" && !*stopped {
		return fmt.Errorf("apply requires -writers-stopped: stop ALL old servers, workers and other writers, and back up the database first")
	}
	var artifact *adapter.RecoveryArtifact
	if *planPath != "" {
		file, err := os.Open(*planPath)
		if err != nil {
			return err
		}
		defer file.Close()
		decoder := json.NewDecoder(file)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&artifact); err != nil {
			return fmt.Errorf("decode recovery plan: %w", err)
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) || artifact == nil {
			return fmt.Errorf("recovery plan must contain exactly one JSON object")
		}
	}
	dsn := os.Getenv("XOLO_STORAGE_DATABASE_DSN")
	if dsn == "" {
		return fmt.Errorf("XOLO_STORAGE_DATABASE_DSN is required")
	}
	db, err := openDatabase(dsn, action != "apply")
	if err != nil {
		return err
	}
	pool, err := db.DB()
	if err != nil {
		return err
	}
	defer pool.Close()
	if action == "apply" {
		if artifact != nil {
			report, err := adapter.DiagnoseCommonRecovery(ctx, db, artifact)
			if err != nil {
				return err
			}
			if len(report.Issues) != 0 {
				if err := writeJSON(out, report); err != nil {
					return err
				}
				return fmt.Errorf("resolve the reported issues before applying this plan")
			}
		}
		if err := adapter.MigrateDatabase(ctx, db, artifact); err != nil {
			return err
		}
		return writeJSON(out, map[string]bool{"applied": true})
	}
	// One read-only snapshot binds planning and diagnostics to the same data.
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if setup.IsPostgresDSN(dsn) {
			if err := tx.Exec("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY").Error; err != nil {
				return err
			}
		}
		if artifact == nil {
			artifact, err = adapter.PlanCommonRecovery(ctx, tx)
			if err != nil {
				return err
			}
		}
		report, err := adapter.DiagnoseCommonRecovery(ctx, tx, artifact)
		if err != nil {
			return err
		}
		if action == "plan" {
			if err := savePlan(*destination, artifact); err != nil {
				return err
			}
		}
		if err := writeJSON(out, report); err != nil {
			return err
		}
		if action == "diagnose" && len(report.Issues) != 0 {
			return fmt.Errorf("migration blocked; resolve the reported issues in a saved plan")
		}
		return nil
	})
}

func openDatabase(dsn string, readOnly bool) (*gorm.DB, error) {
	var dialect gorm.Dialector
	if setup.IsPostgresDSN(dsn) {
		dialect = postgres.Open(dsn)
	} else {
		if readOnly {
			if !strings.HasPrefix(dsn, "file:") {
				path, err := filepath.Abs(dsn)
				if err != nil {
					return nil, err
				}
				dsn = (&url.URL{Scheme: "file", Path: path}).String()
			}
			u, err := url.Parse(dsn)
			if err != nil {
				return nil, fmt.Errorf("invalid SQLite file URI")
			}
			q := u.Query()
			q.Set("mode", "ro")
			u.RawQuery = q.Encode()
			dsn = u.String()
		}
		dialect = gormlite.Open(dsn)
	}
	db, err := gorm.Open(dialect, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		// Driver errors can include credentials from the DSN.
		return nil, fmt.Errorf("cannot open database; check XOLO_STORAGE_DATABASE_DSN and access permissions")
	}
	if !setup.IsPostgresDSN(dsn) {
		pool, err := db.DB()
		if err != nil {
			return nil, err
		}
		pool.SetMaxOpenConns(1)
		if err := db.Exec("PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000").Error; err != nil {
			pool.Close()
			return nil, err
		}
	}
	return db, nil
}

func writeJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func savePlan(path string, artifact *adapter.RecoveryArtifact) (err error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			os.Remove(path)
		}
	}()
	if err := writeJSON(file, artifact); err != nil {
		return err
	}
	return file.Sync()
}
