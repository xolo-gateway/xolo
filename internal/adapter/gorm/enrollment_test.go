package gorm_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

const enrollmentIssuer = "https://id.example.test/"

type enrollmentFixture struct {
	store   *xologorm.Store
	service *service.EnrollmentService
	tenant  model.Tenant
	org     model.Organization
}

func newEnrollmentFixture(t *testing.T, store *xologorm.Store) *enrollmentFixture {
	t.Helper()
	suffix := string(model.NewOrgID())
	tenant := model.NewTenant("t-"+suffix, "T", "")
	require.NoError(t, store.CreateTenant(t.Context(), tenant))
	org := model.NewOrganization(tenant.ID(), "acme", "Acme", "")
	require.NoError(t, store.CreateOrg(t.Context(), org))
	require.NoError(t, store.EnsureBuiltinRoles(t.Context(), org.ID()))
	return &enrollmentFixture{store: store, service: service.NewEnrollmentService(store, store), tenant: tenant, org: org}
}

func (f *enrollmentFixture) rule(t *testing.T, kind model.EnrollmentKind, claim, value, role string) model.EnrollmentRule {
	t.Helper()
	rule := model.NewEnrollmentRule(f.org.ID(), "rule", kind, claim, value, role, true)
	require.NoError(t, f.service.SaveRule(t.Context(), f.tenant.ID(), rule, true))
	saved, err := f.service.GetRule(t.Context(), f.org.ID(), rule.ID())
	require.NoError(t, err)
	return saved
}

func (f *enrollmentFixture) user(t *testing.T, active bool) model.User {
	t.Helper()
	id := string(model.NewUserID())
	user := model.NewUser(f.tenant.ID(), "oidc", id, id+"@example.org", "", active, model.PlatformRoleUser)
	require.NoError(t, f.store.SaveUser(t.Context(), user))
	return user
}

func (f *enrollmentFixture) membership(t *testing.T, user model.User) model.Membership {
	t.Helper()
	membership, err := f.store.GetUserOrgMembership(t.Context(), user.ID(), f.org.ID())
	if err != nil {
		require.ErrorIs(t, err, port.ErrNotFound)
		return nil
	}
	return membership
}

func verified(user model.User) model.EnrollmentIdentity {
	return model.EnrollmentIdentity{Email: user.Email(), EmailVerified: true}
}

func fromIssuer(issuer string) model.EnrollmentIdentity {
	return model.EnrollmentIdentity{Claims: map[string][]string{"iss": {issuer}, "groups": {"staff", "ops"}}}
}

func TestEnrollmentRuleValidation(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		f := newEnrollmentFixture(t, store)
		for name, rule := range map[string]*model.BaseEnrollmentRule{
			"no name":          model.NewEnrollmentRule(f.org.ID(), " ", model.EnrollmentKindClaim, "iss", "x", model.RoleMember, true),
			"unknown kind":     model.NewEnrollmentRule(f.org.ID(), "r", "regex", "", "x", model.RoleMember, true),
			"not a domain":     model.NewEnrollmentRule(f.org.ID(), "r", model.EnrollmentKindEmailDomain, "", "localhost", model.RoleMember, true),
			"an address":       model.NewEnrollmentRule(f.org.ID(), "r", model.EnrollmentKindEmailDomain, "", "a@example.org", model.RoleMember, true),
			"domain and claim": model.NewEnrollmentRule(f.org.ID(), "r", model.EnrollmentKindEmailDomain, "groups", "example.org", model.RoleMember, true),
			"claim unnamed":    model.NewEnrollmentRule(f.org.ID(), "r", model.EnrollmentKindClaim, "", "x", model.RoleMember, true),
			"claim sans value": model.NewEnrollmentRule(f.org.ID(), "r", model.EnrollmentKindClaim, "iss", "", model.RoleMember, true),
			"no role":          model.NewEnrollmentRule(f.org.ID(), "r", model.EnrollmentKindClaim, "iss", "x", "", true),
			"owner":            model.NewEnrollmentRule(f.org.ID(), "r", model.EnrollmentKindClaim, "iss", "x", model.RoleOrgOwner, true),
			"unknown role":     model.NewEnrollmentRule(f.org.ID(), "r", model.EnrollmentKindClaim, "iss", "x", string(model.NewRoleID()), true),
		} {
			t.Run(name, func(t *testing.T) {
				require.ErrorIs(t, f.service.SaveRule(t.Context(), f.tenant.ID(), rule, true), port.ErrInvalid)
			})
		}

		t.Run("a role of another organization", func(t *testing.T) {
			other := model.NewOrganization(f.tenant.ID(), "other", "Other", "")
			require.NoError(t, store.CreateOrg(t.Context(), other))
			role := model.NewRole(other.ID(), "Reader", "")
			require.NoError(t, store.CreateRole(t.Context(), role))
			rule := model.NewEnrollmentRule(f.org.ID(), "r", model.EnrollmentKindClaim, "iss", "x", string(role.ID()), true)
			require.ErrorIs(t, f.service.SaveRule(t.Context(), f.tenant.ID(), rule, true), port.ErrInvalid)
		})

		t.Run("another tenant can not write a rule", func(t *testing.T) {
			rule := model.NewEnrollmentRule(f.org.ID(), "r", model.EnrollmentKindClaim, "iss", "x", model.RoleMember, true)
			require.ErrorIs(t, f.service.SaveRule(t.Context(), model.NewTenantID(), rule, true), port.ErrNotFound)
		})

		t.Run("a rule of another organization is not found", func(t *testing.T) {
			rule := f.rule(t, model.EnrollmentKindClaim, "iss", "x", model.RoleMember)
			_, err := f.service.GetRule(t.Context(), model.NewOrgID(), rule.ID())
			require.ErrorIs(t, err, port.ErrNotFound)
			require.ErrorIs(t, f.service.DeleteRule(t.Context(), f.tenant.ID(), model.NewOrgID(), rule.ID()), port.ErrNotFound)
		})

		t.Run("another tenant can not delete a rule", func(t *testing.T) {
			rule := f.rule(t, model.EnrollmentKindClaim, "iss", "x", model.RoleMember)
			require.ErrorIs(t, f.service.DeleteRule(t.Context(), model.NewTenantID(), f.org.ID(), rule.ID()), port.ErrNotFound)
			_, err := f.service.GetRule(t.Context(), f.org.ID(), rule.ID())
			require.NoError(t, err)
		})

		t.Run("a domain is stored normalized", func(t *testing.T) {
			rule := f.rule(t, model.EnrollmentKindEmailDomain, "", " @Example.ORG", model.RoleMember)
			require.Equal(t, "example.org", rule.Value())
		})

		// A provider sends no claim with spaces around it: such a rule would
		// never match.
		t.Run("a claim rule is stored trimmed", func(t *testing.T) {
			rule := f.rule(t, model.EnrollmentKindClaim, " groups ", " staff\t", model.RoleMember)
			require.Equal(t, "groups", rule.Claim())
			require.Equal(t, "staff", rule.Value())
			blank := model.NewEnrollmentRule(f.org.ID(), "r", model.EnrollmentKindClaim, "groups", "  ", model.RoleMember, true)
			require.ErrorIs(t, f.service.SaveRule(t.Context(), f.tenant.ID(), blank, true), port.ErrInvalid)
		})
	})
}

func TestEnrollmentMatching(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		f := newEnrollmentFixture(t, store)
		ctx := t.Context()
		f.rule(t, model.EnrollmentKindEmailDomain, "", "example.org", model.RoleMember)

		t.Run("an unverified domain does not match", func(t *testing.T) {
			user := f.user(t, true)
			enrollments, _, err := f.service.Enroll(ctx, f.tenant.ID(), user, model.EnrollmentIdentity{Email: user.Email()})
			require.NoError(t, err)
			require.Empty(t, enrollments)
			require.Nil(t, f.membership(t, user))
		})

		t.Run("another domain does not match", func(t *testing.T) {
			user := f.user(t, true)
			_, _, err := f.service.Enroll(ctx, f.tenant.ID(), user, model.EnrollmentIdentity{Email: "a@sub.example.org", EmailVerified: true})
			require.NoError(t, err)
			require.Nil(t, f.membership(t, user))
		})

		t.Run("a verified domain enrolls and activates a new account", func(t *testing.T) {
			user := f.user(t, false)
			identity := verified(user)
			identity.NewAccount = true
			enrollments, _, err := f.service.Enroll(ctx, f.tenant.ID(), user, identity)
			require.NoError(t, err)
			require.Len(t, enrollments, 1)
			require.True(t, enrollments[0].Activated)
			require.Equal(t, model.BuiltinKindMember, enrollments[0].Role.BuiltinKind())
			membership := f.membership(t, user)
			require.NotNil(t, membership)
			require.Len(t, membership.Roles(), 1)
			require.Equal(t, enrollments[0].Role.ID(), membership.Roles()[0].ID())
			account, err := store.GetUserByID(ctx, user.ID())
			require.NoError(t, err)
			require.True(t, account.Active())
		})

		// An inactive account that already existed may have been deactivated
		// on purpose: a rule created afterwards enrolls it, never activates it.
		t.Run("an existing inactive account stays inactive", func(t *testing.T) {
			user := f.user(t, false)
			enrollments, _, err := f.service.Enroll(ctx, f.tenant.ID(), user, verified(user))
			require.NoError(t, err)
			require.Len(t, enrollments, 1)
			require.False(t, enrollments[0].Activated)
			require.NotNil(t, f.membership(t, user))
			account, err := store.GetUserByID(ctx, user.ID())
			require.NoError(t, err)
			require.False(t, account.Active())
		})

		t.Run("a list claim matches one of its values", func(t *testing.T) {
			f := newEnrollmentFixture(t, store)
			role := model.NewRole(f.org.ID(), "Reader", "")
			require.NoError(t, store.CreateRole(ctx, role))
			f.rule(t, model.EnrollmentKindClaim, "groups", "ops", string(role.ID()))
			user := f.user(t, true)
			enrollments, _, err := f.service.Enroll(ctx, f.tenant.ID(), user, fromIssuer(enrollmentIssuer))
			require.NoError(t, err)
			require.Len(t, enrollments, 1)
			require.False(t, enrollments[0].Activated)
			require.Equal(t, role.ID(), f.membership(t, user).Roles()[0].ID())
		})

		t.Run("an issuer rule covers every account of the provider", func(t *testing.T) {
			f := newEnrollmentFixture(t, store)
			f.rule(t, model.EnrollmentKindClaim, "iss", enrollmentIssuer, model.RoleMember)
			matching, other := f.user(t, true), f.user(t, true)
			_, _, err := f.service.Enroll(ctx, f.tenant.ID(), matching, fromIssuer(enrollmentIssuer))
			require.NoError(t, err)
			_, _, err = f.service.Enroll(ctx, f.tenant.ID(), other, fromIssuer("https://other.example.test/"))
			require.NoError(t, err)
			require.NotNil(t, f.membership(t, matching))
			require.Nil(t, f.membership(t, other))
		})

		t.Run("a disabled rule does nothing", func(t *testing.T) {
			f := newEnrollmentFixture(t, store)
			rule := model.UpdateEnrollmentRule(f.rule(t, model.EnrollmentKindClaim, "iss", enrollmentIssuer, model.RoleMember),
				model.WithEnrollmentRuleEnabled(false))
			require.NoError(t, f.service.SaveRule(ctx, f.tenant.ID(), rule, false))
			user := f.user(t, true)
			_, _, err := f.service.Enroll(ctx, f.tenant.ID(), user, fromIssuer(enrollmentIssuer))
			require.NoError(t, err)
			require.Nil(t, f.membership(t, user))
		})

		t.Run("rules stay in their tenant", func(t *testing.T) {
			f := newEnrollmentFixture(t, store)
			foreign := newEnrollmentFixture(t, store)
			f.rule(t, model.EnrollmentKindClaim, "iss", enrollmentIssuer, model.RoleMember)
			user := foreign.user(t, true)
			_, _, err := foreign.service.Enroll(ctx, foreign.tenant.ID(), user, fromIssuer(enrollmentIssuer))
			require.NoError(t, err)
			require.Nil(t, f.membership(t, user))
			require.Nil(t, foreign.membership(t, user))
		})
	})
}

// What an administrator changes prevails over a rule.
func TestEnrollmentActsOnce(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := t.Context()
		// A service of its own forgets what it settled, like another replica.
		enroll := func(t *testing.T, f *enrollmentFixture, user model.User) []service.Enrollment {
			t.Helper()
			enrollments, _, err := service.NewEnrollmentService(store, store).Enroll(ctx, f.tenant.ID(), user, fromIssuer(enrollmentIssuer))
			require.NoError(t, err)
			return enrollments
		}

		t.Run("a removed member stays out", func(t *testing.T) {
			f := newEnrollmentFixture(t, store)
			f.rule(t, model.EnrollmentKindClaim, "iss", enrollmentIssuer, model.RoleMember)
			user := f.user(t, true)
			require.Len(t, enroll(t, f, user), 1)
			require.NoError(t, store.RemoveMember(ctx, f.membership(t, user).ID()))
			require.Empty(t, enroll(t, f, user))
			require.Nil(t, f.membership(t, user))
		})

		t.Run("a deactivated account stays inactive", func(t *testing.T) {
			f := newEnrollmentFixture(t, store)
			f.rule(t, model.EnrollmentKindClaim, "iss", enrollmentIssuer, model.RoleMember)
			user := f.user(t, false)
			require.Len(t, enroll(t, f, user), 1)
			account, err := store.GetUserByID(ctx, user.ID())
			require.NoError(t, err)
			next := model.CopyUser(account)
			next.SetActive(false)
			require.NoError(t, store.SaveUser(ctx, next))
			require.Empty(t, enroll(t, f, user))
			account, err = store.GetUserByID(ctx, user.ID())
			require.NoError(t, err)
			require.False(t, account.Active())
		})

		t.Run("a member keeps what they hold", func(t *testing.T) {
			f := newEnrollmentFixture(t, store)
			user := f.user(t, false)
			membership := model.NewMembership(user.ID(), f.org.ID())
			require.NoError(t, store.AddMember(ctx, membership))
			require.NoError(t, store.SetMembershipStatus(ctx, membership.ID(), model.StatusSuspended))
			f.rule(t, model.EnrollmentKindClaim, "iss", enrollmentIssuer, model.RoleOrgAdmin)
			require.Empty(t, enroll(t, f, user))
			kept := f.membership(t, user)
			require.Equal(t, model.StatusSuspended, kept.Status())
			require.Empty(t, kept.Roles())
			account, err := store.GetUserByID(ctx, user.ID())
			require.NoError(t, err)
			require.False(t, account.Active())
		})

		t.Run("deleting a rule keeps its memberships", func(t *testing.T) {
			f := newEnrollmentFixture(t, store)
			rule := f.rule(t, model.EnrollmentKindClaim, "iss", enrollmentIssuer, model.RoleMember)
			user := f.user(t, true)
			require.Len(t, enroll(t, f, user), 1)
			require.NoError(t, f.service.DeleteRule(ctx, f.tenant.ID(), f.org.ID(), rule.ID()))
			require.NotNil(t, f.membership(t, user))
			applied, err := store.ListAppliedEnrollmentRules(ctx, user.ID())
			require.NoError(t, err)
			require.Equal(t, []model.EnrollmentRuleID{rule.ID()}, applied)
		})

		// The organization remembers whom it enrolled, whichever rule did.
		t.Run("a removed member stays out of a rule created afterwards", func(t *testing.T) {
			f := newEnrollmentFixture(t, store)
			rule := f.rule(t, model.EnrollmentKindClaim, "iss", enrollmentIssuer, model.RoleMember)
			user := f.user(t, true)
			require.Len(t, enroll(t, f, user), 1)
			require.NoError(t, store.RemoveMember(ctx, f.membership(t, user).ID()))
			require.NoError(t, f.service.DeleteRule(ctx, f.tenant.ID(), f.org.ID(), rule.ID()))
			f.rule(t, model.EnrollmentKindClaim, "groups", "ops", model.RoleMember)
			require.Empty(t, enroll(t, f, user))
			require.Nil(t, f.membership(t, user))
		})

		t.Run("a role a rule grants can not be deleted", func(t *testing.T) {
			f := newEnrollmentFixture(t, store)
			role := model.NewRole(f.org.ID(), "Reader", "")
			require.NoError(t, store.CreateRole(ctx, role))
			rule := f.rule(t, model.EnrollmentKindClaim, "iss", enrollmentIssuer, string(role.ID()))
			require.ErrorIs(t, store.DeleteRole(ctx, role.ID()), port.ErrRoleGrantedByEnrollment)
			require.NoError(t, f.service.DeleteRule(ctx, f.tenant.ID(), f.org.ID(), rule.ID()))
			require.NoError(t, store.DeleteRole(ctx, role.ID()))
		})
	})
}

// A rule can no longer lose its role through the stores: the failure is
// forged in SQL.
func TestEnrollmentFailingRuleDoesNotBlockTheOthers(t *testing.T) {
	db := openSQLite(t, newSQLiteDSN(t))
	store := newSeededStore(t, db)
	f := newEnrollmentFixture(t, store)
	broken := f.rule(t, model.EnrollmentKindClaim, "iss", enrollmentIssuer, model.RoleMember)
	require.NoError(t, db.Exec("UPDATE enrollment_rules SET role = ? WHERE id = ?", string(model.NewRoleID()), string(broken.ID())).Error)
	f.rule(t, model.EnrollmentKindClaim, "groups", "ops", model.RoleMember)
	user := f.user(t, true)
	enrollments, _, err := service.NewEnrollmentService(store, store).Enroll(t.Context(), f.tenant.ID(), user, fromIssuer(enrollmentIssuer))
	require.ErrorIs(t, err, port.ErrInvalid)
	require.Len(t, enrollments, 1)
}

func TestEnrollmentConcurrentRequests(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		f := newEnrollmentFixture(t, store)
		f.rule(t, model.EnrollmentKindClaim, "iss", enrollmentIssuer, model.RoleMember)
		user := f.user(t, false)

		var (
			wg       sync.WaitGroup
			enrolled atomic.Int32
		)
		start := make(chan struct{})
		for range 4 {
			wg.Go(func() {
				<-start
				enrollments, _, err := service.NewEnrollmentService(store, store).Enroll(t.Context(), f.tenant.ID(), user, fromIssuer(enrollmentIssuer))
				if err != nil {
					t.Error(err)
				}
				enrolled.Add(int32(len(enrollments)))
			})
		}
		close(start)
		wg.Wait()

		require.EqualValues(t, 1, enrolled.Load())
		require.NotNil(t, f.membership(t, user))
	})
}

// The request that created the account activates it, even when a concurrent
// request, which did not know the account was new, enrolled it first.
func TestEnrollmentActivatesAnAccountAConcurrentRequestEnrolled(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := t.Context()
		f := newEnrollmentFixture(t, store)
		f.rule(t, model.EnrollmentKindClaim, "iss", enrollmentIssuer, model.RoleMember)
		user := f.user(t, false)

		enrollments, _, err := service.NewEnrollmentService(store, store).Enroll(ctx, f.tenant.ID(), user, fromIssuer(enrollmentIssuer))
		require.NoError(t, err)
		require.Len(t, enrollments, 1)
		require.False(t, enrollments[0].Activated)

		creating := fromIssuer(enrollmentIssuer)
		creating.NewAccount = true
		enrollments, activated, err := service.NewEnrollmentService(store, store).Enroll(ctx, f.tenant.ID(), user, creating)
		require.NoError(t, err)
		require.Empty(t, enrollments)
		require.True(t, activated)
		account, err := store.GetUserByID(ctx, user.ID())
		require.NoError(t, err)
		require.True(t, account.Active())
	})
}

// The sign-in that created an account activates it once a member, even when
// the rule that matches does not enroll it.
func TestEnrollmentActivatesANewMemberItDoesNotEnroll(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := t.Context()
		creating := fromIssuer(enrollmentIssuer)
		creating.NewAccount = true

		t.Run("already a member", func(t *testing.T) {
			f := newEnrollmentFixture(t, store)
			f.rule(t, model.EnrollmentKindClaim, "iss", enrollmentIssuer, model.RoleOrgAdmin)
			user := f.user(t, false)
			require.NoError(t, store.AddMember(ctx, model.NewMembership(user.ID(), f.org.ID())))
			enrollments, activated, err := service.NewEnrollmentService(store, store).Enroll(ctx, f.tenant.ID(), user, creating)
			require.NoError(t, err)
			require.Empty(t, enrollments)
			require.True(t, activated)
			require.Empty(t, f.membership(t, user).Roles())
			account, err := store.GetUserByID(ctx, user.ID())
			require.NoError(t, err)
			require.True(t, account.Active())
		})

		t.Run("enrolled by another rule of the organization", func(t *testing.T) {
			f := newEnrollmentFixture(t, store)
			first := f.rule(t, model.EnrollmentKindClaim, "iss", enrollmentIssuer, model.RoleMember)
			user := f.user(t, false)
			enrollments, activated, err := service.NewEnrollmentService(store, store).Enroll(ctx, f.tenant.ID(), user, fromIssuer(enrollmentIssuer))
			require.NoError(t, err)
			require.Len(t, enrollments, 1)
			require.False(t, activated)
			require.NoError(t, f.service.DeleteRule(ctx, f.tenant.ID(), f.org.ID(), first.ID()))
			f.rule(t, model.EnrollmentKindClaim, "groups", "ops", model.RoleMember)

			enrollments, activated, err = service.NewEnrollmentService(store, store).Enroll(ctx, f.tenant.ID(), user, creating)
			require.NoError(t, err)
			require.Empty(t, enrollments)
			require.True(t, activated)
			account, err := store.GetUserByID(ctx, user.ID())
			require.NoError(t, err)
			require.True(t, account.Active())
		})

		t.Run("not a member", func(t *testing.T) {
			f := newEnrollmentFixture(t, store)
			first := f.rule(t, model.EnrollmentKindClaim, "iss", enrollmentIssuer, model.RoleMember)
			user := f.user(t, false)
			_, _, err := service.NewEnrollmentService(store, store).Enroll(ctx, f.tenant.ID(), user, fromIssuer(enrollmentIssuer))
			require.NoError(t, err)
			require.NoError(t, store.RemoveMember(ctx, f.membership(t, user).ID()))
			require.NoError(t, f.service.DeleteRule(ctx, f.tenant.ID(), f.org.ID(), first.ID()))
			f.rule(t, model.EnrollmentKindClaim, "groups", "ops", model.RoleMember)

			_, activated, err := service.NewEnrollmentService(store, store).Enroll(ctx, f.tenant.ID(), user, creating)
			require.NoError(t, err)
			require.False(t, activated)
			account, err := store.GetUserByID(ctx, user.ID())
			require.NoError(t, err)
			require.False(t, account.Active())
		})
	})
}

// A rule that failed on the sign-in that created the account still activates
// it on a later request.
func TestEnrollmentRetryActivatesANewAccount(t *testing.T) {
	db := openSQLite(t, newSQLiteDSN(t))
	store := newSeededStore(t, db)
	ctx := t.Context()
	f := newEnrollmentFixture(t, store)
	broken := f.rule(t, model.EnrollmentKindClaim, "iss", enrollmentIssuer, model.RoleMember)
	require.NoError(t, db.Exec("UPDATE enrollment_rules SET role = ? WHERE id = ?", string(model.NewRoleID()), string(broken.ID())).Error)
	user := f.user(t, false)

	creating := fromIssuer(enrollmentIssuer)
	creating.NewAccount = true
	_, _, err := f.service.Enroll(ctx, f.tenant.ID(), user, creating)
	require.ErrorIs(t, err, port.ErrInvalid)

	f.rule(t, model.EnrollmentKindClaim, "groups", "ops", model.RoleMember)
	enrollments, _, err := f.service.Enroll(ctx, f.tenant.ID(), user, fromIssuer(enrollmentIssuer))
	require.NoError(t, err)
	require.Len(t, enrollments, 1)
	require.True(t, enrollments[0].Activated)
}

// The retry may reach another replica, which never saw the account created.
func TestEnrollmentRetryOnAnotherReplicaActivatesANewAccount(t *testing.T) {
	db := openSQLite(t, newSQLiteDSN(t))
	store := newSeededStore(t, db)
	ctx := t.Context()
	f := newEnrollmentFixture(t, store)
	rule := f.rule(t, model.EnrollmentKindClaim, "iss", enrollmentIssuer, model.RoleMember)
	role := rule.Role()
	require.NoError(t, db.Exec("UPDATE enrollment_rules SET role = ? WHERE id = ?", string(model.NewRoleID()), string(rule.ID())).Error)
	user := f.user(t, false)

	creating := fromIssuer(enrollmentIssuer)
	creating.NewAccount = true
	_, _, err := f.service.Enroll(ctx, f.tenant.ID(), user, creating)
	require.ErrorIs(t, err, port.ErrInvalid)

	require.NoError(t, db.Exec("UPDATE enrollment_rules SET role = ? WHERE id = ?", role, string(rule.ID())).Error)
	enrollments, _, err := service.NewEnrollmentService(store, store).Enroll(ctx, f.tenant.ID(), user, fromIssuer(enrollmentIssuer))
	require.NoError(t, err)
	require.Len(t, enrollments, 1)
	require.True(t, enrollments[0].Activated)

	pending, err := store.IsEnrollmentActivationPending(ctx, user.ID(), time.Time{})
	require.NoError(t, err)
	require.False(t, pending)
}

// Once a rule activated the account, another one retried later leaves a
// deactivation alone.
func TestEnrollmentRetryKeepsADeactivation(t *testing.T) {
	db := openSQLite(t, newSQLiteDSN(t))
	store := newSeededStore(t, db)
	ctx := t.Context()
	f := newEnrollmentFixture(t, store)
	f.rule(t, model.EnrollmentKindClaim, "groups", "ops", model.RoleMember)
	other := model.NewOrganization(f.tenant.ID(), "other", "Other", "")
	require.NoError(t, store.CreateOrg(ctx, other))
	require.NoError(t, store.EnsureBuiltinRoles(ctx, other.ID()))
	broken := model.NewEnrollmentRule(other.ID(), "rule", model.EnrollmentKindClaim, "iss", enrollmentIssuer, model.RoleMember, true)
	require.NoError(t, f.service.SaveRule(ctx, f.tenant.ID(), broken, true))
	require.NoError(t, db.Exec("UPDATE enrollment_rules SET role = ? WHERE id = ?", string(model.NewRoleID()), string(broken.ID())).Error)
	user := f.user(t, false)

	creating := fromIssuer(enrollmentIssuer)
	creating.NewAccount = true
	enrollments, _, err := f.service.Enroll(ctx, f.tenant.ID(), user, creating)
	require.ErrorIs(t, err, port.ErrInvalid)
	require.Len(t, enrollments, 1)
	require.True(t, enrollments[0].Activated)

	account, err := store.GetUserByID(ctx, user.ID())
	require.NoError(t, err)
	deactivated := model.CopyUser(account)
	deactivated.SetActive(false)
	require.NoError(t, store.SaveUser(ctx, deactivated))

	require.NoError(t, db.Exec("UPDATE enrollment_rules SET role = ? WHERE id = ?", model.RoleMember, string(broken.ID())).Error)
	enrollments, _, err = service.NewEnrollmentService(store, store).Enroll(ctx, f.tenant.ID(), deactivated, fromIssuer(enrollmentIssuer))
	require.NoError(t, err)
	require.Len(t, enrollments, 1)
	require.False(t, enrollments[0].Activated)
	account, err = store.GetUserByID(ctx, user.ID())
	require.NoError(t, err)
	require.False(t, account.Active())
}

// A rule another replica disabled is not settled: enabled again, it acts.
func TestEnrollmentReenabledRuleActs(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := t.Context()
		f := newEnrollmentFixture(t, store)
		rule := f.rule(t, model.EnrollmentKindClaim, "iss", enrollmentIssuer, model.RoleMember)
		_, err := f.service.ComparedClaims(ctx, f.tenant.ID())
		require.NoError(t, err)

		replica := service.NewEnrollmentService(store, store)
		require.NoError(t, replica.SaveRule(ctx, f.tenant.ID(), model.UpdateEnrollmentRule(rule, model.WithEnrollmentRuleEnabled(false)), false))
		user := f.user(t, true)
		enrollments, _, err := f.service.Enroll(ctx, f.tenant.ID(), user, fromIssuer(enrollmentIssuer))
		require.NoError(t, err)
		require.Empty(t, enrollments)

		require.NoError(t, replica.SaveRule(ctx, f.tenant.ID(), model.UpdateEnrollmentRule(rule, model.WithEnrollmentRuleEnabled(true)), false))
		enrollments, _, err = f.service.Enroll(ctx, f.tenant.ID(), user, fromIssuer(enrollmentIssuer))
		require.NoError(t, err)
		require.Len(t, enrollments, 1)
	})
}

func TestEnrollmentComparedClaims(t *testing.T) {
	store := newTestStore(t)
	f := newEnrollmentFixture(t, store)
	f.rule(t, model.EnrollmentKindEmailDomain, "", "example.org", model.RoleMember)
	f.rule(t, model.EnrollmentKindClaim, "groups", "ops", model.RoleMember)
	f.rule(t, model.EnrollmentKindClaim, "groups", "staff", model.RoleMember)
	f.rule(t, model.EnrollmentKindClaim, "groups", "ops", model.RoleOrgAdmin)
	compared, err := f.service.ComparedClaims(t.Context(), f.tenant.ID())
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"groups": {"ops", "staff"}}, compared)
}

// countingEnrollmentStore counts the queries a request costs.
type countingEnrollmentStore struct {
	port.EnrollmentStore
	rules, applied atomic.Int32
}

func (s *countingEnrollmentStore) ListActiveEnrollmentRules(ctx context.Context, tenantID model.TenantID) ([]model.EnrollmentRule, error) {
	s.rules.Add(1)
	return s.EnrollmentStore.ListActiveEnrollmentRules(ctx, tenantID)
}

func (s *countingEnrollmentStore) ListAppliedEnrollmentRules(ctx context.Context, userID model.UserID) ([]model.EnrollmentRuleID, error) {
	s.applied.Add(1)
	return s.EnrollmentStore.ListAppliedEnrollmentRules(ctx, userID)
}

// A bearer token comes through on every request: once settled, it costs no
// query.
func TestEnrollmentSettledRequestsCostNothing(t *testing.T) {
	store := newTestStore(t)
	f := newEnrollmentFixture(t, store)
	f.rule(t, model.EnrollmentKindClaim, "iss", enrollmentIssuer, model.RoleMember)
	counting := &countingEnrollmentStore{EnrollmentStore: store}
	svc := service.NewEnrollmentService(counting, store)
	user := f.user(t, true)
	for range 5 {
		_, _, err := svc.Enroll(t.Context(), f.tenant.ID(), user, fromIssuer(enrollmentIssuer))
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, counting.rules.Load())
	require.EqualValues(t, 1, counting.applied.Load())
}

func TestEnrollmentRuleProjection(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		f := newEnrollmentFixture(t, store)
		rule := f.rule(t, model.EnrollmentKindClaim, "iss", enrollmentIssuer, model.RoleMember)
		scope := model.CommonScope{Family: model.FamilyEnrollmentRule, TenantID: string(f.tenant.ID()), OrganizationID: string(f.org.ID())}
		item, err := store.ReadProjection(t.Context(), scope, string(rule.ID()))
		require.NoError(t, err)
		require.JSONEq(t, `{"name":"rule","kind":"claim","claim":"iss","value":"`+enrollmentIssuer+`","role":"member","enabled":true}`, string(item.Representation))
		require.NoError(t, f.service.DeleteRule(t.Context(), f.tenant.ID(), f.org.ID(), rule.ID()))
		_, err = store.ReadProjection(t.Context(), scope, string(rule.ID()))
		require.ErrorIs(t, err, port.ErrNotFound)
	})
}

// Deleting an organization or an account leaves nothing behind.
func TestEnrollmentCascades(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := t.Context()
		f := newEnrollmentFixture(t, store)
		f.rule(t, model.EnrollmentKindClaim, "iss", enrollmentIssuer, model.RoleMember)
		kept, gone := f.user(t, true), f.user(t, true)
		for _, user := range []model.User{kept, gone} {
			_, _, err := f.service.Enroll(ctx, f.tenant.ID(), user, fromIssuer(enrollmentIssuer))
			require.NoError(t, err)
		}

		require.NoError(t, store.DeleteUser(ctx, gone.ID()))
		applied, err := store.ListAppliedEnrollmentRules(ctx, gone.ID())
		require.NoError(t, err)
		require.Empty(t, applied)

		require.NoError(t, store.DeleteOrg(ctx, f.org.ID()))
		applied, err = store.ListAppliedEnrollmentRules(ctx, kept.ID())
		require.NoError(t, err)
		require.Empty(t, applied)
		rules, err := store.ListEnrollmentRules(ctx, f.org.ID())
		require.NoError(t, err)
		require.Empty(t, rules)
	})
}
