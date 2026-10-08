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
	SetMembershipStatus(ctx context.Context, id model.MembershipID, status model.Status) error
}

// ProvisioningUserStore exposes only provisioning operations; all calls share the transaction.
type ProvisioningUserStore interface {
	FindOrCreateUser(ctx context.Context, tenantID model.TenantID, provider, subject string) (model.User, error)
	GetUserByID(ctx context.Context, userID model.UserID) (model.User, error)
	GetUserByIdentity(ctx context.Context, tenantID model.TenantID, provider, subject string) (model.User, error)
	GetUserByDeclaredIdentity(ctx context.Context, tenantID model.TenantID, identity model.Identity) (model.User, error)
	FindUsersByEmail(ctx context.Context, tenantID model.TenantID, email string, limit int) ([]model.User, error)
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

// ProvisioningBusinessStore exposes the business resources to provisioning;
// all calls share the transaction.
type ProvisioningBusinessStore interface {
	SetApplicationRoles(ctx context.Context, appID model.ApplicationID, roleIDs []model.RoleID) error
	CreateApplication(ctx context.Context, app model.Application) error
	UpdateApplication(ctx context.Context, app model.Application) error
	GetApplication(ctx context.Context, appID model.ApplicationID) (model.Application, error)
	SetQuota(ctx context.Context, quota model.Quota) error
	GetQuotaByID(ctx context.Context, id model.QuotaID) (model.Quota, error)
	GetQuota(ctx context.Context, scope model.QuotaScope, scopeID string) (model.Quota, error)
	CreateAlert(ctx context.Context, alert model.Alert) error
	UpdateAlert(ctx context.Context, alert model.Alert) error
	GetAlertByID(ctx context.Context, id model.AlertID) (model.Alert, error)
	CreateProvider(ctx context.Context, p model.Provider) error
	SaveProvider(ctx context.Context, p model.Provider) error
	GetProviderByID(ctx context.Context, id model.ProviderID) (model.Provider, error)
	GetLLMModelByID(ctx context.Context, id model.LLMModelID) (model.LLMModel, error)
	GetVirtualModelByID(ctx context.Context, id model.VirtualModelID) (model.VirtualModel, error)
}

// ProvisioningTx must not escape the callback or perform external effects.
type ProvisioningTx interface {
	ProvisioningTenantStore
	ProvisioningOrgStore
	ProvisioningUserStore
	ProvisioningRoleStore
	ProvisioningBusinessStore
	DomainStore
	// ReadProjection publishes the changes made so far in the transaction,
	// then reads the projection of one resource within it. A missing resource
	// or parent is ErrNotFound.
	ReadProjection(ctx context.Context, scope model.CommonScope, key string) (model.CommonItem, error)
}
