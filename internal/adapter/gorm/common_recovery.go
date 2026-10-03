package gorm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/xid"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"gorm.io/gorm"
)

// RecoveryArtifact records migration decisions, never runtime aliases.
// Optional owner and domain overrides use old IDs.
type RecoveryArtifact struct {
	Version           int                          `json:"version"`
	IDs               map[string]map[string]string `json:"ids"`
	TenantOwners      map[string][]string          `json:"tenant_owners"`
	MembershipRoles   map[string]string            `json:"membership_roles"`
	Domains           []model.Domain               `json:"domains"`
	ReservedHostnames []string                     `json:"reserved_hostnames"`
}
type RecoveryReport struct {
	BeforeCounts map[string]int64 `json:"before_counts,omitempty"`
	Digest       string           `json:"artifact_sha256"`
	Counts       map[string]int64 `json:"counts"`
	Issues       []string         `json:"issues"`
	Applied      bool             `json:"applied"`
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
	refs = append(refs, recoveryReference{"auth_tokens", "owner_id", "users", "", "", false}, recoveryReference{"alerts", "owner_id", "users", "", "", false}, recoveryReference{"invite_tokens", "created_by_user_id", "users", "", "", false}, recoveryReference{"plugin_node_secrets", "org_id", "organizations", "org_id NOT LIKE '~:%'", "", false}, recoveryReference{"plugin_node_secrets", "org_id", "users", "org_id LIKE '~:%'", "~:", false})
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
	ids, err := recoveryIDs(db.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	a := &RecoveryArtifact{Version: 1, IDs: map[string]map[string]string{}, TenantOwners: map[string][]string{}, MembershipRoles: map[string]string{}}
	for family, values := range ids {
		a.IDs[family] = map[string]string{}
		for _, old := range values {
			next := old
			if _, err := model.ParseTenantID(old); err != nil {
				if _, err := xid.FromString(old); err != nil {
					return nil, fmt.Errorf("invalid legacy identifier in %s", family)
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
	if a == nil || a.Version != 1 || len(a.IDs) != 3 {
		return nil, fmt.Errorf("unsupported recovery artifact")
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
	seen := map[string]bool{}
	for family, values := range ids {
		if len(a.IDs[family]) != len(values) {
			report.Issues = append(report.Issues, "incomplete mapping: "+family)
		}
		for _, old := range values {
			next, ok := a.IDs[family][old]
			if !ok {
				report.Issues = append(report.Issues, "missing mapping: "+family)
				continue
			}
			if _, err := model.ParseTenantID(next); err != nil || seen[next] {
				report.Issues = append(report.Issues, "invalid or duplicate UUID: "+family)
			}
			if _, err := model.ParseTenantID(old); err == nil && old != next {
				report.Issues = append(report.Issues, "existing UUID must be preserved: "+family)
			}
			seen[next] = true
		}
	}
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
			q = q.Where(ref.where)
		}
		if err := q.Distinct(ref.column).Pluck(ref.column, &values).Error; err != nil {
			return nil, err
		}
		for _, value := range values {
			old := strings.TrimPrefix(value, ref.prefix)
			if _, ok := a.IDs[ref.family][old]; !ok && !ref.historical {
				report.Issues = append(report.Issues, "orphan: "+ref.table+"."+ref.column)
				break
			}
		}
	}
	var users []User
	if err := db.Select("id, tenant_id, provider, subject, email, active").Find(&users).Error; err != nil {
		return nil, err
	}
	emails := map[string]bool{}
	identities := map[string]bool{}
	byID := map[string]User{}
	for _, u := range users {
		byID[u.ID] = u
		email := model.NormalizeEmail(u.Email)
		key := u.TenantID + "\x00" + email
		if email != "" && emails[key] {
			report.Issues = append(report.Issues, "normalized email collision in tenant "+u.TenantID)
		}
		emails[key] = true
		if (u.Provider == "") != (u.Subject == "") {
			report.Issues = append(report.Issues, "partial provider identity")
		}
		if u.Provider != "" {
			key = u.TenantID + "\x00" + u.Provider + "\x00" + u.Subject
			if identities[key] {
				report.Issues = append(report.Issues, "duplicate provider identity")
			}
			identities[key] = true
		}
	}
	for tenant := range a.TenantOwners {
		if _, ok := a.IDs["tenants"][tenant]; !ok {
			report.Issues = append(report.Issues, "unknown tenant owner decision")
		}
	}
	for _, tenant := range ids["tenants"] {
		owners := a.TenantOwners[tenant]
		for _, owner := range owners {
			u, ok := byID[owner]
			if !ok || u.TenantID != tenant || !u.Active {
				report.Issues = append(report.Issues, "invalid tenant owner: "+tenant)
			}
		}
	}
	var memberships []Membership
	if err := db.Select("id, user_id, org_id").Preload("Roles").Preload("Org").Find(&memberships).Error; err != nil {
		return nil, err
	}
	pairs := map[string]bool{}
	for _, m := range memberships {
		key := m.OrgID + "\x00" + m.UserID
		if pairs[key] {
			report.Issues = append(report.Issues, "duplicate membership")
		}
		pairs[key] = true
		if m.Org == nil || byID[m.UserID].TenantID != m.Org.TenantID {
			report.Issues = append(report.Issues, "foreign membership parent")
		}
		kinds := map[string]bool{}
		for _, r := range m.Roles {
			if r.OrgID != m.OrgID {
				report.Issues = append(report.Issues, "foreign membership role")
			}
			if r.Builtin {
				kinds[r.BuiltinKind] = true
			}
		}
		decision := a.MembershipRoles[m.ID]
		if len(kinds) > 1 && decision == "" {
			report.Issues = append(report.Issues, "ambiguous membership role: "+m.ID)
		}
		if decision != "" && (!model.MembershipRole(decision).Valid() || (len(kinds) > 0 && !kinds[decision]) || (len(kinds) == 0 && decision != "member")) {
			report.Issues = append(report.Issues, "invalid membership role decision")
		}
	}
	membershipIDs := map[string]bool{}
	for _, m := range memberships {
		membershipIDs[m.ID] = true
	}
	for id := range a.MembershipRoles {
		if !membershipIDs[id] {
			report.Issues = append(report.Issues, "unknown membership role decision")
		}
	}
	hosts := map[string]bool{}
	reserved := map[string]bool{}
	for _, h := range a.ReservedHostnames {
		n, e := model.NormalizeHostname(h)
		if e != nil {
			return nil, e
		}
		reserved[n] = true
	}
	for _, d := range a.Domains {
		h, e := model.NormalizeHostname(d.Hostname)
		if e != nil || h != d.Hostname || hosts[h] || reserved[h] || !d.Status.Valid() || a.IDs["tenants"][string(d.TenantID)] == "" {
			report.Issues = append(report.Issues, "invalid, reserved or duplicate domain")
		}
		hosts[h] = true
	}
	// Plugin settings and alert expressions are opaque application input. Refuse
	// unresolved legacy references rather than replace arbitrary text.
	for _, spec := range [][2]string{{"alerts", "query"}, {"virtual_models", "graph_json"}, {"personal_virtual_models", "graph_json"}, {"middlewares", "graph_json"}} {
		if !db.Migrator().HasTable(spec[0]) || !db.Migrator().HasColumn(spec[0], spec[1]) {
			continue
		}
		var values []string
		if err := db.Table(spec[0]).Pluck(spec[1], &values).Error; err != nil {
			return nil, err
		}
		for _, value := range values {
			found := false
			for _, mapping := range a.IDs {
				for old, next := range mapping {
					if old != next && strings.Contains(value, old) {
						found = true
					}
				}
			}
			if found {
				report.Issues = append(report.Issues, "manual serialized reference review required: "+spec[0]+"."+spec[1])
				break
			}
		}
	}
	return report, nil
}
func artifactDigest(a *RecoveryArtifact) string {
	b, _ := json.Marshal(a)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ApplyCommonRecovery requires all writers stopped. DDL, rewritten references,
// semantic backfill and checkpoint commit together on both supported backends.
func ApplyCommonRecovery(ctx context.Context, db *gorm.DB, a *RecoveryArtifact) (*RecoveryReport, error) {
	return applyCommonRecovery(ctx, db, a, true)
}

func applyCommonRecovery(ctx context.Context, db *gorm.DB, a *RecoveryArtifact, markMigration bool) (*RecoveryReport, error) {
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
		// Do not invoke stores: historical resources must generate no creation facts.
		for _, ref := range recoveryReferences() {
			if !tx.Migrator().HasTable(ref.table) || !tx.Migrator().HasColumn(ref.table, ref.column) {
				continue
			}
			for old, next := range a.IDs[ref.family] {
				if old == next {
					continue
				}
				q := tx.Table(ref.table).Where(ref.column+" = ?", ref.prefix+old)
				if ref.where != "" {
					q = q.Where(ref.where)
				}
				if err := q.UpdateColumn(ref.column, ref.prefix+next).Error; err != nil {
					return err
				}
			}
		}
		for _, family := range []string{"tenants", "organizations", "users"} {
			for old, next := range a.IDs[family] {
				if old != next {
					if err := tx.Table(family).Where("id = ?", old).UpdateColumn("id", next).Error; err != nil {
						return err
					}
				}
			}
		}
		// Normalize before installing the unique index; diagnostics already refused
		// collisions and never choose a survivor automatically.
		var users []User
		if err := tx.Select("id, tenant_id, provider, subject, email, active").Find(&users).Error; err != nil {
			return err
		}
		for _, u := range users {
			if err := tx.Table("users").Where("id = ?", u.ID).UpdateColumn("email", model.NormalizeEmail(u.Email)).Error; err != nil {
				return err
			}
		}
		if isPostgres(tx) {
			if err := tx.Exec("SET CONSTRAINTS ALL IMMEDIATE").Error; err != nil {
				return err
			}
		}
		if err := installCommonSchema(tx); err != nil {
			return err
		}
		for _, owners := range a.TenantOwners {
			for _, old := range owners {
				if err := tx.Model(&User{}).Where("id = ?", a.IDs["users"][old]).UpdateColumn("tenant_role", "owner").Error; err != nil {
					return err
				}
			}
		}
		var memberships []Membership
		if err := tx.Select("id, user_id, org_id").Preload("Roles").Find(&memberships).Error; err != nil {
			return err
		}
		for _, m := range memberships {
			role := "member"
			for _, r := range m.Roles {
				if r.Builtin {
					role = r.BuiltinKind
				}
			}
			if chosen := a.MembershipRoles[m.ID]; chosen != "" {
				role = chosen
			}
			if err := tx.Model(&Membership{}).Where("id = ?", m.ID).Updates(map[string]any{"common_role": role, "status": "active"}).Error; err != nil {
				return err
			}
			// An explicit decision replaces only builtin assignments.
			if markMigration && a.MembershipRoles[m.ID] != "" {
				for _, r := range m.Roles {
					if r.Builtin && r.BuiltinKind != role {
						if err := tx.Where("membership_id = ? AND role_id = ?", m.ID, r.ID).Delete(&MembershipRole{}).Error; err != nil {
							return err
						}
					}
				}
			}
		}
		for _, h := range a.ReservedHostnames {
			n, _ := model.NormalizeHostname(h)
			if err := tx.Create(&ReservedDomain{Hostname: n}).Error; err != nil {
				return err
			}
		}
		for _, d := range a.Domains {
			if err := tx.Create(&Domain{Hostname: d.Hostname, TenantID: a.IDs["tenants"][string(d.TenantID)], Status: string(d.Status)}).Error; err != nil {
				return err
			}
		}
		if err := checkRecoveredReferences(tx, a); err != nil {
			return err
		}
		if err := requireCommonIDs(tx); err != nil {
			return err
		}
		for table, count := range report.Counts {
			var after int64
			if err := tx.Table(table).Count(&after).Error; err != nil {
				return err
			}
			if table == "reserved_domains" {
				count += int64(len(a.ReservedHostnames))
			}
			if table == "domains" {
				count += int64(len(a.Domains))
			}
			if after != count && table != "membership_roles" {
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
		if markMigration {
			if err := tx.Exec("INSERT INTO migrations (id) VALUES (?)", commonMigrationID).Error; err != nil {
				return err
			}
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

// Check the declared reference inventory, including non-FK and personal scopes.
func checkRecoveredReferences(db *gorm.DB, a *RecoveryArtifact) error {
	for _, ref := range recoveryReferences() {
		if !db.Migrator().HasTable(ref.table) || !db.Migrator().HasColumn(ref.table, ref.column) {
			continue
		}
		for old, next := range a.IDs[ref.family] {
			if old == next {
				continue
			}
			var count int64
			q := db.Table(ref.table).Where(ref.column+" = ?", ref.prefix+old)
			if ref.where != "" {
				q = q.Where(ref.where)
			}
			if err := q.Count(&count).Error; err != nil {
				return err
			}
			if count != 0 {
				return fmt.Errorf("legacy reference remains in %s.%s", ref.table, ref.column)
			}
		}
	}
	return nil
}
