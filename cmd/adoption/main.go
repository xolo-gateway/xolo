// Command adoption exports and verifies the inventory a control plane adopts
// an instance from, and detaches an instance offline. It never changes a
// resource, an ID, an identity link or the event feed.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"os/user"
	"syscall"

	_ "github.com/ncruces/go-sqlite3/embed"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/adoption"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/setup"
)

const usage = "usage: xolo-adoption export -out file | verify -in file | detach -writers-stopped -operator-access-verified; set XOLO_STORAGE_DATABASE_DSN for export and detach"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "adoption:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	action := args[0]
	if action != "export" && action != "verify" && action != "detach" {
		return fmt.Errorf("unknown action %q; %s", action, usage)
	}
	flags := flag.NewFlagSet(action, flag.ContinueOnError)
	flags.SetOutput(out)
	destination := flags.String("out", "", "new export file (export)")
	source := flags.String("in", "", "export file to verify, - for the standard input (verify)")
	stopped := flags.Bool("writers-stopped", false, "confirm every server, worker and other database writer is stopped (detach)")
	access := flags.Bool("operator-access-verified", false, "confirm a platform administrator sign-in that remains usable was tested (detach)")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 ||
		(action == "export") != (*destination != "") ||
		(action == "verify") != (*source != "") ||
		(action != "detach" && (*stopped || *access)) {
		return errors.New(usage)
	}

	switch action {
	case "verify":
		return verify(*source, in, out)
	case "detach":
		if !*stopped || !*access {
			return errors.New("detach requires -writers-stopped and -operator-access-verified: test a platform administrator sign-in, back up the database, then stop ALL servers, workers and other writers")
		}
	}

	dsn := os.Getenv("XOLO_STORAGE_DATABASE_DSN")
	if dsn == "" {
		return errors.New("XOLO_STORAGE_DATABASE_DSN is required")
	}
	db, err := setup.OpenOfflineDatabase(dsn, action == "export")
	if err != nil {
		return err
	}
	pool, err := db.DB()
	if err != nil {
		return err
	}
	defer pool.Close()
	// Never migrates: the schema must be the one of this binary.
	store := adapter.NewStore(db, adapter.WithAutoMigrate(false))
	if err := store.CheckSchema(ctx); err != nil {
		return fmt.Errorf("check schema: %w", err)
	}

	if action == "export" {
		return export(ctx, store, *destination)
	}
	operator, err := user.Current()
	if err != nil {
		return err
	}
	ctx = model.WithActor(ctx, model.Actor{URI: "urn:xolo:operator:" + operator.Uid, RequestID: model.NewRequestID()})
	report, err := store.DetachControlPlane(ctx)
	if err != nil {
		return err
	}
	return writeJSON(out, report)
}

// export writes a new file, removed when the export fails.
func export(ctx context.Context, store *adapter.Store, path string) (err error) {
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
	if err := adoption.Export(ctx, store, file); err != nil {
		return err
	}
	return file.Sync()
}

func verify(path string, in io.Reader, out io.Writer) error {
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		in = file
	}
	summary, err := adoption.Verify(in)
	if err != nil {
		return err
	}
	return writeJSON(out, summary)
}

func writeJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
