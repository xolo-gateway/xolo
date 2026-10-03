package gorm

import (
	"fmt"
	"log/slog"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const commonMigrationID = "202610020001"

// requireCommonIDs checks the post-migration identifier format.
func requireCommonIDs(db *gorm.DB) error {
	for _, table := range []string{"tenants", "organizations", "users"} {
		if !db.Migrator().HasTable(table) {
			continue
		}
		var ids []string
		if err := db.Table(table).Pluck("id", &ids).Error; err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := model.ParseTenantID(id); err != nil {
				return fmt.Errorf("legacy identifiers remain in %s", table)
			}
		}
	}
	return nil
}
func migrateCommonSchema(tx *gorm.DB) error {
	// The startup migration lock covers planning, application and the migration
	// marker. No privileges are inferred from platform roles: existing accounts
	// remain tenant members until an owner is explicitly assigned.
	slog.InfoContext(tx.Statement.Context, "migrating common identifiers to UUID")
	a, err := PlanCommonRecovery(tx.Statement.Context, tx)
	if err != nil {
		return err
	}
	var memberships []Membership
	if err := tx.Select("id, user_id, org_id").Preload("Roles").Find(&memberships).Error; err != nil {
		return err
	}
	rank := map[string]int{"member": 1, "admin": 2, "owner": 3}
	for _, m := range memberships {
		chosen := ""
		for _, r := range m.Roles {
			if r.Builtin && rank[r.BuiltinKind] > rank[chosen] {
				chosen = r.BuiltinKind
			}
		}
		if chosen != "" {
			a.MembershipRoles[m.ID] = chosen
		}
	}
	_, err = applyCommonRecovery(tx.Statement.Context, tx, a, false)
	return err
}
func installCommonSchema(tx *gorm.DB) error {
	if err := tx.Exec("DROP INDEX IF EXISTS idx_users_tenant_identity").Error; err != nil {
		return err
	}
	if err := tx.AutoMigrate(&User{}, &Membership{}, &Domain{}, &ReservedDomain{}, &PublicationClock{}, &MutationAudit{}, &Publication{}); err != nil {
		return err
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&PublicationClock{ID: 1}).Error
}
