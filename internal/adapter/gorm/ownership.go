package gorm

import (
	"context"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// OwnershipPolicy returns the write authority of each family, nil when the
// store checks none.
func (s *Store) OwnershipPolicy() model.OwnershipPolicy {
	return s.ownership
}

// checkOwnership refuses a write to family when the policy reserves it to
// another authority than the one of ctx. Writers whose effect has no
// projection (invitations, webhook subscriptions) call it themselves; every
// other identity write is checked when its projections are published.
func (s *Store) checkOwnership(ctx context.Context, family string) error {
	if !s.ownership.Allows(family, model.WriteAuthority(ctx)) {
		return errors.Wrapf(port.ErrOwnershipDenied, "%s", family)
	}
	return nil
}

// authorizeProjections checks the write authority of every projection a
// transaction changes, cascades included: the transaction rolls back as a
// whole when one of them belongs to another authority. Only the public
// representation is governed, so the fields outside the common contract
// (platform roles, organization settings, custom roles) stay local.
//
// A sign-in may write the very account it resolves, so that a member owned
// by the control plane can still link its proven identity.
func authorizeProjections(ctx context.Context, ownership model.OwnershipPolicy, changes []projectionChange) error {
	if ownership == nil {
		return nil
	}
	authority := model.WriteAuthority(ctx)
	signIn, signingIn := model.SignInAccount(ctx)
	for _, change := range changes {
		family := change.id.family
		if ownership.Allows(family, authority) {
			continue
		}
		if signingIn && authority == model.OwnerLocal && family == model.FamilyMember && change.id.key == string(signIn) {
			continue
		}
		return errors.Wrapf(port.ErrOwnershipDenied, "%s", family)
	}
	return nil
}
