package gorm

import (
	"context"
	"sync"

	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
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
			migrateErr = withMigrationLock(ctx, db, func(db *gorm.DB) error {
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
					{
						// Replace clear-text API keys by their SHA-256 hash.
						ID: "202608220001",
						Migrate: func(tx *gorm.DB) error {
							return migrateAuthTokensToHashes(tx)
						},
						Rollback: func(tx *gorm.DB) error {
							// A hash cannot be reversed: rolling back would have to
							// invalidate every key, which is worse than staying put.
							return errors.New("auth token hashing cannot be rolled back")
						},
					},
					{
						// Covering index for the PAYG cost sums the quota enforcer runs on
						// every proxy request: (org_id, plan_covered, created_at, user_id,
						// currency, cost) answers SumCostSince* from the index alone instead
						// of visiting every row of the org over the budget period.
						ID: "202609040001",
						Migrate: func(tx *gorm.DB) error {
							return errors.WithStack(tx.AutoMigrate(&UsageRecord{}))
						},
						Rollback: func(tx *gorm.DB) error {
							return errors.WithStack(tx.Migrator().DropIndex(&UsageRecord{}, "idx_usage_org_payg_cost"))
						},
					},
					{
						// Subscription counterpart of the index above: (org_id, provider_id,
						// plan_covered, created_at, user_id) serves the fair-share allocator's
						// plan-wide aggregations, the DISTINCT count of active users from the
						// index alone. Without it that count, which runs on every proxy request
						// of a subscription provider, scans every row of the org over the
						// window and filters provider and plan afterwards.
						ID: "202609150001",
						Migrate: func(tx *gorm.DB) error {
							return errors.WithStack(tx.AutoMigrate(&UsageRecord{}))
						},
						Rollback: func(tx *gorm.DB) error {
							return errors.WithStack(tx.Migrator().DropIndex(&UsageRecord{}, "idx_usage_org_prov_plan"))
						},
					},
					{
						// (org_id, provider_id, plan_covered, user_id, created_at) for the
						// per-user presence probe of the fair-share allocator. The index
						// above puts user_id after the created_at range, so an equality on
						// the caller cannot bound it and the probe would walk the window.
						//
						// Both indexes are tags on the same model, so the AutoMigrate of
						// 202609150001 already creates this one on a database that skipped
						// both. This entry exists so the index has a rollback of its own.
						ID: "202609150002",
						Migrate: func(tx *gorm.DB) error {
							return errors.WithStack(tx.AutoMigrate(&UsageRecord{}))
						},
						Rollback: func(tx *gorm.DB) error {
							return errors.WithStack(tx.Migrator().DropIndex(&UsageRecord{}, "idx_usage_org_prov_user"))
						},
					},
					{
						// Add the status column to usage_records. A streamed answer cut
						// short by the provider is now recorded like any other call — the
						// tokens it delivered were billed — and this column is what tells
						// the two apart in usage reports. Existing rows predate the change
						// and are all completed calls, hence the "ok" backfill.
						ID: "202609170001",
						Migrate: func(tx *gorm.DB) error {
							if err := tx.AutoMigrate(&UsageRecord{}); err != nil {
								return errors.WithStack(err)
							}
							return errors.WithStack(tx.Exec("UPDATE usage_records SET status = ? WHERE status IS NULL OR status = ''", string(model.UsageStatusOK)).Error)
						},
						Rollback: func(tx *gorm.DB) error {
							return errors.WithStack(tx.Migrator().DropColumn(&UsageRecord{}, "status"))
						},
					},
					{
						// Running per-day PAYG counters backing budget enforcement.
						// Until now every proxied request aggregated over
						// usage_records, up to six times, and the yearly window
						// rescanned the whole history: the database saturated at a few
						// tens of requests per second whatever the number of gateway
						// replicas. The counters are filled from the existing history
						// so budgets carry over unchanged.
						ID: "202609170002",
						Migrate: func(tx *gorm.DB) error {
							return migrateQuotaUsageCounters(tx)
						},
						Rollback: func(tx *gorm.DB) error {
							return errors.WithStack(tx.Migrator().DropTable("quota_usages"))
						},
					},
					{
						// Replays the per-day PAYG counter backfill so the application
						// scope (introduced with the QuotaScopeApplication enforcement
						// PR) gets its own rows. Migration 202609170002 above runs
						// once per instance and gormigrate marks it applied; that id
						// shipped before application counters existed, so on upgrade
						// from any pre-application-enforcement build the table only
						// has org+user rows. This migration backfills application rows
						// from the existing usage_records on the application id.
						//
						// The table is guaranteed to exist when this runs: gormigrate
						// records each id as applied after its first execution, so
						// this id is only reached on upgrade from a build that
						// already created quota_usages through 202609170002. On a
						// fresh install, InitSchema creates the table and marks every
						// migration applied, so this id never executes there and no
						// extra AutoMigrate is needed.
						//
						// The backfill's first statement is DELETE FROM quota_usages,
						// so the replay is idempotent and safe to re-run if needed.
						ID: "202609240001",
						Migrate: func(tx *gorm.DB) error {
							return backfillQuotaUsage(tx)
						},
						Rollback: func(tx *gorm.DB) error {
							// Truncate the application counter rows so a downgrade
							// returns to the pre-fix state (no application counter at
							// all). Org and user rows stay — they were the pre-fix
							// shape and any downgrade expects them intact.
							return errors.WithStack(tx.Exec("DELETE FROM " + quotaUsageTable + " WHERE scope = 'application'").Error)
						},
					},
					{
						ID:      "202609300001",
						Migrate: migrateUniqueMemberships,
						Rollback: func(tx *gorm.DB) error {
							return tx.Exec("DROP INDEX IF EXISTS idx_memberships_user_org").Error
						},
					},
					{
						ID:      "202609300002",
						Migrate: migrateRevokeLegacyInvitations,
						Rollback: func(tx *gorm.DB) error {
							return errors.New("legacy invitation revocation cannot be rolled back; recreate links")
						},
					},
					{
						ID:      "202609300003",
						Migrate: migratePluginSecretScope,
						Rollback: func(tx *gorm.DB) error {
							return errors.New("plugin secret scope migration cannot be rolled back; legacy uniqueness may no longer hold")
						},
					},
					{ID: commonMigrationID, Migrate: migrateCommonSchema},
					{ID: "202610020002", Migrate: migrateCommonReads},
					{ID: "202610020003", Migrate: migrateWebhooks},
					{ID: "202610030001", Migrate: migrateIdentitySessions},
					{ID: "202610030002", Migrate: migrateLifecycle},
					{ID: "202610030003", Migrate: migrateBusiness},
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
							&Quota{}, &QuotaUsage{},
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
						if err := migrateUniqueMemberships(tx); err != nil {
							return err
						}
						if err := migratePluginSecretScope(tx); err != nil {
							return err
						}
						if err := installCommonSchema(tx); err != nil {
							return err
						}
						if err := migrateCommonReads(tx); err != nil {
							return err
						}
						if err := migrateWebhooks(tx); err != nil {
							return err
						}
						if err := migrateIdentitySessions(tx); err != nil {
							return err
						}
						if err := migrateLifecycle(tx); err != nil {
							return err
						}
						return migrateBusiness(tx)
					})
				})

				return m.Migrate()
			})
		})

		if migrateErr != nil {
			return nil, errors.WithStack(migrateErr)
		}

		return db, nil
	}
}
