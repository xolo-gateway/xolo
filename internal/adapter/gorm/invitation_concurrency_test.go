package gorm_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/adapter/events"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

type invitationOutcome struct {
	result *service.InvitationAcceptance
	err    error
}

func TestInvitationConcurrentAcceptance(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		for _, scenario := range []string{"last use", "targeted double accept", "two invites same member"} {
			t.Run(scenario, func(t *testing.T) {
				f := newInvitationFixture(t, store)
				first := f.invite(t, scenario == "targeted double accept", "", nil, ptr(1))
				ids := []model.InviteTokenID{first.ID(), first.ID()}
				users := []model.UserID{f.user.ID(), f.user.ID()}
				if scenario == "last use" {
					users[1] = f.other.ID()
				}
				if scenario == "two invites same member" {
					ids[1] = f.invite(t, false, model.RoleOrgOwner, nil, ptr(1)).ID()
				}
				ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
				defer cancel()
				start := make(chan struct{})
				outcomes := make(chan invitationOutcome, 2)
				for i := range 2 {
					go func() {
						<-start
						result, err := f.service.Accept(ctx, f.tenant.ID(), ids[i], users[i])
						outcomes <- invitationOutcome{result, err}
					}()
				}
				close(start)
				created, already := 0, 0
				var winning *service.InvitationAcceptance
				for range 2 {
					select {
					case outcome := <-outcomes:
						if outcome.err != nil {
							require.True(t, errors.Is(outcome.err, port.ErrInvalid) || errors.Is(outcome.err, port.ErrNotFound), "%v", outcome.err)
							require.Nil(t, outcome.result)
						} else if outcome.result.AlreadyMember {
							already++
						} else {
							created++
							winning = outcome.result
						}
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				require.Equal(t, 1, created)
				if scenario == "two invites same member" {
					require.Equal(t, 1, already)
				} else {
					require.Zero(t, already)
				}
				members, count, err := store.ListOrgMembers(f.ctx, f.org.ID(), port.ListOrgMembersOptions{})
				require.NoError(t, err)
				require.EqualValues(t, 1, count)
				require.Len(t, members[0].Roles(), 1)
				require.Equal(t, winning.Role.ID(), members[0].Roles()[0].ID())
				invites, err := store.ListInvites(f.ctx, f.org.ID())
				require.NoError(t, err)
				uses := 0
				for _, inv := range invites {
					uses += inv.UsesCount()
				}
				if scenario == "targeted double accept" {
					require.Empty(t, invites)
					require.Len(t, f.recorder.snapshot(), 3)
				} else {
					require.Equal(t, 1, uses)
					require.Len(t, f.recorder.snapshot(), 2)
				}
			})
		}
	})
}

// Both transactions must see the membership absent before inserting. This pins
// the ON CONFLICT path on PostgreSQL and full snapshot retry on SQLite.
type synchronizedInsert struct {
	port.InvitationTx
	barrier *sync.WaitGroup
	entered *atomic.Int32
}

func (tx synchronizedInsert) InsertInvitationMember(ctx context.Context, m model.Membership) (bool, error) {
	if tx.entered.Add(1) <= 2 {
		tx.barrier.Done()
		tx.barrier.Wait()
	}
	return tx.InvitationTx.InsertInvitationMember(ctx, m)
}
func TestInvitationMembershipInsertConflict(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		f := newInvitationFixture(t, store)
		a, b := f.invite(t, false, "", nil, nil), f.invite(t, false, model.RoleOrgOwner, nil, nil)
		barrier := &sync.WaitGroup{}
		barrier.Add(2)
		entered := &atomic.Int32{}
		txs := invitationTxWrapper{store, func(tx port.InvitationTx) port.InvitationTx { return synchronizedInsert{tx, barrier, entered} }}
		svc := service.NewInvitationService(events.NewInvitationTransaction(txs, f.recorder))
		ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
		defer cancel()
		outcomes := make(chan invitationOutcome, 2)
		for _, inv := range []model.InviteToken{a, b} {
			go func() {
				result, err := svc.Accept(ctx, f.tenant.ID(), inv.ID(), f.user.ID())
				outcomes <- invitationOutcome{result, err}
			}()
		}
		already := 0
		for range 2 {
			select {
			case out := <-outcomes:
				require.NoError(t, out.err)
				if out.result.AlreadyMember {
					already++
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
		require.Equal(t, 1, already)
		invA, err := store.GetInviteByID(f.ctx, a.ID())
		require.NoError(t, err)
		invB, err := store.GetInviteByID(f.ctx, b.ID())
		require.NoError(t, err)
		require.Equal(t, 1, invA.UsesCount()+invB.UsesCount())
		member, err := store.GetUserOrgMembership(f.ctx, f.user.ID(), f.org.ID())
		require.NoError(t, err)
		require.Len(t, member.Roles(), 1)
		if invA.UsesCount() == 1 {
			require.Equal(t, f.role.ID(), member.Roles()[0].ID())
		}
		require.Len(t, f.recorder.snapshot(), 2)
	})
}

func TestInvitationAcceptAgainstMutation(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		for _, mutation := range []string{"decline", "revoke", "delete"} {
			t.Run(mutation, func(t *testing.T) {
				f := newInvitationFixture(t, store)
				inv := f.invite(t, mutation == "decline", "", nil, ptr(1))
				ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
				defer cancel()
				start := make(chan struct{})
				accepted := make(chan invitationOutcome, 1)
				mutated := make(chan error, 1)
				go func() {
					<-start
					result, err := f.service.Accept(ctx, f.tenant.ID(), inv.ID(), f.user.ID())
					accepted <- invitationOutcome{result, err}
				}()
				go func() {
					<-start
					var err error
					switch mutation {
					case "decline":
						_, err = f.service.Decline(ctx, f.tenant.ID(), inv.ID(), f.user.ID())
					case "revoke":
						err = store.RevokeInvite(ctx, inv.ID())
					case "delete":
						err = events.NewInviteStore(store, f.recorder).DeleteInvite(ctx, inv.ID())
					}
					mutated <- err
				}()
				close(start)
				out, changeErr := <-accepted, <-mutated
				if changeErr != nil {
					require.ErrorIs(t, changeErr, port.ErrNotFound)
				}
				exists, err := store.IsMember(f.ctx, f.user.ID(), f.org.ID())
				require.NoError(t, err)
				if out.err == nil {
					require.True(t, exists)
					m, err := store.GetUserOrgMembership(f.ctx, f.user.ID(), f.org.ID())
					require.NoError(t, err)
					require.Len(t, m.Roles(), 1)
					require.Equal(t, f.role.ID(), m.Roles()[0].ID())
				} else {
					require.True(t, errors.Is(out.err, port.ErrNotFound) || errors.Is(out.err, port.ErrInvalid), "%v", out.err)
					require.False(t, exists)
					require.Nil(t, out.result)
				}
				after, err := store.GetInviteByID(f.ctx, inv.ID())
				if mutation == "revoke" {
					require.NoError(t, err)
					require.NotNil(t, after.RevokedAt())
					want := 0
					if exists {
						want = 1
					}
					require.Equal(t, want, after.UsesCount())
				} else {
					require.ErrorIs(t, err, port.ErrNotFound)
				}
				added := 0
				for _, event := range f.recorder.snapshot() {
					if event.Type() == model.EventTypeMemberAdded {
						added++
					}
				}
				if exists {
					require.Equal(t, 1, added)
				} else {
					require.Zero(t, added)
				}
			})
		}
	})
}

// Force a successful callback to roll back, just as a commit conflict would.
// The event decorator and service must discard that attempt's events/results.
type replayInvitationTransaction struct {
	port.InvitationTransaction
	afterRollback func()
	finalFailure  bool
}

func (tx replayInvitationTransaction) WithInvitationTransaction(ctx context.Context, fn func(port.InvitationTx) error) error {
	err := tx.InvitationTransaction.WithInvitationTransaction(ctx, func(bound port.InvitationTx) error {
		if err := fn(bound); err != nil {
			return err
		}
		return errInvitationInjected
	})
	if !errors.Is(err, errInvitationInjected) || tx.finalFailure {
		return err
	}
	if tx.afterRollback != nil {
		tx.afterRollback()
	}
	return tx.InvitationTransaction.WithInvitationTransaction(ctx, fn)
}
func TestInvitationRetryDiscardsResultsAndEvents(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		for _, mode := range []string{"commit failure", "retry success", "retry revoked", "retry inactive user", "retry deleted role", "retry foreign organization"} {
			t.Run(mode, func(t *testing.T) {
				f := newInvitationFixture(t, store)
				inv := f.invite(t, true, "", nil, nil)
				replay := replayInvitationTransaction{InvitationTransaction: store, finalFailure: mode == "commit failure"}
				replay.afterRollback = func() {
					require.Empty(t, f.recorder.snapshot())
					switch mode {
					case "retry revoked":
						require.NoError(t, store.RevokeInvite(f.ctx, inv.ID()))
					case "retry inactive user":
						f.user.SetActive(false)
						require.NoError(t, store.SaveUser(f.ctx, f.user))
					case "retry deleted role":
						require.NoError(t, store.DeleteRole(f.ctx, f.role.ID()))
					case "retry foreign organization":
						require.NoError(t, store.SaveOrg(f.ctx, foreignInvitationOrg{Organization: f.org, tenant: f.foreignTenant.ID()}))
					}
				}
				svc := service.NewInvitationService(events.NewInvitationTransaction(replay, f.recorder))
				result, err := svc.Accept(f.ctx, f.tenant.ID(), inv.ID(), f.user.ID())
				if mode == "retry success" {
					require.NoError(t, err)
					require.NotNil(t, result)
					require.Len(t, f.recorder.snapshot(), 3)
				} else {
					require.Error(t, err)
					require.Nil(t, result)
					require.Empty(t, f.recorder.snapshot())
					member, err := store.IsMember(f.ctx, f.user.ID(), f.org.ID())
					require.NoError(t, err)
					require.False(t, member)
					after, err := store.GetInviteByID(f.ctx, inv.ID())
					require.NoError(t, err)
					require.Zero(t, after.UsesCount())
				}
			})
		}
	})
}

type foreignInvitationOrg struct {
	model.Organization
	tenant model.TenantID
}

func (o foreignInvitationOrg) TenantID() model.TenantID { return o.tenant }

func (o foreignInvitationOrg) Slug() string { return o.Organization.Slug() + "-moved" }
