package service

import (
	"context"
	"errors"
	"slices"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

type AuthenticatedIdentity struct {
	Provider, Issuer, Subject, Email, DisplayName string
	EmailVerified                                 bool
}
type LoginPolicy struct {
	AutoCreate, ActiveByDefault, Managed bool
	DefaultAdmins                        []string
}

// ResolveAuthenticatedIdentity serializes declaration, email and existing-link
// resolution with provisioning. A conflicting identity never falls back to email.
func ResolveAuthenticatedIdentity(ctx context.Context, transactions port.ProvisioningTransaction, tid model.TenantID, proof AuthenticatedIdentity, policy LoginPolicy) (model.User, error) {
	var result model.User
	if proof.Provider == "" || proof.Subject == "" {
		return nil, port.ErrNotAllowed
	}
	err := transactions.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
		links, ok := tx.(port.IdentityLinkStore)
		if !ok {
			return errors.New("identity link store unavailable")
		}
		provider := proof.Provider
		if proof.Issuer != "" {
			provider = proof.Issuer
		}
		identity := model.Identity{Issuer: proof.Issuer, Subject: proof.Subject}
		var declared model.User
		var err error
		if proof.Issuer != "" {
			declared, err = links.GetUserByDeclaredIdentity(ctx, tid, identity)
			if err != nil && !errors.Is(err, port.ErrNotFound) {
				return err
			}
		}
		linked, err := tx.GetUserByIdentity(ctx, tid, provider, proof.Subject)
		if err != nil && !errors.Is(err, port.ErrNotFound) {
			return err
		}
		// Explicit mapping also permits migrating the existing local provider link.
		if linked == nil && provider != proof.Provider {
			linked, err = tx.GetUserByIdentity(ctx, tid, proof.Provider, proof.Subject)
			if err != nil && !errors.Is(err, port.ErrNotFound) {
				return err
			}
		}
		if declared != nil && linked != nil && declared.ID() != linked.ID() {
			return port.ErrAlreadyExists
		}
		user := linked
		if declared != nil {
			user = declared
		}
		if user != nil && user.DeclaredIdentity() != nil && *user.DeclaredIdentity() != identity {
			return port.ErrNotAllowed
		}
		if user == nil && proof.EmailVerified && proof.Email != "" {
			user, err = links.GetUserByEmail(ctx, tid, model.NormalizeEmail(proof.Email))
			if err != nil && !errors.Is(err, port.ErrNotFound) {
				return err
			}
			if user != nil && (user.DeclaredIdentity() != nil || user.Provider() != "") {
				return port.ErrAlreadyExists
			}
		}
		admin := proof.EmailVerified && slices.ContainsFunc(policy.DefaultAdmins, func(email string) bool { return model.NormalizeEmail(email) == model.NormalizeEmail(proof.Email) })
		app := proof.Provider == model.ApplicationProvider
		var next *model.BaseUser
		if user == nil {
			if !app && !admin && (policy.Managed || !policy.AutoCreate) {
				return port.ErrNotAllowed
			}
			next = model.NewUser(tid, provider, proof.Subject, proof.Email, proof.DisplayName, policy.ActiveByDefault || admin || app, model.PlatformRoleUser)
		} else {
			next = model.CopyUser(user)
			// Managed mode requires a pre-existing member (platform admins excepted).
			next.SetIdentity(provider, proof.Subject)
		}
		if !policy.Managed && user != nil {
			if proof.EmailVerified && proof.Email != "" {
				next.SetEmail(proof.Email)
			}
			if proof.DisplayName != "" {
				next.SetDisplayName(proof.DisplayName)
			}
		}
		if len(next.Roles()) == 0 {
			next.SetRoles(model.PlatformRoleUser)
		}
		if admin && !slices.Contains(next.Roles(), model.PlatformRoleAdmin) {
			next.SetRoles(append(next.Roles(), model.PlatformRoleAdmin)...)
			next.SetActive(true)
		}
		actor := model.ActorFromContext(ctx)
		actor.UserID = next.ID()
		actor.URI = ""
		loginCtx := model.WithActor(ctx, actor)
		if err := links.SaveAuthenticatedUser(loginCtx, next); err != nil {
			return err
		}
		result = next
		return nil
	})
	return result, err
}
