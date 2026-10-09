package gorm

import (
	"context"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	"gorm.io/gorm"
)

// lifecycleGuardsLock serializes the replicas installing or removing the
// guards at startup.
const lifecycleGuardsLock = 867530904

// pgFrozenGuard refuses a write to a frozen scope. It takes no instance-wide
// lock: it locks the rows of the tenant, organization and member of the
// written row FOR KEY SHARE, like a foreign key check does, and a freeze
// locks the row it freezes FOR UPDATE. A write therefore waits for a freeze
// of its own scope only, then sees it: at read committed, the next statement
// of this volatile function takes a new snapshot; at repeatable read and
// serializable, the lock of a row the freeze changed fails the transaction,
// which is replayed.
const pgFrozenGuard = `CREATE OR REPLACE FUNCTION xolo_frozen_guard(t text, o text, m text) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
	IF t IS NULL AND o IS NOT NULL THEN SELECT tenant_id INTO t FROM organizations WHERE id = o; END IF;
	IF t IS NULL AND m IS NOT NULL THEN SELECT tenant_id INTO t FROM users WHERE id = m; END IF;
	IF t IS NOT NULL THEN PERFORM 1 FROM tenants WHERE id = t FOR KEY SHARE; END IF;
	IF o IS NOT NULL THEN PERFORM 1 FROM organizations WHERE id = o FOR KEY SHARE; END IF;
	IF m IS NOT NULL THEN PERFORM 1 FROM users WHERE id = m FOR KEY SHARE; END IF;
	IF EXISTS (SELECT 1 FROM resource_deletions d WHERE (d.family = 'tenant' AND d.resource_id = t)
		OR (d.family = 'organization' AND d.resource_id = o) OR (d.family = 'member' AND d.resource_id = m)) THEN
		RAISE EXCEPTION '` + frozenGuardMessage + `' USING ERRCODE = '` + frozenGuardCode + `';
	END IF;
END $$`

// LifecycleGuardsInstalled reports whether the guards are installed.
func (s *Store) LifecycleGuardsInstalled(ctx context.Context) (bool, error) {
	db, err := s.getDatabase(ctx)
	if err != nil {
		return false, errors.WithStack(err)
	}
	var control LifecycleControl
	if err := db.WithContext(ctx).First(&control, lifecycleControlID).Error; err != nil {
		return false, errors.WithStack(err)
	}
	return control.GuardsInstalled, nil
}

// PrepareLifecycle installs the database guards of the frozen scopes when the
// lifecycle is enabled. Disabled, it removes them unless a deletion is
// recorded: an instance that never froze anything pays no trigger, and the
// freezes already recorded stay protected. It runs at startup, after the
// schema migration, on every replica.
func (s *Store) PrepareLifecycle(ctx context.Context, enabled bool) error {
	db, err := s.getDatabase(ctx)
	if err != nil {
		return errors.WithStack(err)
	}
	return errors.WithStack(db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if isPostgres(tx) {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", lifecycleGuardsLock).Error; err != nil {
				return err
			}
		}
		var control LifecycleControl
		if err := tx.First(&control, lifecycleControlID).Error; err != nil {
			return err
		}
		install := enabled
		if !enabled && control.GuardsInstalled {
			var deletions int64
			if err := tx.Model(&ResourceDeletion{}).Count(&deletions).Error; err != nil {
				return err
			}
			install = deletions > 0
		}
		if install == control.GuardsInstalled && !install {
			return nil
		}
		if install {
			if err := installLifecycleGuards(tx); err != nil {
				return err
			}
		} else if err := uninstallLifecycleGuards(tx); err != nil {
			return err
		}
		return tx.Model(&LifecycleControl{}).Where("id = ?", lifecycleControlID).Update("guards_installed", install).Error
	}))
}

// installLifecycleGuards installs, or replaces, one guard per guarded table
// of the inventory. On PostgreSQL, the trigger function carries the
// expressions of the inventory and is replaced on every call; the trigger
// itself never changes, and is only created when missing: CREATE OR REPLACE
// TRIGGER needs PostgreSQL 14, and dropping it would lock the table ACCESS
// EXCLUSIVE at every startup.
func installLifecycleGuards(db *gorm.DB) error {
	if isPostgres(db) {
		if err := db.Exec(pgFrozenGuard).Error; err != nil {
			return err
		}
	}
	for _, t := range lifecycleTables {
		if t.unguarded || !db.Migrator().HasTable(t.table) {
			continue
		}
		if isPostgres(db) {
			call := func(alias string) string {
				return fmt.Sprintf("PERFORM xolo_frozen_guard(%s, %s, %s);", t.expression(t.tenant, alias), t.expression(t.org, alias), t.expression(t.member, alias))
			}
			function := "xolo_guard_" + t.table
			body := `CREATE OR REPLACE FUNCTION ` + function + `() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	IF TG_OP <> 'INSERT' THEN ` + call("OLD") + ` END IF;
	IF TG_OP <> 'DELETE' THEN ` + call("NEW") + ` END IF;
	IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
	RETURN NEW;
END $$`
			if err := db.Exec(body).Error; err != nil {
				return err
			}
			var exists bool
			if err := db.Raw("SELECT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'xolo_lifecycle_guard' AND tgrelid = to_regclass(?))", t.table).Scan(&exists).Error; err != nil {
				return err
			}
			if !exists {
				if err := db.Exec("CREATE TRIGGER xolo_lifecycle_guard BEFORE INSERT OR UPDATE OR DELETE ON " + t.table + " FOR EACH ROW EXECUTE FUNCTION " + function + "()").Error; err != nil {
					return err
				}
			}
			continue
		}
		for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
			var predicates []string
			if op != "INSERT" {
				predicates = append(predicates, t.frozenPredicate("OLD"))
			}
			if op != "DELETE" {
				predicates = append(predicates, t.frozenPredicate("NEW"))
			}
			// SQLite triggers carry their predicate: replaced, they follow
			// the inventory.
			name := sqliteGuardName(t.table, op)
			for _, statement := range []string{
				"DROP TRIGGER IF EXISTS " + name,
				fmt.Sprintf("CREATE TRIGGER %s BEFORE %s ON %s WHEN %s BEGIN SELECT RAISE(ABORT, '%s'); END",
					name, op, t.table, strings.Join(predicates, " OR "), frozenGuardMessage),
			} {
				if err := db.Exec(statement).Error; err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func uninstallLifecycleGuards(db *gorm.DB) error {
	for _, t := range lifecycleTables {
		if t.unguarded || !db.Migrator().HasTable(t.table) {
			continue
		}
		var statements []string
		if isPostgres(db) {
			statements = []string{
				"DROP TRIGGER IF EXISTS xolo_lifecycle_guard ON " + t.table,
				"DROP FUNCTION IF EXISTS xolo_guard_" + t.table + "()",
			}
		} else {
			for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
				statements = append(statements, "DROP TRIGGER IF EXISTS "+sqliteGuardName(t.table, op))
			}
		}
		for _, statement := range statements {
			if err := db.Exec(statement).Error; err != nil {
				return err
			}
		}
	}
	if isPostgres(db) {
		return db.Exec("DROP FUNCTION IF EXISTS xolo_frozen_guard(text, text, text)").Error
	}
	return nil
}

func sqliteGuardName(table, op string) string {
	return "xolo_lifecycle_" + table + "_" + strings.ToLower(op)
}
