package gorm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"gorm.io/gorm"
)

// RecoveryArtifact records UUID mappings and explicit serialized corrections.
// It never changes emails, identities or role assignments.
type RecoveryArtifact struct {
	Version             int                          `json:"version"`
	SerializedOverrides []RecoverySerializedOverride `json:"serialized_overrides,omitempty"`
	IDs                 map[string]map[string]string `json:"ids"`
}

const recoveryArtifactVersion = 2

func recoveryVersionError(version int) error {
	return fmt.Errorf("unsupported recovery plan version %d; regenerate with xolo-migrate plan -out <new-file>; do not edit the version by hand", version)
}

// UnmarshalJSON checks the version before decoding decisions. Old decisions
// must never disappear silently when reading an artifact from an earlier binary.
func (a *RecoveryArtifact) UnmarshalJSON(data []byte) error {
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return err
	}
	if header.Version != recoveryArtifactVersion {
		return recoveryVersionError(header.Version)
	}
	type artifact RecoveryArtifact
	var decoded artifact
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*a = RecoveryArtifact(decoded)
	return nil
}

type RecoveryReport struct {
	BeforeCounts map[string]int64 `json:"before_counts,omitempty"`
	Digest       string           `json:"artifact_sha256"`
	Counts       map[string]int64 `json:"counts"`
	Issues       []string         `json:"issues"`
	Applied      bool             `json:"applied"`
	// Notices are informational; only Issues prevent application.
	Notices []string `json:"notices,omitempty"`
}
type recoveryFK struct {
	TableName         string
	ConstraintName    string
	Deferrable        bool
	InitiallyDeferred bool
}

type CommonRecovery struct {
	ID       int `gorm:"primaryKey;autoIncrement:false"`
	Digest   string
	Artifact string
}
type recoveryReference struct {
	table, column, family, where, prefix string
	historical                           bool
}

func recoveryReferences() []recoveryReference {
	refs := []recoveryReference{
		{"organizations", "tenant_id", "tenants", "", "", false}, {"users", "tenant_id", "tenants", "", "", false},
	}
	for _, t := range []string{"memberships", "applications", "roles", "providers", "llm_models", "virtual_models", "middlewares", "auth_tokens", "invite_tokens", "alerts", "alert_incidents", "usage_records", "events", "event_settings", "quota_usages"} {
		refs = append(refs, recoveryReference{t, "org_id", "organizations", "", "", t == "events" || t == "usage_records" || t == "quota_usages"})
	}
	for _, t := range []string{"memberships", "user_roles", "user_preferences", "personal_virtual_models", "usage_records", "events"} {
		refs = append(refs, recoveryReference{t, "user_id", "users", "", "", t == "events" || t == "usage_records"})
	}
	refs = append(refs,
		recoveryReference{table: "auth_tokens", column: "owner_id", family: "users"},
		// Normal deletion retains organization alerts, invitations and plugin
		// secrets. Empty/NULL alert scopes predate personal alerts and mean org.
		recoveryReference{table: "alerts", column: "owner_id", family: "users",
			where: "scope = 'org' OR scope = '' OR scope IS NULL", historical: true},
		recoveryReference{table: "alerts", column: "owner_id", family: "users",
			where: "scope IS NOT NULL AND scope NOT IN ('org', '')"},
		recoveryReference{table: "invite_tokens", column: "created_by_user_id", family: "users", historical: true},
		recoveryReference{table: "plugin_node_secrets", column: "org_id", family: "organizations",
			where: "org_id NOT LIKE '~:%'"},
		recoveryReference{table: "plugin_node_secrets", column: "org_id", family: "users",
			where: "org_id LIKE '~:%'", prefix: "~:", historical: true},
		recoveryReference{table: "plugin_node_secrets", column: "key", family: "users",
			where: "plugin_name = 'mcp-bridge' AND key LIKE 'oauth:%'", prefix: "oauth:", historical: true},
	)
	for _, t := range []string{"quota", "quota_usages"} {
		for scope, family := range map[string]string{"org": "organizations", "user": "users"} {
			refs = append(refs, recoveryReference{t, "scope_id", family, "scope = '" + scope + "'", "", t == "quota_usages"})
		}
	}
	return refs
}
func recoveryIDs(db *gorm.DB) (map[string][]string, error) {
	result := map[string][]string{}
	for _, table := range []string{"tenants", "organizations", "users"} {
		var ids []string
		if !db.Migrator().HasTable(table) {
			return nil, fmt.Errorf("missing %s: first upgrade with the previous Xolo release", table)
		}
		if err := db.Table(table).Order("id").Pluck("id", &ids).Error; err != nil {
			return nil, err
		}
		result[table] = ids
	}
	return result, nil
}

// PlanCommonRecovery is read-only. Save its result before editing decisions;
// repeated apply attempts must reuse that exact artifact.
func PlanCommonRecovery(ctx context.Context, db *gorm.DB) (*RecoveryArtifact, error) {
	db = db.WithContext(ctx)
	if db.Migrator().HasTable(&CommonRecovery{}) {
		var checkpoint CommonRecovery
		err := db.First(&checkpoint, 1).Error
		if err == nil {
			var artifact RecoveryArtifact
			if err := json.Unmarshal([]byte(checkpoint.Artifact), &artifact); err != nil {
				return nil, err
			}
			return &artifact, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	ids, err := recoveryIDs(db)
	if err != nil {
		return nil, err
	}
	a := &RecoveryArtifact{Version: recoveryArtifactVersion, IDs: map[string]map[string]string{}}
	for family, values := range ids {
		a.IDs[family] = map[string]string{}
		for _, old := range values {
			next := old
			if _, err := model.ParseTenantID(old); err != nil {
				if old == "" {
					return nil, fmt.Errorf("empty legacy identifier in %s: repair the primary key and its references before planning", family)
				}
				next = uuid.NewString()
			}
			a.IDs[family][old] = next
		}
	}
	return a, nil
}

// DiagnoseCommonRecovery never creates tables, migrates or mutates rows.
func DiagnoseCommonRecovery(ctx context.Context, db *gorm.DB, a *RecoveryArtifact) (*RecoveryReport, error) {
	db = db.WithContext(ctx)
	report := &RecoveryReport{Digest: artifactDigest(a), Counts: map[string]int64{}, Issues: []string{}}
	if a == nil {
		return nil, fmt.Errorf("missing recovery artifact")
	}
	if a.Version != recoveryArtifactVersion {
		return nil, recoveryVersionError(a.Version)
	}
	if db.Migrator().HasTable(&CommonRecovery{}) {
		var checkpoint CommonRecovery
		checkpointErr := db.First(&checkpoint, 1).Error
		if checkpointErr != nil && !errors.Is(checkpointErr, gorm.ErrRecordNotFound) {
			return nil, checkpointErr
		}
		if checkpointErr == nil {
			if checkpoint.Digest != artifactDigest(a) {
				return nil, fmt.Errorf("database migrated with a different artifact")
			}
			if err := requireCommonIDs(db); err != nil {
				return nil, err
			}
			if err := checkRecoveredReferences(db, a); err != nil {
				return nil, err
			}
			tables, err := db.Migrator().GetTables()
			if err != nil {
				return nil, err
			}
			for _, table := range tables {
				var count int64
				if err := db.Table(table).Count(&count).Error; err != nil {
					return nil, err
				}
				report.Counts[table] = count
			}
			report.Applied = true
			return report, nil
		}
	}
	ids, err := recoveryIDs(db)
	if err != nil {
		return nil, err
	}
	diagnostics := recoveryDiagnostics{}
	diagnoseRecoveryMappings(ids, a, diagnostics)
	tables, err := db.Migrator().GetTables()
	if err != nil {
		return nil, err
	}
	sort.Strings(tables)
	for _, table := range tables {
		var count int64
		if err := db.Table(table).Count(&count).Error; err != nil {
			return nil, err
		}
		report.Counts[table] = count
	}
	for _, ref := range recoveryReferences() {
		if !db.Migrator().HasTable(ref.table) || !db.Migrator().HasColumn(ref.table, ref.column) {
			continue
		}
		var values []string
		q := db.Table(ref.table).Where(ref.column + " IS NOT NULL AND " + ref.column + " <> ''")
		if ref.where != "" {
			q = q.Where("(" + ref.where + ")")
		}
		if err := q.Distinct(ref.column).Pluck(ref.column, &values).Error; err != nil {
			return nil, err
		}
		for _, value := range values {
			old := strings.TrimPrefix(value, ref.prefix)
			if _, ok := a.IDs[ref.family][old]; !ok && !ref.historical {
				diagnostics.add("orphan", ref.location(), value, "")
			}
		}
	}
	issues, notices, err := scanSerializedRecovery(db, a, nil)
	if err != nil {
		return nil, err
	}
	report.Issues = append(diagnostics.issues(), issues...)
	report.Notices = notices
	sort.Strings(report.Issues)
	return report, nil
}
func artifactDigest(a *RecoveryArtifact) string {
	b, _ := json.Marshal(a)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func applyCommonRecovery(ctx context.Context, db *gorm.DB, a *RecoveryArtifact) (*RecoveryReport, error) {
	var final *RecoveryReport
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if isPostgres(tx) {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(867530902)").Error; err != nil {
				return err
			}
		}
		report, err := DiagnoseCommonRecovery(ctx, tx, a)
		if err != nil {
			return err
		}
		final = report
		if report.Applied {
			return nil
		}
		report.BeforeCounts = maps.Clone(report.Counts)
		if len(report.Issues) > 0 {
			return fmt.Errorf("common migration blocked: %s", strings.Join(report.Issues, "; "))
		}
		for _, notice := range report.Notices {
			slog.InfoContext(
				ctx,
				"UUID recovery will preserve an unrecognized event attribute",
				"diagnostic", notice,
			)
		}
		var constraints []recoveryFK
		if isSQLite(tx) {
			if err := tx.Exec("PRAGMA defer_foreign_keys = ON").Error; err != nil {
				return err
			}
		} else {

			if err := tx.Raw(`SELECT c.conrelid::regclass::text AS table_name,c.conname AS constraint_name,c.condeferrable AS deferrable,c.condeferred AS initially_deferred FROM pg_constraint c JOIN pg_namespace n ON n.oid=c.connamespace WHERE c.contype='f' AND n.nspname=current_schema()`).Scan(&constraints).Error; err != nil {
				return err
			}
			for _, c := range constraints {
				if err := tx.Exec("ALTER TABLE " + tx.Statement.Quote(c.TableName) + " ALTER CONSTRAINT " + tx.Statement.Quote(c.ConstraintName) + " DEFERRABLE INITIALLY IMMEDIATE").Error; err != nil {
					return err
				}
			}
			if err := tx.Exec("SET CONSTRAINTS ALL DEFERRED").Error; err != nil {
				return err
			}
			// Original constraint modes are restored before committing.

		}
		if err := installRecoveryMappings(tx, a); err != nil {
			return err
		}
		issues, _, err := scanSerializedRecovery(tx, a, func(changes []RecoverySerializedOverride) error {
			return applySerializedRecoveryBatch(tx, changes)
		})
		if err != nil {
			return err
		}
		if len(issues) != 0 {
			return fmt.Errorf("serialized recovery blocked: %s", strings.Join(issues, "; "))
		}
		// Raw SQL preserves timestamps and emits no runtime creation events.
		for _, ref := range recoveryReferences() {
			if !tx.Migrator().HasTable(ref.table) || !tx.Migrator().HasColumn(ref.table, ref.column) {
				continue
			}
			if err := rewriteRecoveryReference(tx, ref); err != nil {
				return err
			}
		}
		for _, family := range []string{"tenants", "organizations", "users"} {
			if err := rewriteRecoveryReference(tx, recoveryReference{table: family, column: "id", family: family}); err != nil {
				return err
			}
		}
		if err := checkRecoveredReferences(tx, a); err != nil {
			return err
		}
		for _, table := range []string{"uuid_recovery_changes", "uuid_recovery_ids"} {
			if err := tx.Exec("DROP TABLE " + recoveryTempTable(tx, table)).Error; err != nil {
				return err
			}
		}
		if err := requireCommonIDs(tx); err != nil {
			return err
		}
		for table, count := range report.Counts {
			var after int64
			if err := tx.Table(table).Count(&after).Error; err != nil {
				return err
			}
			if after != count {
				return fmt.Errorf("row count changed: %s", table)
			}
		}
		if isSQLite(tx) {
			var violations []map[string]any
			if err := tx.Raw("PRAGMA foreign_key_check").Scan(&violations).Error; err != nil {
				return err
			}
			if len(violations) > 0 {
				return fmt.Errorf("foreign key check failed")
			}
		} else {
			if err := tx.Exec("SET CONSTRAINTS ALL IMMEDIATE").Error; err != nil {
				return err
			}
		}
		for _, c := range constraints {
			mode := "NOT DEFERRABLE INITIALLY IMMEDIATE"
			if c.Deferrable {
				mode = "DEFERRABLE INITIALLY IMMEDIATE"
				if c.InitiallyDeferred {
					mode = "DEFERRABLE INITIALLY DEFERRED"
				}
			}
			if err := tx.Exec("ALTER TABLE " + tx.Statement.Quote(c.TableName) + " ALTER CONSTRAINT " + tx.Statement.Quote(c.ConstraintName) + " " + mode).Error; err != nil {
				return err
			}
		}
		if err := tx.AutoMigrate(&CommonRecovery{}); err != nil {
			return err
		}
		artifactJSON, err := json.Marshal(a)
		if err != nil {
			return err
		}
		if err := tx.Create(&CommonRecovery{ID: 1, Digest: artifactDigest(a), Artifact: string(artifactJSON)}).Error; err != nil {
			return err
		}
		tables, err := tx.Migrator().GetTables()
		if err != nil {
			return err
		}
		final.Counts = map[string]int64{}
		for _, table := range tables {
			var count int64
			if err := tx.Table(table).Count(&count).Error; err != nil {
				return err
			}
			final.Counts[table] = count
		}
		return nil
	})
	if final != nil {
		final.Applied = err == nil
	}
	return final, err
}

// Check the reference inventory with one set query per column. JSON-derived
// mappings also work in diagnose's read-only transaction after a checkpoint.
func checkRecoveredReferences(db *gorm.DB, a *RecoveryArtifact) error {
	for _, ref := range recoveryReferences() {
		if !db.Migrator().HasTable(ref.table) || !db.Migrator().HasColumn(ref.table, ref.column) {
			continue
		}
		encoded, err := json.Marshal(a.IDs[ref.family])
		if err != nil {
			return err
		}
		source := "json_each(?)"
		if isPostgres(db) {
			source = "jsonb_each_text(CAST(? AS jsonb))"
		}
		mapping := db.Raw("SELECT ? || key FROM "+source+" WHERE key <> value", ref.prefix, string(encoded))
		q := db.Table(ref.table).Where(db.Statement.Quote(ref.column)+" IN (?)", mapping)
		if ref.where != "" {
			q = q.Where("(" + ref.where + ")")
		}
		var count int64
		if err := db.Table("(?) AS remaining", q.Select("1").Limit(1)).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("legacy reference remains in %s.%s", ref.table, ref.column)
		}
	}
	return nil
}
