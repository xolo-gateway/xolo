package gorm

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMutationAuditRollbackRefusesDataLoss(t *testing.T) {
	for _, migration := range schemaMigrations(nil) {
		if migration.ID == mutationAuditMigrationID {
			require.NotNil(t, migration.Rollback)
			// Refusal must happen before accessing a database or deleting any history.
			require.ErrorContains(t, migration.Rollback(nil), "cannot be rolled back")
			return
		}
	}
	t.Fatal("audit migration missing")
}
