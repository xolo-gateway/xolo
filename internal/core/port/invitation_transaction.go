package port

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// InvitationTransaction runs a complete invitation operation atomically. The
// callback may be replayed on contention: it must not publish events or retain
// results from a failed attempt. Readers and writers share the same transaction.
type InvitationTransaction interface {
	WithInvitationTransaction(context.Context, func(InvitationTx) error) error
}

// InvitationTx exposes only the operations required by invitation workflows.
// Invitation, parent, user and role reads are protected against concurrent
// changes until commit. Implementations must not retry individual statements.
type InvitationTx interface {
	GetInviteByID(context.Context, model.InviteTokenID) (model.InviteToken, error)
	GetOrgByID(context.Context, model.OrgID) (model.Organization, error)
	GetUserByID(context.Context, model.UserID) (model.User, error)
	GetRoleByID(context.Context, model.RoleID) (model.Role, error)
	ListOrgRoles(context.Context, model.OrgID) ([]model.Role, error)
	GetUserOrgMembership(context.Context, model.UserID, model.OrgID) (model.Membership, error)
	GetMembership(context.Context, model.MembershipID) (model.Membership, error)
	CreateInvite(context.Context, model.InviteToken) error
	DeleteInvite(context.Context, model.InviteTokenID) error
	// IncrementInviteUses succeeds only when exactly one usable invitation was consumed.
	IncrementInviteUses(context.Context, model.InviteTokenID) error
	// InsertInvitationMember returns false on an existing (user, organization)
	// membership, without changing that membership or its roles.
	InsertInvitationMember(context.Context, model.Membership) (bool, error)
	SetMembershipRoles(context.Context, model.MembershipID, []model.RoleID) error
}
