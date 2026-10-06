package gorm_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

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
					require.ErrorContains(t, test.run(ctx), "invitation transaction")
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
