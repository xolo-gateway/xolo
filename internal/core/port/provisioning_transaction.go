package port

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

type ProvisioningTransaction interface {
	WithProvisioningTransaction(context.Context, func(ProvisioningTx) error) error
}

// ProvisioningTenantStore exposes only provisioning operations; all calls share the transaction.
type ProvisioningTenantStore interface {
	CreateTenant(ctx context.Context, tenant model.Tenant) error
	GetTenantByID(ctx context.Context, id model.TenantID) (model.Tenant, error)
	GetTenantBySlug(ctx context.Context, slug string) (model.Tenant, error)
	ListTenants(ctx context.Context, opts ListTenantsOptions) ([]model.Tenant, int64, error)
	SaveTenant(ctx context.Context, tenant model.Tenant) error
	DeleteTenant(ctx context.Context, id model.TenantID) error
}

// ProvisioningOrgStore exposes only provisioning operations; all calls share the transaction.
type ProvisioningOrgStore interface {
	CreateOrg(ctx context.Context, org model.Organization) error
	GetOrgByID(ctx context.Context, id model.OrgID) (model.Organization, error)
	GetOrgBySlug(ctx context.Context, tenantID model.TenantID, slug string) (model.Organization, error)
	ListOrgs(ctx context.Context, opts ListOrgsOptions) ([]model.Organization, int64, error)
	SaveOrg(ctx context.Context, org model.Organization) error
	DeleteOrg(ctx context.Context, id model.OrgID) error
	AddMember(ctx context.Context, membership model.Membership) error
	RemoveMember(ctx context.Context, id model.MembershipID) error
	GetMembership(ctx context.Context, id model.MembershipID) (model.Membership, error)
	GetUserOrgMembership(ctx context.Context, userID model.UserID, orgID model.OrgID) (model.Membership, error)
	ListOrgMembers(ctx context.Context, orgID model.OrgID, opts ListOrgMembersOptions) ([]model.Membership, int64, error)
}

// ProvisioningUserStore exposes only provisioning operations; all calls share the transaction.
type ProvisioningUserStore interface {
	FindOrCreateUser(ctx context.Context, tenantID model.TenantID, provider, subject string) (model.User, error)
	GetUserByID(ctx context.Context, userID model.UserID) (model.User, error)
	GetUserByIdentity(ctx context.Context, tenantID model.TenantID, provider, subject string) (model.User, error)
	QueryUsers(ctx context.Context, opts QueryUsersOptions) ([]model.User, error)
	CountUsers(ctx context.Context, opts QueryUsersOptions) (int64, error)
	SaveUser(ctx context.Context, user model.User) error
}

// ProvisioningRoleStore exposes only provisioning operations; all calls share the transaction.
type ProvisioningRoleStore interface {
	CreateRole(ctx context.Context, role model.Role) error
	GetRoleByID(ctx context.Context, id model.RoleID) (model.Role, error)
	ListOrgRoles(ctx context.Context, orgID model.OrgID) ([]model.Role, error)
	SaveRole(ctx context.Context, role model.Role) error
	DeleteRole(ctx context.Context, id model.RoleID) error
	SetMembershipRoles(ctx context.Context, membershipID model.MembershipID, roleIDs []model.RoleID) error
	EnsureBuiltinRoles(ctx context.Context, orgID model.OrgID) error
}

// ProvisioningTx must not escape the callback or perform external effects.
type ProvisioningTx interface {
	ProvisioningTenantStore
	ProvisioningOrgStore
	ProvisioningUserStore
	ProvisioningRoleStore
	DomainStore
}
