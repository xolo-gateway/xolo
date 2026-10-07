package gorm

import (
	"context"
	"fmt"
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

func createDatabaseInitializer(db *gorm.DB) func(context.Context, bool) (*gorm.DB, error) {
	var mu sync.Mutex
	var checked, migrated bool
	return func(ctx context.Context, migrate bool) (*gorm.DB, error) {
		mu.Lock()
		defer mu.Unlock()
		// Each operation is cached independently: checking must not suppress
		// a later explicit migration, nor migration a later schema check.
		if migrate && !migrated {
			if err := MigrateDatabase(ctx, db, nil); err != nil {
				return nil, err
			}
			migrated = true
		}
		if !migrate && !checked {
			if err := CheckDatabaseSchema(ctx, db); err != nil {
				return nil, err
			}
			checked = true
		}
		return db, nil
	}
}

// CheckDatabaseSchema is read-only: a pending migration must be applied offline
// when automatic migration is disabled. Unknown migrations indicate a newer binary.
func CheckDatabaseSchema(ctx context.Context, db *gorm.DB) error {
	db = db.WithContext(ctx)
	if !db.Migrator().HasTable("migrations") {
		return fmt.Errorf("database is not initialized; run xolo-migrate apply before starting with XOLO_STORAGE_AUTO_MIGRATE=false")
	}
	var ids []string
	if err := db.Table("migrations").Pluck("id", &ids).Error; err != nil {
		return err
	}
	applied := make(map[string]bool, len(ids))
	for _, id := range ids {
		applied[id] = true
	}
	for _, migration := range schemaMigrations(nil) {
		if !applied[migration.ID] {
			return fmt.Errorf("pending migration %s; stop all writers and run xolo-migrate apply", migration.ID)
		}
		delete(applied, migration.ID)
	}
	delete(applied, "SCHEMA_INIT")
	if len(applied) > 0 {
		return fmt.Errorf("database contains unknown migrations; use a compatible Xolo binary")
	}
	return nil
}

// MigrateDatabase applies the complete migration chain under the startup lock.
// A recovery artifact fixes the UUID mapping and operator decisions for an upgrade.
func MigrateDatabase(ctx context.Context, db *gorm.DB, artifact *RecoveryArtifact) error {
	return withMigrationLock(ctx, db, func(db *gorm.DB) (bool, error) {
		migrationExecuted := false
		if artifact != nil {
			report, err := DiagnoseCommonRecovery(ctx, db, artifact)
			if err != nil {
				return false, err
			}
			if len(report.Issues) != 0 {
				return false, fmt.Errorf("recovery plan is invalid: %v", report.Issues)
			}
			if !report.Applied && db.Migrator().HasTable("migrations") {
				var applied int64
				if err := db.Table("migrations").Where("id = ?", commonMigrationID).Count(&applied).Error; err != nil {
					return false, err
				}
				if applied != 0 {
					return false, fmt.Errorf("UUID migration is already applied without this recovery plan; do not use recovery to edit a current database")
				}
			}
		}
		migrations := schemaMigrations(artifact)
		for _, migration := range migrations {
			migrate := migration.Migrate
			migration.Migrate = func(tx *gorm.DB) error {
				migrationExecuted = true
				return migrate(tx)
			}
		}
		m := gormigrate.New(db, gormigrate.DefaultOptions, migrations)
		m.InitSchema(func(tx *gorm.DB) error {
			migrationExecuted = true
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
					// Provisioning audit
					&MutationAudit{},
					// Domain routing
					&Domain{}, &DomainRouting{},
					// Provisioning projections and event feed
					&ProvisioningProjection{}, &ProvisioningEvent{}, &ProvisioningFeed{},
					// Event system
					&Event{}, &Alert{}, &AlertIncident{}, &EventSettings{},
					// Interactive OIDC sessions
					&OIDCIdentity{}, &OIDCSession{}, &OIDCLogoutReplay{},
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
				if err := migrateProvisioningSync(tx); err != nil {
					return err
				}
				return migrateWebhooks(tx)
			})
		})
		err := m.Migrate()
		return migrationExecuted, err
	})
}

func schemaMigrations(artifact *RecoveryArtifact) []*gormigrate.Migration {
	return []*gormigrate.Migration{
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
		{ID: commonMigrationID, Migrate: func(tx *gorm.DB) error {
			if artifact == nil {
				return migrateCommonSchema(tx)
			}
			warnCommonMigration(tx)
			_, err := applyCommonRecovery(tx.Statement.Context, tx, artifact)
			return err
		}},
		{
			ID:       mutationAuditMigrationID,
			Migrate:  func(tx *gorm.DB) error { return tx.AutoMigrate(&MutationAudit{}) },
			Rollback: func(*gorm.DB) error { return errors.New("mutation audit history cannot be rolled back") },
		},
		{
			ID:      commonAPIMigrationID,
			Migrate: migrateCommonAPI,
			Rollback: func(*gorm.DB) error {
				return errors.New("common API migration cannot be rolled back: domains and suspended memberships would be lost")
			},
		},
		{
			ID:      inviteEmailMigrationID,
			Migrate: migrateNormalizeInviteeEmails,
			// The original case is not kept anywhere, and the normalized form is
			// what every reader expects.
			Rollback: func(*gorm.DB) error { return nil },
		},
		{
			// Out of chronological order on purpose: the provisioning sync
			// backfill below snapshots users with their declared identity, so
			// an upgrade that has not applied it yet needs the columns first.
			// A database that already applied it runs this one alone.
			ID:       identityMigrationID,
			Migrate:  migrateIdentity,
			Rollback: rollbackIdentity,
		},
		{
			ID:      provisioningSyncMigrationID,
			Migrate: migrateProvisioningSync,
			Rollback: func(*gorm.DB) error {
				return errors.New("provisioning sync migration cannot be rolled back: consumers rely on the revisions and the event feed")
			},
		},
		{
			ID:      webhooksMigrationID,
			Migrate: migrateWebhooks,
			Rollback: func(*gorm.DB) error {
				return errors.New("webhooks migration cannot be rolled back: pending deliveries would be lost")
			},
		},
		{
			ID:       oidcSessionsMigrationID,
			Migrate:  migrateOIDCSessions,
			Rollback: rollbackOIDCSessions,
		},
	}
}
