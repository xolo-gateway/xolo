package gorm_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/adapter/events"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	gormpkg "gorm.io/gorm"
)

type invitationRecorder struct {
	mu     sync.Mutex
	events []model.Event
}

func (r *invitationRecorder) Emit(_ context.Context, event model.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}
func (r *invitationRecorder) snapshot() []model.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]model.Event(nil), r.events...)
}

type invitationFixture struct {
	store                    *xologorm.Store
	tenant, foreignTenant    model.Tenant
	org, foreignOrg          model.Organization
	user, other, foreignUser *model.BaseUser
	role, foreignRole        model.Role
	recorder                 *invitationRecorder
	service                  *service.InvitationService
	ctx                      context.Context
}

func newInvitationFixture(t *testing.T, store *xologorm.Store) *invitationFixture {
	t.Helper()
	suffix := string(model.NewOrgID())
	f := &invitationFixture{store: store, recorder: &invitationRecorder{}}
	f.tenant = model.NewTenant("a-"+suffix, "A", "")
	f.foreignTenant = model.NewTenant("b-"+suffix, "B", "")
	require.NoError(t, store.CreateTenant(t.Context(), f.tenant))
	require.NoError(t, store.CreateTenant(t.Context(), f.foreignTenant))
	f.org = model.NewOrganization(f.tenant.ID(), "shared", "PRIVATE-ORGANIZATION", "")
	f.foreignOrg = model.NewOrganization(f.foreignTenant.ID(), f.org.Slug(), "FOREIGN-ORGANIZATION", "")
	require.NoError(t, store.CreateOrg(t.Context(), f.org))
	require.NoError(t, store.CreateOrg(t.Context(), f.foreignOrg))
	f.user = model.NewUser(f.tenant.ID(), "test", "user", "Recipient@example.test", "Recipient", true, model.PlatformRoleUser)
	f.other = model.NewUser(f.tenant.ID(), "test", "other", "other@example.test", "Other", true, model.PlatformRoleUser)
	f.foreignUser = model.NewUser(f.foreignTenant.ID(), "test", "user", f.user.Email(), "Foreign", true, model.PlatformRoleUser)
	for _, u := range []model.User{f.user, f.other, f.foreignUser} {
		require.NoError(t, store.SaveUser(t.Context(), u))
	}
	f.role = model.NewRole(f.org.ID(), "PRIVATE-ROLE", "")
	f.foreignRole = model.NewRole(f.foreignOrg.ID(), "FOREIGN-ROLE", "")
	require.NoError(t, store.CreateRole(t.Context(), f.role))
	require.NoError(t, store.CreateRole(t.Context(), f.foreignRole))
	require.NoError(t, store.EnsureBuiltinRoles(t.Context(), f.org.ID()))
	f.ctx = httpCtx.SetUser(t.Context(), f.user)
	f.service = service.NewInvitationService(events.NewInvitationTransaction(store, f.recorder))
	return f
}
func (f *invitationFixture) invite(t *testing.T, targeted bool, role string, expires *time.Time, limit *int) model.InviteToken {
	t.Helper()
	var email *string
	if targeted {
		e := f.user.Email()
		email = &e
	}
	if role == "" {
		role = string(f.role.ID())
	}
	inv := model.NewInviteToken(f.org.ID(), role, email, expires, limit, f.other.ID())
	require.NoError(t, f.store.CreateInvite(t.Context(), inv))
	return inv
}

func TestInvitationValidation(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		cases := []struct {
			name string
			want error
		}{
			{"foreign tenant same slug", port.ErrNotFound}, {"foreign user", port.ErrNotFound},
			{"wrong recipient", port.ErrNotFound},
			{"inactive user", port.ErrNotAllowed}, {"inactive organization", port.ErrInvalid},
			{"foreign role", port.ErrInvalid}, {"deleted role", port.ErrInvalid},
			{"unknown role", port.ErrInvalid},
			{"expired", port.ErrInvalid}, {"revoked", port.ErrInvalid}, {"exhausted", port.ErrInvalid},
			{"targeted already consumed", port.ErrInvalid}, {"missing invitation", port.ErrNotFound},
			{"missing user", port.ErrNotFound}, {"empty tenant", port.ErrNotFound},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				f := newInvitationFixture(t, store)
				tenant, user := f.tenant.ID(), f.user.ID()
				role := string(f.role.ID())
				var expires *time.Time
				var limit *int
				switch tc.name {
				case "foreign tenant same slug":
					tenant = f.foreignTenant.ID()
				case "foreign user":
					user = f.foreignUser.ID()
				case "wrong recipient":
					user = f.other.ID()
				case "inactive user":
					f.user.SetActive(false)
					require.NoError(t, store.SaveUser(f.ctx, f.user))
				case "inactive organization":
					require.NoError(t, store.SaveOrg(f.ctx, model.UpdateOrganization(f.org, model.WithOrgActive(false))))
				case "foreign role":
					role = string(f.foreignRole.ID())
				case "deleted role":
					require.NoError(t, store.DeleteRole(f.ctx, f.role.ID()))
				case "unknown role":
					role = "unknown-role"

				case "expired":
					when := time.Now().Add(-time.Hour)
					expires = &when
				case "exhausted":
					limit = ptr(1)
				case "missing user":
					user = model.NewUserID()
				case "empty tenant":
					tenant = ""
				}
				inv := f.invite(t, true, role, expires, limit)
				id := inv.ID()
				switch tc.name {
				case "revoked":
					require.NoError(t, store.RevokeInvite(f.ctx, id))
				case "exhausted", "targeted already consumed":
					require.NoError(t, store.IncrementInviteUses(f.ctx, id))
				case "missing invitation":
					id = model.NewInviteTokenID()
				}
				before, err := store.GetInviteByID(f.ctx, inv.ID())
				require.NoError(t, err)
				view, err := f.service.Prepare(f.ctx, tenant, id, user)
				require.ErrorIs(t, err, tc.want)
				require.Nil(t, view)
				accepted, err := f.service.Accept(f.ctx, tenant, id, user)
				require.ErrorIs(t, err, tc.want)
				require.Nil(t, accepted)
				open, err := f.service.Decline(f.ctx, tenant, id, user)
				require.ErrorIs(t, err, tc.want)
				require.False(t, open)
				after, err := store.GetInviteByID(f.ctx, inv.ID())
				require.NoError(t, err)
				require.Equal(t, before, after)
				member, err := store.IsMember(f.ctx, user, f.org.ID())
				require.NoError(t, err)
				require.False(t, member)
				require.Empty(t, f.recorder.snapshot())
			})
		}
	})
}

func TestInvitationCreate(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		f := newInvitationFixture(t, store)
		for _, tc := range []struct {
			name  string
			input service.CreateInvitationInput
		}{
			{"email", service.CreateInvitationInput{Email: "not-an-email"}},
			{"display name", service.CreateInvitationInput{Email: "Recipient <a@example.test>"}},
			{"date syntax", service.CreateInvitationInput{ExpiresAt: "tomorrow"}},
			{"past", service.CreateInvitationInput{ExpiresAt: "2000-01-01"}},
			{"today midnight", service.CreateInvitationInput{ExpiresAt: time.Now().UTC().Format("2006-01-02")}},
			{"zero limit", service.CreateInvitationInput{MaxUses: "0"}},
			{"negative limit", service.CreateInvitationInput{MaxUses: "-1"}},
			{"decimal limit", service.CreateInvitationInput{MaxUses: "1.5"}},
			{"text limit", service.CreateInvitationInput{MaxUses: "no"}},
			{"space limit", service.CreateInvitationInput{MaxUses: " "}},
			{"overflow limit", service.CreateInvitationInput{MaxUses: "99999999999999999999999"}},
			{"foreign role", service.CreateInvitationInput{Role: string(f.foreignRole.ID())}},
			{"unknown role", service.CreateInvitationInput{Role: "invalid"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				inv, err := f.service.Create(f.ctx, f.tenant.ID(), f.org.ID(), f.user.ID(), tc.input)
				require.ErrorIs(t, err, port.ErrInvalid)
				require.Nil(t, inv)
			})
		}
		require.Empty(t, f.recorder.snapshot())
		for _, role := range []string{"", model.RoleMember, model.RoleOrgAdmin, model.RoleOrgOwner, string(f.role.ID())} {
			t.Run("role-"+role, func(t *testing.T) {
				inv, err := f.service.Create(f.ctx, f.tenant.ID(), f.org.ID(), f.user.ID(), service.CreateInvitationInput{Role: role})
				require.NoError(t, err)
				resolved, err := store.GetRoleByID(f.ctx, model.RoleID(inv.Role()))
				require.NoError(t, err)
				require.Equal(t, f.org.ID(), resolved.OrgID())
				require.Nil(t, inv.MaxUses())
				require.Nil(t, inv.ExpiresAt())
			})
		}
		inv, err := f.service.Create(f.ctx, f.tenant.ID(), f.org.ID(), f.user.ID(), service.CreateInvitationInput{Email: f.user.Email(), ExpiresAt: "2099-01-02", MaxUses: "12"})
		require.NoError(t, err)
		require.Equal(t, 1, *inv.MaxUses())
		require.Equal(t, time.Date(2099, 1, 2, 0, 0, 0, 0, time.UTC), *inv.ExpiresAt())
		require.Len(t, f.recorder.snapshot(), 6)
		_, err = f.service.Create(f.ctx, f.foreignTenant.ID(), f.org.ID(), f.foreignUser.ID(), service.CreateInvitationInput{})
		require.ErrorIs(t, err, port.ErrNotFound)
	})
}

func TestInvitationAcceptanceAndDecline(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		for _, role := range []string{model.RoleMember, model.RoleOrgAdmin, model.RoleOrgOwner, "custom"} {
			t.Run(role, func(t *testing.T) {
				f := newInvitationFixture(t, store)
				code := role
				if code == "custom" {
					code = string(f.role.ID())
				}
				inv := f.invite(t, true, code, nil, nil)
				anonymous, err := f.service.Prepare(f.ctx, f.tenant.ID(), inv.ID(), "")
				require.NoError(t, err)
				require.True(t, anonymous.LoginRequired)
				require.Nil(t, anonymous.Org)
				require.Nil(t, anonymous.Role)
				require.Nil(t, anonymous.Invite)
				result, err := f.service.Accept(f.ctx, f.tenant.ID(), inv.ID(), f.user.ID())
				require.NoError(t, err)
				require.False(t, result.AlreadyMember)
				membership, err := store.GetUserOrgMembership(f.ctx, f.user.ID(), f.org.ID())
				require.NoError(t, err)
				require.Len(t, membership.Roles(), 1)
				require.Equal(t, result.Role.ID(), membership.Roles()[0].ID())
				_, err = store.GetInviteByID(f.ctx, inv.ID())
				require.ErrorIs(t, err, port.ErrNotFound)
				emitted := f.recorder.snapshot()
				require.Len(t, emitted, 3)
				require.Equal(t, []string{model.EventTypeMemberAdded, model.EventTypeMemberUpdated, model.EventTypeInviteDeleted}, []string{emitted[0].Type(), emitted[1].Type(), emitted[2].Type()})
			})
		}
		t.Run("already member retains roles and targeted invitation", func(t *testing.T) {
			f := newInvitationFixture(t, store)
			membership := model.NewMembership(f.user.ID(), f.org.ID())
			require.NoError(t, store.AddMember(f.ctx, membership))
			require.NoError(t, store.SetMembershipRoles(f.ctx, membership.ID(), []model.RoleID{f.role.ID()}))
			for _, targeted := range []bool{true, false} {
				inv := f.invite(t, targeted, model.RoleOrgOwner, nil, nil)
				result, err := f.service.Accept(f.ctx, f.tenant.ID(), inv.ID(), f.user.ID())
				require.NoError(t, err)
				require.True(t, result.AlreadyMember)
				require.Len(t, result.Roles, 1)
				require.Equal(t, f.role.ID(), result.Roles[0].ID())
				after, err := store.GetInviteByID(f.ctx, inv.ID())
				require.NoError(t, err)
				require.Zero(t, after.UsesCount())
			}
			require.Empty(t, f.recorder.snapshot())
		})
		t.Run("open accept and decline", func(t *testing.T) {
			f := newInvitationFixture(t, store)
			inv := f.invite(t, false, "", nil, ptr(2))
			view, err := f.service.Prepare(f.ctx, f.tenant.ID(), inv.ID(), "")
			require.NoError(t, err)
			require.Equal(t, f.role.Name(), view.Role.Name())
			require.Equal(t, f.org.ID(), view.Org.ID())
			hide, err := f.service.Decline(f.ctx, f.tenant.ID(), inv.ID(), f.user.ID())
			require.NoError(t, err)
			require.True(t, hide)
			require.Empty(t, f.recorder.snapshot())
			_, err = f.service.Accept(f.ctx, f.tenant.ID(), inv.ID(), f.user.ID())
			require.NoError(t, err)
			after, err := store.GetInviteByID(f.ctx, inv.ID())
			require.NoError(t, err)
			require.Equal(t, 1, after.UsesCount())
		})
		// An address is the same recipient whatever its case: the invitation was
		// sent to "Recipient@…" while the account stores "recipient@…".
		t.Run("recipient case is ignored", func(t *testing.T) {
			f := newInvitationFixture(t, store)
			email := strings.ToUpper(f.user.Email())
			inv := model.NewInviteToken(f.org.ID(), string(f.role.ID()), &email, nil, nil, f.other.ID())
			require.NoError(t, store.CreateInvite(f.ctx, inv))
			require.NotEqual(t, *inv.InviteeEmail(), f.user.Email())
			require.NoError(t, store.SaveUser(f.ctx, f.user))
			pending, err := store.ListPendingInvitesForEmail(f.ctx, f.tenant.ID(), f.user.Email())
			require.NoError(t, err)
			require.Len(t, pending, 1)
			require.Equal(t, inv.ID(), pending[0].ID())
			view, err := f.service.Prepare(f.ctx, f.tenant.ID(), inv.ID(), f.user.ID())
			require.NoError(t, err)
			require.Equal(t, inv.ID(), view.Invite.ID())
			result, err := f.service.Accept(f.ctx, f.tenant.ID(), inv.ID(), f.user.ID())
			require.NoError(t, err)
			require.False(t, result.AlreadyMember)
			member, err := store.IsMember(f.ctx, f.user.ID(), f.org.ID())
			require.NoError(t, err)
			require.True(t, member)
		})
		t.Run("targeted decline", func(t *testing.T) {
			f := newInvitationFixture(t, store)
			inv := f.invite(t, true, "", nil, nil)
			hide, err := f.service.Decline(f.ctx, f.tenant.ID(), inv.ID(), f.user.ID())
			require.NoError(t, err)
			require.False(t, hide)
			_, err = store.GetInviteByID(f.ctx, inv.ID())
			require.ErrorIs(t, err, port.ErrNotFound)
			require.Len(t, f.recorder.snapshot(), 1)
		})
	})
}

var errInvitationInjected = errors.New("injected invitation failure")

type invitationTxWrapper struct {
	port.InvitationTransaction
	wrap func(port.InvitationTx) port.InvitationTx
}

func (w invitationTxWrapper) WithInvitationTransaction(ctx context.Context, fn func(port.InvitationTx) error) error {
	return w.InvitationTransaction.WithInvitationTransaction(ctx, func(tx port.InvitationTx) error { return fn(w.wrap(tx)) })
}

type failingInvitationTx struct {
	port.InvitationTx
	stage string
}

func (tx failingInvitationTx) InsertInvitationMember(ctx context.Context, m model.Membership) (bool, error) {
	created, err := tx.InvitationTx.InsertInvitationMember(ctx, m)
	if err == nil && tx.stage == "membership" {
		return false, errInvitationInjected
	}
	return created, err
}
func (tx failingInvitationTx) SetMembershipRoles(ctx context.Context, id model.MembershipID, roles []model.RoleID) error {
	err := tx.InvitationTx.SetMembershipRoles(ctx, id, roles)
	if err == nil && tx.stage == "role" {
		return errInvitationInjected
	}
	return err
}
func (tx failingInvitationTx) IncrementInviteUses(ctx context.Context, id model.InviteTokenID) error {
	err := tx.InvitationTx.IncrementInviteUses(ctx, id)
	if err == nil && tx.stage == "consume" {
		return errInvitationInjected
	}
	return err
}
func (tx failingInvitationTx) DeleteInvite(ctx context.Context, id model.InviteTokenID) error {
	err := tx.InvitationTx.DeleteInvite(ctx, id)
	if err == nil && tx.stage == "delete" {
		return errInvitationInjected
	}
	return err
}

func TestInvitationRollback(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newStoreOn(t, db)
		for _, stage := range []string{"membership", "role", "consume", "delete"} {
			t.Run(stage, func(t *testing.T) {
				f := newInvitationFixture(t, store)
				inv := f.invite(t, true, "", nil, nil)
				decorated := events.NewInvitationTransaction(store, f.recorder)
				svc := service.NewInvitationService(invitationTxWrapper{decorated, func(tx port.InvitationTx) port.InvitationTx { return failingInvitationTx{tx, stage} }})
				result, err := svc.Accept(f.ctx, f.tenant.ID(), inv.ID(), f.user.ID())
				require.ErrorIs(t, err, errInvitationInjected)
				require.Nil(t, result)
				member, err := store.IsMember(f.ctx, f.user.ID(), f.org.ID())
				require.NoError(t, err)
				require.False(t, member)
				after, err := store.GetInviteByID(f.ctx, inv.ID())
				require.NoError(t, err)
				require.Zero(t, after.UsesCount())
				var count int64
				require.NoError(t, db.Model(&xologorm.MembershipRole{}).Joins("JOIN roles ON roles.id = membership_roles.role_id").Where("roles.org_id = ?", string(f.org.ID())).Count(&count).Error)
				require.Zero(t, count)
				require.Empty(t, f.recorder.snapshot())
				if stage == "delete" {
					_, err := svc.Decline(f.ctx, f.tenant.ID(), inv.ID(), f.user.ID())
					require.ErrorIs(t, err, errInvitationInjected)
					_, err = store.GetInviteByID(f.ctx, inv.ID())
					require.NoError(t, err)
					require.Empty(t, f.recorder.snapshot())
				}
			})
		}
	})
}
