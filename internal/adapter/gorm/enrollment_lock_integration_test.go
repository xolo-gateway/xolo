//go:build integration

package gorm_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// A role deleted while a rule granting it is saved: the deletion waits for the
// rule, then refuses, instead of leaving the rule without its role.
func TestEnrollmentRoleDeletionWaitsForARuleBeingSaved(t *testing.T) {
	store := postgresBackends(t)[0].newStore(t)
	ctx := t.Context()
	f := newEnrollmentFixture(t, store)
	role := model.NewRole(f.org.ID(), "Reader", "")
	require.NoError(t, store.CreateRole(ctx, role))

	deleted := make(chan error, 1)
	require.NoError(t, store.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
		if _, err := tx.GetRoleByID(ctx, role.ID()); err != nil {
			return err
		}
		go func() { deleted <- store.DeleteRole(ctx, role.ID()) }()
		select {
		case err := <-deleted:
			t.Fatalf("the deletion did not wait for the rule: %v", err)
		case <-time.After(300 * time.Millisecond):
		}
		rule := model.NewEnrollmentRule(f.org.ID(), "rule", model.EnrollmentKindClaim, "iss", enrollmentIssuer, string(role.ID()), true)
		return tx.CreateEnrollmentRule(ctx, rule)
	}))
	require.ErrorIs(t, <-deleted, port.ErrRoleGrantedByEnrollment)
}
