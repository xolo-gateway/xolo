package port

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

type OrgStore interface {
	// Organizations
	CreateOrg(ctx context.Context, org model.Organization) error
	GetOrgByID(ctx context.Context, id model.OrgID) (model.Organization, error)
	// GetOrgBySlug resolves an organization within a tenant. Slugs are only
	// unique per tenant, so the tenant is part of the key, never optional.
	GetOrgBySlug(ctx context.Context, tenantID model.TenantID, slug string) (model.Organization, error)
	ListOrgs(ctx context.Context, opts ListOrgsOptions) ([]model.Organization, int64, error)
	SaveOrg(ctx context.Context, org model.Organization) error
	DeleteOrg(ctx context.Context, id model.OrgID) error

	// Memberships
	AddMember(ctx context.Context, membership model.Membership) error
	RemoveMember(ctx context.Context, id model.MembershipID) error
	GetMembership(ctx context.Context, id model.MembershipID) (model.Membership, error)
	GetUserOrgMembership(ctx context.Context, userID model.UserID, orgID model.OrgID) (model.Membership, error)
	ListOrgMembers(ctx context.Context, orgID model.OrgID, opts ListOrgMembersOptions) ([]model.Membership, int64, error)
	GetUserMemberships(ctx context.Context, userID model.UserID) ([]model.Membership, error)
	IsMember(ctx context.Context, userID model.UserID, orgID model.OrgID) (bool, error)
}

type ListOrgsOptions struct {
	Page  *int
	Limit *int

	// TenantID restricts the listing to a single tenant. A nil value spans the
	// whole instance and must stay reserved for maintenance loops that
	// legitimately cross tenants.
	TenantID *model.TenantID
}

type ListOrgMembersOptions struct {
	Page  *int
	Limit *int
}
