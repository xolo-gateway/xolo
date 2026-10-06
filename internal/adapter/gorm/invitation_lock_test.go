package gorm_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/adapter/events"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	gormpkg "gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type beforeInvitationInsert struct {
	port.InvitationTx
	before func()
}

func (tx beforeInvitationInsert) InsertInvitationMember(ctx context.Context, m model.Membership) (bool, error) {
	tx.before()
	return tx.InvitationTx.InsertInvitationMember(ctx, m)
}

func TestInvitationParentLocksAndSnapshotRetry(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newStoreOn(t, db)
		for _, parent := range []string{"organization", "user", "role", "invitation"} {
			t.Run(parent, func(t *testing.T) {
				f := newInvitationFixture(t, store)
				inv := f.invite(t, true, "", nil, nil)
				var inserted atomic.Int32
				txs := invitationTxWrapper{store, func(tx port.InvitationTx) port.InvitationTx {
					return beforeInvitationInsert{tx, func() {
						if inserted.Add(1) != 1 {
							return
						}
						if db.Dialector.Name() == "postgres" {
							// A concurrent UPDATE/DELETE needs FOR UPDATE. NOWAIT verifies that
							// every validated parent and the invitation are locked before writing.
							var row any
							var id string
							switch parent {
							case "organization":
								row, id = &xologorm.Organization{}, string(f.org.ID())
							case "user":
								row, id = &xologorm.User{}, string(f.user.ID())
							case "role":
								row, id = &xologorm.Role{}, string(f.role.ID())
							case "invitation":
								row, id = &xologorm.InviteToken{}, string(inv.ID())
							}
							err := db.Transaction(func(other *gormpkg.DB) error {
								return other.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).First(row, "id = ?", id).Error
							})
							var pgErr *pgconn.PgError
							require.ErrorAs(t, err, &pgErr)
							require.Equal(t, "55P03", pgErr.Code)
							return
						}
						// SQLite allows the concurrent write while this transaction only read.
						// Its next write must fail BUSY_SNAPSHOT, and all validation must run anew.
						switch parent {
						case "organization":
							require.NoError(t, store.SaveOrg(f.ctx, model.UpdateOrganization(f.org, model.WithOrgActive(false))))
						case "user":
							f.user.SetActive(false)
							require.NoError(t, store.SaveUser(f.ctx, f.user))
						case "role":
							require.NoError(t, store.DeleteRole(f.ctx, f.role.ID()))
						case "invitation":
							require.NoError(t, store.RevokeInvite(f.ctx, inv.ID()))
						}
					}}
				}}
				svc := service.NewInvitationService(events.NewInvitationTransaction(txs, f.recorder))
				result, err := svc.Accept(f.ctx, f.tenant.ID(), inv.ID(), f.user.ID())
				if db.Dialector.Name() == "postgres" {
					require.NoError(t, err)
					require.NotNil(t, result)
					require.Len(t, f.recorder.snapshot(), 3)
				} else {
					require.True(t, errors.Is(err, port.ErrInvalid) || errors.Is(err, port.ErrNotAllowed), "%v", err)
					require.Nil(t, result)
					require.Empty(t, f.recorder.snapshot())
					member, err := store.IsMember(f.ctx, f.user.ID(), f.org.ID())
					require.NoError(t, err)
					require.False(t, member)
				}
			})
		}
	})
}

func TestInvitationConditionalConsumption(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		f := newInvitationFixture(t, store)
		inv := f.invite(t, false, "", nil, ptr(1))
		require.NoError(t, store.IncrementInviteUses(f.ctx, inv.ID()))
		require.ErrorIs(t, store.IncrementInviteUses(f.ctx, inv.ID()), port.ErrInvalid)
		require.ErrorIs(t, store.IncrementInviteUses(f.ctx, model.NewInviteTokenID()), port.ErrInvalid)
		expired := time.Now().Add(-time.Hour)
		inv = f.invite(t, false, "", &expired, nil)
		require.ErrorIs(t, store.IncrementInviteUses(f.ctx, inv.ID()), port.ErrInvalid)
		inv = f.invite(t, false, "", nil, nil)
		require.NoError(t, store.RevokeInvite(f.ctx, inv.ID()))
		require.ErrorIs(t, store.IncrementInviteUses(f.ctx, inv.ID()), port.ErrInvalid)
		after, err := store.GetInviteByID(f.ctx, inv.ID())
		require.NoError(t, err)
		require.Zero(t, after.UsesCount())
	})
}

func TestInvitationExpirationOffsets(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		f := newInvitationFixture(t, store)
		// SQLite persists RFC3339 offsets; lexical comparison would consider the
		// expired +14:00 value future and the future -12:00 value expired.
		expired := time.Now().Add(-time.Hour).In(time.FixedZone("east", 14*60*60))
		future := time.Now().Add(time.Hour).In(time.FixedZone("west", -12*60*60))
		expiredInvite := f.invite(t, true, "", &expired, nil)
		futureInvite := f.invite(t, true, "", &future, nil)
		pending, err := store.ListPendingInvitesForEmail(f.ctx, f.tenant.ID(), f.user.Email())
		require.NoError(t, err)
		require.Len(t, pending, 1)
		require.Equal(t, futureInvite.ID(), pending[0].ID())
		require.ErrorIs(t, store.IncrementInviteUses(f.ctx, expiredInvite.ID()), port.ErrInvalid)
		require.NoError(t, store.IncrementInviteUses(f.ctx, futureInvite.ID()))
	})
}
