package gorm_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
)

func TestStoreMigrateAfterCheckSchema(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		ctx := t.Context()
		require.NoError(t, adapter.MigrateDatabase(ctx, db, nil))
		store := adapter.NewStore(db, adapter.WithAutoMigrate(false))
		require.NoError(t, store.CheckSchema(ctx))

		// Make migrations pending after the check to observe whether the same
		// store actually migrates, including when automatic migration is disabled.
		prepareInvitationUpgrade(t, db)
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		require.ErrorIs(t, store.Migrate(canceled), context.Canceled)

		require.NoError(t, store.Migrate(ctx), "failed migrations must remain retryable")
		require.True(t, db.Migrator().HasIndex(&adapter.Membership{}, "idx_memberships_user_org"))
		require.NoError(t, adapter.CheckDatabaseSchema(ctx, db))
	})
}

func TestStoreCheckSchemaAfterMigrate(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		ctx := t.Context()
		store := adapter.NewStore(db)
		require.NoError(t, store.Migrate(ctx))

		require.NoError(t, db.Exec("INSERT INTO migrations (id) VALUES (?)", "209901010001").Error)
		require.ErrorContains(t, store.CheckSchema(ctx), "unknown migrations")
		require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", "209901010001").Error)
		require.NoError(t, store.CheckSchema(ctx), "failed checks must remain retryable")
	})
}

func TestInvitationTransactionRejectsSchemaOperations(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *adapter.Store) {
		ctx := t.Context()
		org := model.NewOrganization(testTenantID, "schema", "Schema", "")
		require.NoError(t, store.CreateOrg(ctx, org))
		err := store.WithInvitationTransaction(ctx, func(tx port.InvitationTx) error {
			schema, ok := tx.(interface {
				Migrate(context.Context) error
				CheckSchema(context.Context) error
			})
			require.True(t, ok)
			for _, test := range []struct {
				name string
				run  func(context.Context) error
			}{
				{name: "Migrate", run: schema.Migrate},
				{name: "CheckSchema", run: schema.CheckSchema},
			} {
				t.Run(test.name, func(t *testing.T) {
					require.ErrorContains(t, test.run(ctx), "schema within a transaction")
				})
			}
			// Refusing schema operations must leave the caller's transaction usable.
			_, err := tx.GetOrgByID(ctx, org.ID())
			return err
		})
		require.NoError(t, err)
		require.NoError(t, store.Migrate(ctx))
		require.NoError(t, store.CheckSchema(ctx))
	})
}
