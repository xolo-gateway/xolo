package service

import (
	"context"
	"log/slog"
	"slices"

	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

var (
	// ErrIdentityConflict refuses a sign-in that would bind an identity to an
	// account already bound to another one, or to an ambiguous email. Nothing
	// is merged nor reassigned.
	ErrIdentityConflict = errors.Wrap(port.ErrAlreadyExists, "identity conflict")

	// ErrAccountCreationDisabled refuses an identity no account matches while
	// the policy forbids creating one.
	ErrAccountCreationDisabled = errors.Wrap(port.ErrNotAllowed, "account creation disabled")
)

// errActorChanged reopens the transaction of a sign-in whose replay settled
// on another account than the read: the audit must name the right actor.
var errActorChanged = errors.New("sign-in resolved to another account")

const maxActorAttempts = 3

// AuthenticatedIdentity is what an authenticator proved about a sign-in.
type AuthenticatedIdentity struct {
	// Provider and Subject form the sign-in link: the local provider ID and
	// the subject it asserted.
	Provider string
	Subject  string
	// Issuer is the issuer the provider proved, empty when it proves none.
	// Only a proven issuer matches an identity declared by provisioning.
	Issuer string
	Email  string
	// EmailVerified tells that the provider asserted the email as verified.
	// Only a verified email can attach the sign-in to an existing account.
	EmailVerified bool
	DisplayName   string
}

// LoginPolicy decides which unknown identities get an account.
type LoginPolicy struct {
	// AutoCreate allows any unknown identity to get an account.
	AutoCreate bool
	// ActiveByDefault is the initial state of an account created at sign-in.
	ActiveByDefault bool
	// DefaultAdmins lists the emails granted the platform admin role.
	DefaultAdmins []string
}

// identityReader is the read side shared by the cached store and the
// provisioning transaction.
type identityReader interface {
	GetUserByIdentity(ctx context.Context, tenantID model.TenantID, provider, subject string) (model.User, error)
	GetUserByDeclaredIdentity(ctx context.Context, tenantID model.TenantID, identity model.Identity) (model.User, error)
	FindUsersByEmail(ctx context.Context, tenantID model.TenantID, email string, limit int) ([]model.User, error)
}

// IdentityResolver turns an authenticated identity into a Xolo account. The
// resolution is read-only whenever nothing changes, which is every request of
// an already linked account: no transaction, no lock. Creating, linking or
// updating an account runs in a provisioning transaction that replays the
// whole resolution before writing.
type IdentityResolver struct {
	users        port.UserStore
	invites      port.InviteStore
	transactions port.ProvisioningTransaction
}

func NewIdentityResolver(users port.UserStore, invites port.InviteStore, transactions port.ProvisioningTransaction) *IdentityResolver {
	return &IdentityResolver{users: users, invites: invites, transactions: transactions}
}

// Resolve returns the account of the identity, creating, linking or updating
// it as the policy allows.
func (r *IdentityResolver) Resolve(ctx context.Context, tenantID model.TenantID, proof AuthenticatedIdentity, policy LoginPolicy) (model.User, error) {
	if proof.Provider == "" || proof.Subject == "" {
		return nil, errors.Wrap(port.ErrNotAllowed, "identity without provider or subject")
	}
	// The invitation lookup goes through the ordinary store: it runs in the
	// read phase only, and the transaction reuses its answer. An invitation
	// only allows the account; consuming it is checked on its own.
	invited, checked := false, false
	isInvited := func() bool {
		if !checked {
			invited, checked = r.hasPendingInvite(ctx, tenantID, proof.Email), true
		}
		return invited
	}
	// Chosen once, so the replay creates the very account the read decided on.
	newID := model.NewUserID()
	user, next, err := r.decide(ctx, r.users, tenantID, proof, policy, isInvited, newID)
	if err != nil || next == nil {
		return user, err
	}
	if r.transactions == nil {
		return nil, errors.New("identity resolution can not write without a provisioning transaction")
	}

	// The audit names the account written as its actor, and reads the actor
	// when the transaction opens: when the replay settles on another account,
	// the transaction is reopened in its name.
	requestID := model.ActorFromContext(model.EnsureActor(ctx)).RequestID
	actorID := next.ID()
	for attempt := 0; ; attempt++ {
		actorCtx := model.WithActor(ctx, model.Actor{UserID: actorID, RequestID: requestID})
		err = r.transactions.WithProvisioningTransaction(actorCtx, func(tx port.ProvisioningTx) error {
			// Replayed on the transaction: a concurrent sign-in or
			// provisioning write may have changed the outcome since the read.
			user, next, err = r.decide(actorCtx, tx, tenantID, proof, policy, func() bool { return invited }, newID)
			if err != nil || next == nil {
				return err
			}
			if next.ID() != actorID {
				actorID = next.ID()
				return errActorChanged
			}
			user = next
			return tx.SaveUser(actorCtx, next)
		})
		if !errors.Is(err, errActorChanged) || attempt == maxActorAttempts-1 {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	return user, nil
}

// decide resolves the identity in order: its sign-in link, the identity
// provisioning declared, a verified email, then the creation policy. next is
// the account to write, nil when nothing changes.
func (r *IdentityResolver) decide(ctx context.Context, reader identityReader, tenantID model.TenantID, proof AuthenticatedIdentity, policy LoginPolicy, isInvited func() bool, newID model.UserID) (model.User, *model.BaseUser, error) {
	isDefaultAdmin := isDefaultAdminEmail(policy.DefaultAdmins, proof.Email)
	// An application authenticates through a shadow user created lazily on
	// its first request, whose lifecycle follows the application: the account
	// policy never applies to it.
	isApplication := proof.Provider == model.ApplicationProvider

	user, err := reader.GetUserByIdentity(ctx, tenantID, proof.Provider, proof.Subject)
	if err == nil {
		// The store keeps a declaration and the link of its account equal.
		// Refuse rather than trust a link a declaration contradicts.
		if declared := user.DeclaredIdentity(); declared != nil && (proof.Issuer != declared.Issuer || proof.Subject != declared.Subject) {
			return nil, nil, errors.WithStack(ErrIdentityConflict)
		}
		next := synchronizeProfile(user, proof, isDefaultAdmin, false)
		return user, next, nil
	}
	if !errors.Is(err, port.ErrNotFound) {
		return nil, nil, errors.WithStack(err)
	}

	if proof.Issuer != "" && !isApplication {
		declared, err := reader.GetUserByDeclaredIdentity(ctx, tenantID, model.Identity{Issuer: proof.Issuer, Subject: proof.Subject})
		if err == nil {
			next, err := attach(declared, proof, isDefaultAdmin)
			return declared, next, err
		}
		if !errors.Is(err, port.ErrNotFound) {
			return nil, nil, errors.WithStack(err)
		}
	}

	if proof.EmailVerified && model.NormalizeEmail(proof.Email) != "" && !isApplication {
		users, err := reader.FindUsersByEmail(ctx, tenantID, proof.Email, 2)
		if err != nil {
			return nil, nil, errors.WithStack(err)
		}
		switch len(users) {
		case 0:
		case 1:
			// A declared account waits for its own identity, never for
			// another one proving the same email.
			sameSignIn := users[0].Provider() == proof.Provider && users[0].Subject() == proof.Subject
			if users[0].DeclaredIdentity() != nil && !sameSignIn {
				return nil, nil, errors.WithStack(ErrIdentityConflict)
			}
			next, err := attach(users[0], proof, isDefaultAdmin)
			return users[0], next, err
		default:
			// Historical case variants: refusing is the only choice that
			// neither merges nor picks an account.
			return nil, nil, errors.Wrap(ErrIdentityConflict, "ambiguous email")
		}
	}

	// The identity authenticated successfully but Xolo knows nothing about
	// it. Default admins are the exception: they are the only way to
	// bootstrap an instance that has no user yet. So is an identity holding a
	// pending targeted invitation, pre-provisioned by definition: an
	// administrator named that address on purpose. Checked last so the lookup
	// only runs when it decides the outcome.
	if !policy.AutoCreate && !isDefaultAdmin && !isApplication && !isInvited() {
		return nil, nil, errors.WithStack(ErrAccountCreationDisabled)
	}

	// An invitation grants the account, not its activation: ActiveByDefault
	// keeps deciding that, as it does for any other identity.
	created := model.NewUser(
		tenantID,
		proof.Provider, proof.Subject, proof.Email, proof.DisplayName,
		policy.ActiveByDefault || isDefaultAdmin || isApplication,
		model.PlatformRoleUser,
	)
	created.SetID(newID)
	if next := synchronizeProfile(created, proof, isDefaultAdmin, true); next != nil {
		created = next
	}
	return created, created, nil
}

// attach links the sign-in to an account no sign-in is linked to yet. A linked
// account (an application's shadow user included) is never relinked, and a
// platform administrator is never attached to a new identity.
func attach(user model.User, proof AuthenticatedIdentity, isDefaultAdmin bool) (*model.BaseUser, error) {
	// Linked to this very sign-in by a concurrent one since the link lookup.
	if user.Provider() == proof.Provider && user.Subject() == proof.Subject {
		return synchronizeProfile(user, proof, isDefaultAdmin, false), nil
	}
	if user.Provider() != "" || user.Subject() != "" || isPlatformAdmin(user) {
		return nil, errors.WithStack(ErrIdentityConflict)
	}
	next := model.CopyUser(user)
	next.SetAuthenticationLink(proof.Provider, proof.Subject)
	// Forced because the new link is already a change to write, whatever the
	// profile; force never produces a write of its own.
	if synced := synchronizeProfile(next, proof, isDefaultAdmin, true); synced != nil {
		next = synced
	}
	return next, nil
}

// synchronizeProfile copies the profile the provider asserted onto the
// account and returns it, or nil when nothing changes and force is false. An
// empty asserted value never overwrites a stored one: some authenticators
// (OAuth2 introspection) resolve an identity without email or display name.
func synchronizeProfile(user model.User, proof AuthenticatedIdentity, isDefaultAdmin, force bool) *model.BaseUser {
	missingRole := len(user.Roles()) == 0
	shouldBeAdmin := isDefaultAdmin && !slices.Contains(user.Roles(), model.PlatformRoleAdmin)
	changed := (proof.DisplayName != "" && user.DisplayName() != proof.DisplayName) ||
		(proof.Email != "" && user.Email() != proof.Email)
	if !changed && !missingRole && !shouldBeAdmin && !force {
		return nil
	}

	next := model.CopyUser(user)
	if proof.DisplayName != "" {
		next.SetDisplayName(proof.DisplayName)
	}
	if proof.Email != "" {
		next.SetEmail(proof.Email)
	}
	if missingRole {
		next.SetRoles(model.PlatformRoleUser)
	}
	if shouldBeAdmin {
		next.SetRoles(append(next.Roles(), model.PlatformRoleAdmin)...)
		next.SetActive(true)
	}
	return next
}

// isDefaultAdminEmail compares both sides normalized (case, surrounding
// whitespace), like invitee addresses: an admin who writes
// Jean.Dupont@corp.tld while the provider returns jean.dupont@corp.tld must
// not be locked out of a fresh instance.
func isDefaultAdminEmail(admins []string, email string) bool {
	normalized := model.NormalizeEmail(email)
	return normalized != "" && slices.ContainsFunc(admins, func(admin string) bool {
		return model.NormalizeEmail(admin) == normalized
	})
}

// hasPendingInvite reports whether a still-acceptable targeted invitation names
// this e-mail inside this tenant. A lookup failure is never fatal: it only means
// the identity falls back to the configured provisioning policy.
func (r *IdentityResolver) hasPendingInvite(ctx context.Context, tenantID model.TenantID, email string) bool {
	if r.invites == nil || model.NormalizeEmail(email) == "" {
		return false
	}

	// The store only returns invitations issued by organizations of this
	// tenant: one issued elsewhere grants nothing here.
	invites, err := r.invites.ListPendingInvitesForEmail(ctx, tenantID, email)
	if err != nil {
		slog.ErrorContext(ctx, "could not list pending invites for identity", slogx.Error(err))
		return false
	}

	// The store already excludes revoked, expired and consumed invitations;
	// IsInviteValid repeats the domain rule rather than relying on the query.
	return slices.ContainsFunc(invites, model.IsInviteValid)
}
