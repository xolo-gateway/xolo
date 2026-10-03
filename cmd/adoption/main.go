// Command adoption exports/verifies common inventories and detaches an instance
// offline without changing resource IDs, identity links or feed history.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/user"

	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/ncruces/go-sqlite3/gormlite"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/adoption"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/setup"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "adoption:", err)
		os.Exit(1)
	}
}
func run() error {
	action := flag.String("action", "export", "export, verify or detach")
	path := flag.String("file", "", "export destination / file to verify")
	stopped := flag.Bool("writers-stopped", false, "confirm every server, worker and writer is stopped")
	access := flag.Bool("operator-access-verified", false, "confirm a real platform administrator login was tested before stopping writers")
	flag.Parse()
	if *action == "verify" {
		raw, err := os.ReadFile(*path)
		if err != nil {
			return err
		}
		p, err := adoption.Decode(raw)
		if err != nil {
			return err
		}
		fmt.Printf("Verified %d records; replay C0 before activating the inventory.\n", p.Count)
		return nil
	}
	if *action != "export" && *action != "detach" {
		return fmt.Errorf("unknown action")
	}
	if *action == "detach" && (!*stopped || !*access) {
		return fmt.Errorf("detach requires -writers-stopped and -operator-access-verified")
	}
	if *action == "export" && (*path == "" || *path == "-") {
		return fmt.Errorf("export requires -file")
	}
	dsn := os.Getenv("XOLO_STORAGE_DATABASE_DSN")
	if dsn == "" {
		return fmt.Errorf("XOLO_STORAGE_DATABASE_DSN is required")
	}
	var dialect gorm.Dialector = gormlite.Open(dsn)
	if setup.IsPostgresDSN(dsn) {
		dialect = postgres.Open(dsn)
	} else {
		if _, err := os.Stat(dsn); err != nil {
			return fmt.Errorf("existing SQLite database path required: %w", err)
		}
	}
	db, err := gorm.Open(dialect, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return fmt.Errorf("cannot open database")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	who, err := user.Current()
	if err != nil {
		return err
	}
	ctx := model.WithActor(context.Background(), model.Actor{URI: "urn:xolo:operator:" + who.Uid})
	store := adapter.NewStore(db)
	if err := store.Migrate(ctx); err != nil {
		return err
	}
	if *action == "detach" {
		return store.DetachControlPlane(ctx)
	}
	raw, err := store.ExportAdoption(ctx)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(*path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		file.Close()
		if !complete {
			os.Remove(*path)
		}
	}()
	if _, err := file.Write(raw); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	complete = true
	return nil
}
