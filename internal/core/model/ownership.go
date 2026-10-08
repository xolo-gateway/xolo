package model

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Owner is the write authority of a family of resources.
type Owner string

const (
	// OwnerShared lets both the local instance and the control plane write.
	OwnerShared Owner = "shared"
	// OwnerLocal reserves writes to the local instance: web UI, sign-in,
	// invitations.
	OwnerLocal Owner = "local"
	// OwnerControlPlane reserves writes to the provisioning API.
	OwnerControlPlane Owner = "control_plane"
)

// FamilySubscription designates the webhook subscriptions of a tenant. It
// is not part of the common contract, but has a write authority of its own.
const FamilySubscription = "subscription"

// OwnershipFamilies lists the families a policy can assign.
var OwnershipFamilies = []string{
	FamilyTenant,
	FamilyTenantDomain,
	FamilyOrganization,
	FamilyMember,
	FamilyOrganizationMembership,
	FamilySubscription,
	FamilyCustomRole,
	FamilyApplication,
	FamilyQuota,
	FamilyAlert,
	FamilyProvider,
}

// OwnershipPolicy assigns a write authority to each family. It is fixed at
// startup: every replica of an instance must run the same policy.
type OwnershipPolicy map[string]Owner

// Effective returns the policy with every family assigned, the omitted ones
// shared.
func (p OwnershipPolicy) Effective() OwnershipPolicy {
	out := make(OwnershipPolicy, len(OwnershipFamilies))
	for _, family := range OwnershipFamilies {
		out[family] = OwnerShared
	}
	for family, owner := range p {
		out[family] = owner
	}
	return out
}

// Validate refuses an unknown family or owner.
func (p OwnershipPolicy) Validate() error {
	for family, owner := range p {
		if !slices.Contains(OwnershipFamilies, family) {
			return fmt.Errorf("invalid ownership policy: unknown family %q", family)
		}
		if owner != OwnerShared && owner != OwnerLocal && owner != OwnerControlPlane {
			return fmt.Errorf("invalid ownership policy: unknown owner %q for family %q", owner, family)
		}
	}
	return nil
}

// Allows tells whether authority may write the family. A nil policy allows
// everything: offline operator tools and migrations hold database authority.
func (p OwnershipPolicy) Allows(family string, authority Owner) bool {
	if p == nil {
		return true
	}
	owner, ok := p[family]
	if !ok || owner == OwnerShared {
		return true
	}
	return owner == authority
}

// String renders the policy in the XOLO_OWNERSHIP syntax, families sorted.
func (p OwnershipPolicy) String() string {
	parts := make([]string, 0, len(p))
	for family, owner := range p {
		parts = append(parts, family+"="+string(owner))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

type writeAuthorityKey struct{}

// WithWriteAuthority sets the authority of the writes made with ctx. Only
// trusted transport code sets it, never a payload.
func WithWriteAuthority(ctx context.Context, owner Owner) context.Context {
	return context.WithValue(ctx, writeAuthorityKey{}, owner)
}

// WriteAuthority returns the authority of the writes made with ctx, local
// unless the transport said otherwise.
func WriteAuthority(ctx context.Context) Owner {
	if owner, ok := ctx.Value(writeAuthorityKey{}).(Owner); ok {
		return owner
	}
	return OwnerLocal
}

type signInAccountKey struct{}

// WithSignInAccount designates the account a sign-in is resolving. The
// resolution may write that account even when members belong to the control
// plane: it links the proven identity and bootstraps default admins, never
// anything else.
func WithSignInAccount(ctx context.Context, id UserID) context.Context {
	return context.WithValue(ctx, signInAccountKey{}, id)
}

// SignInAccount returns the account designated by WithSignInAccount.
func SignInAccount(ctx context.Context) (UserID, bool) {
	id, ok := ctx.Value(signInAccountKey{}).(UserID)
	return id, ok && id != ""
}
