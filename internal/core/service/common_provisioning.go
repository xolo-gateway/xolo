package service

import (
	"context"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// CommonResource is the common representation of a tenant or an
// organization. It holds only the fields governed by the common contract:
// descriptions, currencies and other Xolo settings are left untouched.
type CommonResource struct {
	Slug   string
	Name   string
	Status model.Status
}

// CommonMember is the common representation of a tenant member. The
// authentication identity and the platform roles are not part of it: a PUT
// never changes them.
type CommonMember struct {
	Email       string
	DisplayName string
	TenantRole  model.TenantRole
	Status      model.Status
}

// CommonMembership is the common representation of an organization membership.
type CommonMembership struct {
	Role   model.MembershipRole
	Status model.Status
}

const (
	maxCommonNameLength  = 200
	maxCommonEmailLength = 320
)

// validCommonText accepts valid UTF-8 without control characters, whose
// length in bytes lies within [min, max].
func validCommonText(s string, min, max int) bool {
	if !utf8.ValidString(s) || len(s) < min || len(s) > max {
		return false
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

func normalizeCommonResource(p CommonResource) (CommonResource, error) {
	p.Slug = strings.ToLower(strings.TrimSpace(p.Slug))
	p.Name = strings.TrimSpace(p.Name)
	if !model.IsValidSlug(p.Slug) || !validCommonText(p.Name, 1, maxCommonNameLength) || !p.Status.Valid() {
		return p, errors.WithStack(port.ErrInvalid)
	}
	return p, nil
}

// commonParentTenant loads the tenant a common resource hangs from.
func (s *ProvisioningService) commonParentTenant(ctx context.Context, tenantID model.TenantID) (model.Tenant, error) {
	tenant, err := s.tenantStore.GetTenantByID(ctx, tenantID)
	if errors.Is(err, port.ErrNotFound) {
		return nil, errors.Wrap(port.ErrParentNotFound, "tenant not found")
	}
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return tenant, nil
}

// PutTenant creates the tenant or brings it to the given representation. A
// write identical to the stored state changes nothing. A second tenant is
// refused on a single-tenant instance, and the default tenant keeps its slug
// and stays active: every single-tenant instance resolves to it.
func (s *ProvisioningService) PutTenant(ctx context.Context, tenantID model.TenantID, p CommonResource) (CommonResource, error) {
	p, err := normalizeCommonResource(p)
	if err != nil {
		return p, err
	}
	if !s.bound {
		ctx = model.EnsureActor(ctx)
		return p, s.transaction(ctx, func(tx *ProvisioningService) error {
			_, err := tx.PutTenant(ctx, tenantID, p)
			return err
		})
	}

	old, err := s.tenantStore.GetTenantByID(ctx, tenantID)
	if errors.Is(err, port.ErrNotFound) {
		if !s.multiTenant {
			return p, errors.Wrap(port.ErrNotAllowed, "this instance is single-tenant")
		}
		tenant := model.NewTenant(p.Slug, p.Name, "")
		tenant.SetID(tenantID)
		return p, errors.WithStack(s.tenantStore.CreateTenant(ctx, model.UpdateTenant(tenant, model.WithTenantActive(p.Status == model.StatusActive))))
	}
	if err != nil {
		return p, errors.WithStack(err)
	}
	if old.Slug() == p.Slug && old.Name() == p.Name && model.DeclaredStatus(old.Active()) == p.Status {
		return p, nil
	}
	if old.Slug() == model.DefaultTenantSlug && (p.Slug != old.Slug() || p.Status != model.StatusActive) {
		return p, errors.Wrap(port.ErrNotAllowed, "the default tenant keeps its slug and stays active")
	}
	updated := model.UpdateTenant(old, model.WithTenantSlug(p.Slug), model.WithTenantName(p.Name), model.WithTenantActive(p.Status == model.StatusActive))
	return p, errors.WithStack(s.tenantStore.SaveTenant(ctx, updated))
}

// PutDomain declares hostname as a domain of the tenant, or changes its status.
// A hostname belongs to at most one tenant.
func (s *ProvisioningService) PutDomain(ctx context.Context, tenantID model.TenantID, hostname string, status model.Status) (model.Status, error) {
	if !status.Valid() {
		return status, errors.WithStack(port.ErrInvalid)
	}
	host, err := model.NormalizeHostname(hostname)
	if err != nil || host != hostname {
		return status, errors.WithStack(port.ErrInvalidHostname)
	}
	if !s.bound {
		ctx = model.EnsureActor(ctx)
		return status, s.transaction(ctx, func(tx *ProvisioningService) error {
			_, err := tx.PutDomain(ctx, tenantID, hostname, status)
			return err
		})
	}

	if _, err := s.commonParentTenant(ctx, tenantID); err != nil {
		return status, err
	}
	old, err := s.domainStore.GetDomain(ctx, host)
	switch {
	case errors.Is(err, port.ErrNotFound):
	case err != nil:
		return status, errors.WithStack(err)
	case old.TenantID != tenantID:
		return status, errors.Wrap(port.ErrAlreadyExists, "hostname belongs to another tenant")
	case old.Status == status:
		return status, nil
	}
	return status, errors.WithStack(s.domainStore.SaveDomain(ctx, model.Domain{Hostname: host, TenantID: tenantID, Status: status}))
}

// PutOrganization creates the organization, with its builtin roles, or brings
// it to the given representation.
func (s *ProvisioningService) PutOrganization(ctx context.Context, tenantID model.TenantID, orgID model.OrgID, p CommonResource) (CommonResource, error) {
	p, err := normalizeCommonResource(p)
	if err != nil {
		return p, err
	}
	if !s.bound {
		ctx = model.EnsureActor(ctx)
		return p, s.transaction(ctx, func(tx *ProvisioningService) error {
			_, err := tx.PutOrganization(ctx, tenantID, orgID, p)
			return err
		})
	}

	if _, err := s.commonParentTenant(ctx, tenantID); err != nil {
		return p, err
	}
	old, err := s.orgStore.GetOrgByID(ctx, orgID)
	if errors.Is(err, port.ErrNotFound) {
		org := model.NewOrganization(tenantID, p.Slug, p.Name, "")
		org.SetID(orgID)
		if err := s.orgStore.CreateOrg(ctx, model.UpdateOrganization(org, model.WithOrgActive(p.Status == model.StatusActive))); err != nil {
			return p, errors.WithStack(err)
		}
		return p, errors.WithStack(s.roleStore.EnsureBuiltinRoles(ctx, orgID))
	}
	if err != nil {
		return p, errors.WithStack(err)
	}
	// Organization IDs are global: another tenant's organization is a
	// conflict, never something this tenant may overwrite.
	if old.TenantID() != tenantID {
		return p, errors.Wrap(port.ErrAlreadyExists, "organization id is used by another tenant")
	}
	if old.Slug() == p.Slug && old.Name() == p.Name && model.DeclaredStatus(old.Active()) == p.Status {
		return p, nil
	}
	updated := model.UpdateOrganization(old, model.WithOrgSlug(p.Slug), model.WithOrgName(p.Name), model.WithOrgActive(p.Status == model.StatusActive))
	return p, errors.WithStack(s.orgStore.SaveOrg(ctx, updated))
}

// PutTenantMember brings an existing user of the tenant to the given
// representation. Its authentication identity and platform roles are always
// preserved. A platform administrator is never modified: provisioning does not
// act on platform-wide privileges.
//
// An unknown user is not created: until an identity can be declared with the
// member, an account without one would block that person's first sign-in on
// its email. Members are provisioned once they have signed in.
func (s *ProvisioningService) PutTenantMember(ctx context.Context, tenantID model.TenantID, userID model.UserID, p CommonMember) (CommonMember, error) {
	p.Email = strings.TrimSpace(p.Email)
	p.DisplayName = strings.TrimSpace(p.DisplayName)
	if !validCommonText(p.Email, 3, maxCommonEmailLength) || !strings.Contains(p.Email, "@") ||
		!validCommonText(p.DisplayName, 0, maxCommonNameLength) || !p.TenantRole.Valid() || !p.Status.Valid() {
		return p, errors.WithStack(port.ErrInvalid)
	}
	if !s.bound {
		ctx = model.EnsureActor(ctx)
		return p, s.transaction(ctx, func(tx *ProvisioningService) error {
			_, err := tx.PutTenantMember(ctx, tenantID, userID, p)
			return err
		})
	}

	if _, err := s.commonParentTenant(ctx, tenantID); err != nil {
		return p, err
	}
	old, err := s.userStore.GetUserByID(ctx, userID)
	if err != nil {
		return p, errors.WithStack(err)
	}
	if old.TenantID() != tenantID {
		return p, errors.Wrap(port.ErrAlreadyExists, "user id is used by another tenant")
	}
	if old.Email() == p.Email && old.DisplayName() == p.DisplayName &&
		old.TenantRole() == p.TenantRole && model.DeclaredStatus(old.Active()) == p.Status {
		return p, nil
	}
	if isPlatformAdmin(old) {
		return p, errors.WithStack(port.ErrPlatformAdminProtected)
	}
	if !strings.EqualFold(old.Email(), p.Email) {
		if err := s.assertEmailAllowed(&p.Email); err != nil {
			return p, err
		}
	}

	user := model.CopyUser(old)
	user.SetEmail(p.Email)
	user.SetDisplayName(p.DisplayName)
	user.SetActive(p.Status == model.StatusActive)
	user.SetTenantRole(p.TenantRole)
	return p, errors.WithStack(s.userStore.SaveUser(ctx, user))
}

// PutOrgMember adds the tenant member to the organization or brings the
// membership to the given representation. The common role replaces the
// builtin role of the membership; custom roles are kept.
func (s *ProvisioningService) PutOrgMember(ctx context.Context, tenantID model.TenantID, orgID model.OrgID, userID model.UserID, p CommonMembership) (CommonMembership, error) {
	if !p.Role.Valid() || !p.Status.Valid() {
		return p, errors.WithStack(port.ErrInvalid)
	}
	if !s.bound {
		ctx = model.EnsureActor(ctx)
		return p, s.transaction(ctx, func(tx *ProvisioningService) error {
			_, err := tx.PutOrgMember(ctx, tenantID, orgID, userID, p)
			return err
		})
	}

	if _, err := s.commonParentTenant(ctx, tenantID); err != nil {
		return p, err
	}
	org, err := s.orgStore.GetOrgByID(ctx, orgID)
	if errors.Is(err, port.ErrNotFound) || (err == nil && org.TenantID() != tenantID) {
		return p, errors.Wrap(port.ErrParentNotFound, "organization not found")
	}
	if err != nil {
		return p, errors.WithStack(err)
	}
	user, err := s.userStore.GetUserByID(ctx, userID)
	if errors.Is(err, port.ErrNotFound) || (err == nil && user.TenantID() != tenantID) {
		return p, errors.Wrap(port.ErrParentNotFound, "user not found")
	}
	if err != nil {
		return p, errors.WithStack(err)
	}

	membership, err := s.orgStore.GetUserOrgMembership(ctx, userID, orgID)
	if err != nil && !errors.Is(err, port.ErrNotFound) {
		return p, errors.WithStack(err)
	}
	if membership != nil && model.CommonRoleOf(membership) == p.Role && membership.Status() == p.Status {
		return p, nil
	}

	roles, err := s.roleStore.ListOrgRoles(ctx, orgID)
	if err != nil {
		return p, errors.WithStack(err)
	}
	var builtin model.RoleID
	for _, role := range roles {
		if role.BuiltinKind() == string(p.Role) {
			builtin = role.ID()
		}
	}
	if builtin == "" {
		return p, errors.Errorf("organization %s has no builtin %s role", orgID, p.Role)
	}

	roleIDs := []model.RoleID{builtin}
	if membership == nil {
		created := model.NewMembership(userID, orgID)
		created.SetStatus(p.Status)
		if err := s.orgStore.AddMember(ctx, created); err != nil {
			return p, errors.WithStack(err)
		}
		return p, errors.WithStack(s.roleStore.SetMembershipRoles(ctx, created.ID(), roleIDs))
	}

	for _, role := range membership.Roles() {
		if !role.Builtin() {
			roleIDs = append(roleIDs, role.ID())
		}
	}
	if model.CommonRoleOf(membership) != p.Role {
		if err := s.roleStore.SetMembershipRoles(ctx, membership.ID(), roleIDs); err != nil {
			return p, errors.WithStack(err)
		}
	}
	if membership.Status() != p.Status {
		if err := s.orgStore.SetMembershipStatus(ctx, membership.ID(), p.Status); err != nil {
			return p, errors.WithStack(err)
		}
	}
	return p, nil
}

func isPlatformAdmin(user model.User) bool {
	return slices.Contains(user.Roles(), model.PlatformRoleAdmin)
}
