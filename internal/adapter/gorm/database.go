package gorm

import (
	"context"
	"sync"

	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/pkg/errors"
	"gorm.io/gorm"
)

// withoutForeignKeys runs fn with foreign key enforcement disabled on SQLite,
// where AutoMigrate rebuilds tables through INSERT...SELECT into a temporary
// table and would otherwise trip FK checks on legacy rows. Other backends
// migrate columns in place and need no such workaround, so fn runs as-is.
func withoutForeignKeys(tx *gorm.DB, fn func() error) error {
	if !isSQLite(tx) {
		return fn()
	}

	if err := tx.Exec("PRAGMA foreign_keys=off").Error; err != nil {
		return errors.WithStack(err)
	}

	if err := fn(); err != nil {
		return errors.WithStack(err)
	}

	if err := tx.Exec("PRAGMA foreign_keys=on").Error; err != nil {
		return errors.WithStack(err)
	}

	return nil
}

func createGetDatabase(db *gorm.DB) func(ctx context.Context) (*gorm.DB, error) {
	var (
		migrateOnce sync.Once
		migrateErr  error
	)

	return func(ctx context.Context) (*gorm.DB, error) {
		migrateOnce.Do(func() {
			m := gormigrate.New(db, gormigrate.DefaultOptions, []*gormigrate.Migration{
				{
					// Add GraphJSON column to virtual_models and drop plugin tables.
					ID: "202602010001",
					Migrate: func(tx *gorm.DB) error {
						if err := tx.Migrator().DropTable("plugin_activations", "plugin_configs"); err != nil {
							// Ignore if tables don't exist (fresh install).
							_ = err
						}
						return tx.AutoMigrate(&VirtualModel{})
					},
					Rollback: func(tx *gorm.DB) error {
						return tx.Migrator().DropColumn(&VirtualModel{}, "graph_json")
					},
				},
				{
					// Add user_virtual_models table for personal virtual models.
					ID: "202506040001",
					Migrate: func(tx *gorm.DB) error {
						return tx.AutoMigrate(&PersonalVirtualModel{})
					},
					Rollback: func(tx *gorm.DB) error {
						return tx.Migrator().DropTable("personal_virtual_models")
					},
				},
				{
					// Add cached token columns for prompt caching support.
					ID: "202606080001",
					Migrate: func(tx *gorm.DB) error {
						return tx.AutoMigrate(&LLMModel{}, &UsageRecord{})
					},
					Rollback: func(tx *gorm.DB) error {
						if err := tx.Migrator().DropColumn(&LLMModel{}, "cached_prompt_cost_per1_k_tokens"); err != nil {
							return err
						}
						return tx.Migrator().DropColumn(&UsageRecord{}, "cached_tokens")
					},
				},
				{
					// Add plugin_node_secrets table backing the GetSecret/SetSecret
					// host service RPCs (per-node-instance encrypted key/value store).
					ID: "202606180001",
					Migrate: func(tx *gorm.DB) error {
						return tx.AutoMigrate(&PluginNodeSecret{})
					},
					Rollback: func(tx *gorm.DB) error {
						return tx.Migrator().DropTable("plugin_node_secrets")
					},
				},
				{
					// Introduce org-scoped RBAC: roles, role permissions, role model
					// grants and the membership<->role join table. Migrate the legacy
					// single Membership.Role to builtin role assignments, then drop the
					// deprecated column.
					ID: "202606250001",
					Migrate: func(tx *gorm.DB) error {
						return withoutForeignKeys(tx, func() error {
							if err := tx.SetupJoinTable(&Membership{}, "Roles", &MembershipRole{}); err != nil {
								return errors.WithStack(err)
							}
							if err := tx.AutoMigrate(&Role{}, &RolePermission{}, &RoleModel{}, &MembershipRole{}); err != nil {
								return errors.WithStack(err)
							}
							if err := migrateLegacyMembershipRoles(tx); err != nil {
								return errors.WithStack(err)
							}
							if tx.Migrator().HasColumn(&Membership{}, "role") {
								if err := tx.Migrator().DropColumn(&Membership{}, "role"); err != nil {
									return errors.WithStack(err)
								}
							}
							return nil
						})
					},
					Rollback: func(tx *gorm.DB) error {
						return tx.Migrator().DropTable("membership_roles", "role_models", "role_permissions", "roles")
					},
				},
				{
					// Add subscription billing support: billing_mode + subscription_plan on
					// providers, plan_covered + provider_cost on usage records.
					ID: "202506290001",
					Migrate: func(tx *gorm.DB) error {
						return tx.AutoMigrate(&Provider{}, &UsageRecord{})
					},
					Rollback: func(tx *gorm.DB) error {
						if err := tx.Migrator().DropColumn(&Provider{}, "billing_mode"); err != nil {
							return err
						}
						if err := tx.Migrator().DropColumn(&Provider{}, "subscription_plan"); err != nil {
							return err
						}
						if err := tx.Migrator().DropColumn(&UsageRecord{}, "plan_covered"); err != nil {
							return err
						}
						return tx.Migrator().DropColumn(&UsageRecord{}, "provider_cost")
					},
				},
				{
					// Add cost_source column to usage_records to track whether the
					// cost was reported by the provider or computed from the tariff.
					ID: "202606290001",
					Migrate: func(tx *gorm.DB) error {
						return tx.AutoMigrate(&UsageRecord{})
					},
					Rollback: func(tx *gorm.DB) error {
						return tx.Migrator().DropColumn(&UsageRecord{}, "cost_source")
					},
				},
				{
					// Add middlewares table (pipelines applied dynamically to
					// an org's models).
					ID: "202607010001",
					Migrate: func(tx *gorm.DB) error {
						return tx.AutoMigrate(&Middleware{})
					},
					Rollback: func(tx *gorm.DB) error {
						return tx.Migrator().DropTable("middlewares")
					},
				},
				{
					// Add the event system: events (ring buffer), alerts (ruler
					// rules), alert incidents and per-org event settings.
					ID: "202607050001",
					Migrate: func(tx *gorm.DB) error {
						return tx.AutoMigrate(&Event{}, &Alert{}, &AlertIncident{}, &EventSettings{})
					},
					Rollback: func(tx *gorm.DB) error {
						return tx.Migrator().DropTable("events", "alerts", "alert_incidents", "event_settings")
					},
				},
				{
					// Add alert scope (org vs personal). Existing alerts default to org.
					ID: "202607050002",
					Migrate: func(tx *gorm.DB) error {
						if err := tx.AutoMigrate(&Alert{}); err != nil {
							return errors.WithStack(err)
						}
						return errors.WithStack(tx.Exec("UPDATE alerts SET scope = 'org' WHERE scope IS NULL OR scope = ''").Error)
					},
					Rollback: func(tx *gorm.DB) error {
						return tx.Migrator().DropColumn(&Alert{}, "scope")
					},
				},
				{
					// Add composite (org_id, created_at) and (user_id, org_id, created_at)
					// indexes on usage_records to back the time-ranged cost aggregations
					// (quota SumCostSince on the hot path, usage/dashboard chart GROUP BYs).
					ID: "202607170001",
					Migrate: func(tx *gorm.DB) error {
						return errors.WithStack(tx.AutoMigrate(&UsageRecord{}))
					},
					Rollback: func(tx *gorm.DB) error {
						if err := tx.Migrator().DropIndex(&UsageRecord{}, "idx_usage_org_created"); err != nil {
							return errors.WithStack(err)
						}
						return errors.WithStack(tx.Migrator().DropIndex(&UsageRecord{}, "idx_usage_user_org_created"))
					},
				},
				{
					// Add extra_body column to llm_models: arbitrary provider-specific
					// key/values injected verbatim into every request targeting the model.
					ID: "202607210001",
					Migrate: func(tx *gorm.DB) error {
						return errors.WithStack(tx.AutoMigrate(&LLMModel{}))
					},
					Rollback: func(tx *gorm.DB) error {
						return errors.WithStack(tx.Migrator().DropColumn(&LLMModel{}, "extra_body"))
					},
				},
				{
					// Applications become org principals holding roles directly.
					// Until now they had no membership, so permission resolution
					// returned an empty set and every proxy call was rejected:
					// backfill the builtin "member" role so existing applications
					// keep working across the upgrade.
					ID: "202607220001",
					Migrate: func(tx *gorm.DB) error {
						if err := tx.AutoMigrate(&ApplicationRole{}); err != nil {
							return errors.WithStack(err)
						}
						return backfillApplicationBuiltinRoles(tx)
					},
					Rollback: func(tx *gorm.DB) error {
						return errors.WithStack(tx.Migrator().DropTable("application_roles"))
					},
				},
				{
					// Introduce the tenant level above organizations and users.
					// Existing instances keep working unchanged: everything is
					// attached to the "default" tenant, the one Xolo resolves
					// transparently when multi-tenancy is disabled.
					ID: "202608130001",
					Migrate: func(tx *gorm.DB) error {
						return withoutForeignKeys(tx, func() error {
							return migrateToDefaultTenant(tx)
						})
					},
					Rollback: func(tx *gorm.DB) error {
						return withoutForeignKeys(tx, func() error {
							return rollbackDefaultTenant(tx)
						})
					},
				},
			})

			m.InitSchema(func(tx *gorm.DB) error {
				return withoutForeignKeys(tx, func() error {
					// Drop the deprecated index if exists (used in old migration)
					tx.Exec("DROP INDEX IF EXISTS " + tx.Statement.Quote("idx_users_email"))

					if err := tx.SetupJoinTable(&Membership{}, "Roles", &MembershipRole{}); err != nil {
						return errors.WithStack(err)
					}

					err := tx.AutoMigrate(
						// Tenant store
						&Tenant{},
						// User store
						&User{}, &AuthToken{}, &UserRole{}, &UserPreferences{},
						// Org store
						&Organization{}, &Membership{}, &Application{},
						// RBAC store
						&Role{}, &RolePermission{}, &RoleModel{}, &MembershipRole{}, &ApplicationRole{},
						// Provider store
						&Provider{}, &LLMModel{},
						// Virtual model store
						&VirtualModel{},
						// Middleware store
						&Middleware{},
						// Personal virtual model store
						&PersonalVirtualModel{},
						// Quota store
						&Quota{},
						// Usage store
						&UsageRecord{},
						// Invite store
						&InviteToken{},
						// Exchange rate cache
						&ExchangeRate{},
						// Plugin node secrets
						&PluginNodeSecret{},
						// Event system
						&Event{}, &Alert{}, &AlertIncident{}, &EventSettings{},
					)
					if err != nil {
						return errors.WithStack(err)
					}

					// A fresh instance still needs the tenant every organization
					// and user hangs from, exactly like an upgraded one.
					if _, err := ensureDefaultTenant(tx); err != nil {
						return errors.WithStack(err)
					}

					return nil
				})
			})

			migrateErr = m.Migrate()
		})

		if migrateErr != nil {
			return nil, errors.WithStack(migrateErr)
		}

		return db, nil
	}
}
