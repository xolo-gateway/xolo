package gorm

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
)

// auditTable is the table of the audits, exported and purged with the scope
// they designate, although it is no inventory table: removing a resource
// keeps its history, until the purge of its scope.
const auditTable = "mutation_audits"

// ReadDeletionScope implements port.LifecycleStore. The deletion and every
// row come from one read-only snapshot, which takes no lock; the rows are
// read one at a time, so the memory used does not grow with the scope.
//
// The transaction is never replayed: its callbacks may already have written
// to a stream. A failure leaves an export without trailer.
func (s *Store) ReadDeletionScope(ctx context.Context, scope model.CommonScope, key string, start func(source string, deletion model.Deletion) error, row func(table string, row json.RawMessage) error) error {
	db, err := s.readDatabase(ctx)
	if err != nil {
		return errors.WithStack(err)
	}
	var opts *sql.TxOptions
	if isPostgres(db) {
		opts = &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	}
	read := func(db *gorm.DB) error {
		record, deletion, err := readDeletionRow(db, scope, key)
		if err != nil {
			return err
		}
		if record.PurgedAt != nil {
			return errors.WithStack(port.ErrNotFound)
		}
		feed, err := provisioningFeed(db)
		if err != nil {
			return err
		}
		if err := start(feed.Source, deletion); err != nil {
			return err
		}
		for _, t := range lifecycleTables {
			if t.unexported {
				continue
			}
			predicate, args := t.scopePredicate(record)
			q := db.Table(t.table+" r").Where(predicate, args...).Order(qualified("r", t.keyColumns()))
			if err := exportRows(q, t.table, t.secret, row); err != nil {
				return err
			}
		}
		predicate, args := auditScope(db, record)
		q := db.Table(auditTable+" r").Where("r.resource <> ?", deletionAuditResource).Where(predicate, args...).Order("r.id")
		return exportRows(q, auditTable, nil, row)
	}
	if s.transactionBound {
		return read(db.WithContext(ctx))
	}
	return errors.WithStack(db.WithContext(ctx).Transaction(read, opts))
}

// exportRows streams the rows of q, without the secret columns. Values are
// normalized so that two reads of the same rows give the same bytes: maps
// marshal with sorted keys, and times are written in UTC.
func exportRows(q *gorm.DB, table string, secret []string, row func(string, json.RawMessage) error) error {
	rows, err := q.Select("r.*").Rows()
	if err != nil {
		return errors.WithStack(err)
	}
	defer rows.Close()
	for rows.Next() {
		values := map[string]any{}
		if err := q.ScanRows(rows, &values); err != nil {
			return errors.WithStack(err)
		}
		for _, column := range secret {
			delete(values, column)
		}
		for column, value := range values {
			switch v := value.(type) {
			case []byte:
				values[column] = string(v)
			case time.Time:
				values[column] = v.UTC().Format(time.RFC3339Nano)
			}
		}
		encoded, err := json.Marshal(values)
		if err != nil {
			return errors.WithStack(err)
		}
		if err := row(table, encoded); err != nil {
			return err
		}
	}
	return errors.WithStack(rows.Err())
}

func qualified(alias string, columns []string) string {
	out := make([]string, len(columns))
	for i, column := range columns {
		out[i] = alias + "." + column
	}
	return strings.Join(out, ", ")
}

// jsonField extracts a top-level string field of a JSON text column.
func jsonField(db *gorm.DB, column, field string) string {
	if isPostgres(db) {
		return "(" + column + "::jsonb ->> '" + field + "')"
	}
	return "json_extract(" + column + ", '$." + field + "')"
}

// auditScope selects, over the audits aliased r, those of the resources of
// the scope of a deletion: their snapshots hold its data. For a member, the
// audits of the operations it made are selected too; its purge anonymizes
// them rather than removing them.
func auditScope(db *gorm.DB, d ResourceDeletion) (string, []any) {
	switch d.Family {
	case model.FamilyTenant:
		return "r.tenant_id = ?", []any{d.ResourceID}
	case model.FamilyOrganization:
		return "(r.org_id = ? OR (r.resource = 'organization' AND r.resource_id = ?))", []any{d.ResourceID, d.ResourceID}
	}
	about, args := memberAudits(db, d)
	return "r.tenant_id = ? AND (" + about + " OR " + jsonField(db, "r.actor", "user_id") + " = ?)",
		append(append([]any{d.TenantID}, args...), d.ResourceID)
}

// memberAudits selects the audits of the resources of a member: its
// account, its memberships, its quota and its personal alerts.
func memberAudits(db *gorm.DB, d ResourceDeletion) (string, []any) {
	field := func(name string) string {
		return "(" + jsonField(db, `r."before"`, name) + " = ? OR " + jsonField(db, `r."after"`, name) + " = ?)"
	}
	personal := "(" + jsonField(db, `r."before"`, "scope") + " = 'personal' OR " + jsonField(db, `r."after"`, "scope") + " = 'personal')"
	id := d.ResourceID
	return "(r.resource_id = ?" +
			" OR (r.resource = 'membership' AND " + field("user_id") + ")" +
			" OR (r.resource = 'quota' AND " + field("scope_id") + ")" +
			" OR (r.resource = 'alert' AND " + field("owner_id") + " AND " + personal + "))",
		[]any{id, id, id, id, id, id, id}
}
