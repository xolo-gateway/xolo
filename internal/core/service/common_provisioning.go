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

// CommonMember is the common representation of a tenant member. The platform
// roles are not part of it: a PUT never changes them. Identity says what the
// PUT does to the declared identity and the sign-in link. Locale is kept in
// the member's preferences; empty clears it.
type CommonMember struct {
	Email       string
	DisplayName string
	Locale      string
	TenantRole  model.TenantRole
	Status      model.Status
	Identity    IdentityChange
}

// IdentityChangeKind tells what a member PUT does to the declared identity.
type IdentityChangeKind int

const (
	// IdentityKeep leaves the declared identity and the sign-in link as they
	// are: a client unaware of the field never detaches anybody.
	IdentityKeep IdentityChangeKind = iota
	// IdentityUnlink removes the declared identity and detaches the sign-in
	// link. The member signs in again only through a new declaration or an
	// unambiguous verified email.
	IdentityUnlink
	// IdentityDeclare declares the identity. A sign-in link to another
	// identity is never replaced implicitly: it must be unlinked first.
	IdentityDeclare
)

// IdentityChange is the identity part of a member PUT.
type IdentityChange struct {
	Kind     IdentityChangeKind
	Identity model.Identity
}

// unchanged reports whether applying the change to the user is a no-op.
func (c IdentityChange) unchanged(user model.User) bool {
	switch c.Kind {
	case IdentityUnlink:
		return user.DeclaredIdentity() == nil && user.Provider() == "" && user.Subject() == ""
	case IdentityDeclare:
		declared := user.DeclaredIdentity()
		return declared != nil && *declared == c.Identity
	}
	return true
}

func (c IdentityChange) apply(user *model.BaseUser) {
	switch c.Kind {
	case IdentityUnlink:
		user.SetDeclaredIdentity(nil)
		user.SetAuthenticationLink("", "")
	case IdentityDeclare:
		identity := c.Identity
		user.SetDeclaredIdentity(&identity)
	}
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

// PutCommon runs one PUT of the common contract within a single transaction.
// The condition is checked against the current revision of the resource
// before any write, and before the detection of a no-op: a stale condition
// fails even for an identical representation. The result is the projection
// written by the same transaction.
func (s *ProvisioningService) PutCommon(ctx context.Context, scope model.CommonScope, key string, condition model.MatchCondition, put func(ctx context.Context, tx *ProvisioningService) error) (model.CommonItem, error) {
	if model.IsReadOnlyFamily(scope.Family) {
		return model.CommonItem{}, errors.WithStack(port.ErrInvalid)
	}
	var item model.CommonItem
	ctx = model.EnsureActor(ctx)
	err := s.transaction(ctx, func(tx *ProvisioningService) error {
		current, err := tx.tx.ReadProjection(ctx, scope, key)
		if err != nil && !errors.Is(err, port.ErrNotFound) {
			return errors.WithStack(err)
		}
		if !condition.Matches(current.ETag) {
			return errors.WithStack(port.ErrPreconditionFailed)
		}
		if err := put(ctx, tx); err != nil {
			return err
		}
		item, err = tx.tx.ReadProjection(ctx, scope, key)
		return errors.WithStack(err)
	})
	return item, err
}

// deleteTarget checks, within the transaction, the parents of a resource to
// delete and the condition against its current revision. A missing or
// foreign parent is ErrParentNotFound. A missing resource is reported as
// absent, unless a condition designates a revision: it then fails.
func (s *ProvisioningService) deleteTarget(ctx context.Context, scope model.CommonScope, key string, condition model.MatchCondition) (bool, error) {
	if _, err := s.commonParentTenant(ctx, model.TenantID(scope.TenantID)); err != nil {
		return false, err
	}
	if scope.OrganizationID != "" {
		org, err := s.orgStore.GetOrgByID(ctx, model.OrgID(scope.OrganizationID))
		if errors.Is(err, port.ErrNotFound) || (err == nil && string(org.TenantID()) != scope.TenantID) {
			return false, errors.Wrap(port.ErrParentNotFound, "organization not found")
		}
		if err != nil {
			return false, errors.WithStack(err)
		}
	}
	current, err := s.tx.ReadProjection(ctx, scope, key)
	switch {
	case errors.Is(err, port.ErrNotFound):
		if condition.Present {
			return false, errors.WithStack(port.ErrPreconditionFailed)
		}
		return false, nil
	case err != nil:
		return false, errors.WithStack(err)
	case !condition.Matches(current.ETag):
		return false, errors.WithStack(port.ErrPreconditionFailed)
	}
	return true, nil
}

// DeleteDomain removes a domain of the tenant at once: the hostname stops
// resolving to it. Deleting a missing domain changes nothing.
func (s *ProvisioningService) DeleteDomain(ctx context.Context, tenantID model.TenantID, hostname string, condition model.MatchCondition) error {
	scope := model.CommonScope{Family: model.FamilyTenantDomain, TenantID: string(tenantID)}
	ctx = model.EnsureActor(ctx)
	return s.transaction(ctx, func(tx *ProvisioningService) error {
		exists, err := tx.deleteTarget(ctx, scope, hostname, condition)
		if err != nil || !exists {
			return err
		}
		return errors.WithStack(tx.domainStore.DeleteDomain(ctx, tenantID, hostname))
	})
}

// DeleteOrgMember removes the member from the organization at once. The last
// active owner of the organization can not be removed. Deleting a missing
// membership changes nothing.
func (s *ProvisioningService) DeleteOrgMember(ctx context.Context, tenantID model.TenantID, orgID model.OrgID, userID model.UserID, condition model.MatchCondition) error {
	scope := model.CommonScope{Family: model.FamilyOrganizationMembership, TenantID: string(tenantID), OrganizationID: string(orgID)}
	ctx = model.EnsureActor(ctx)
	return s.transaction(ctx, func(tx *ProvisioningService) error {
		exists, err := tx.deleteTarget(ctx, scope, string(userID), condition)
		if err != nil || !exists {
			return err
		}
		membership, err := tx.orgStore.GetUserOrgMembership(ctx, userID, orgID)
		if err != nil {
			return errors.WithStack(err)
		}
		return errors.WithStack(tx.orgStore.RemoveMember(ctx, membership.ID()))
	})
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

// PutTenantMember creates the member or brings it to the given representation.
// A created member has no sign-in link: it is linked at its first sign-in,
// through its declared identity or an unambiguous verified email. Platform
// roles are always preserved, and a platform administrator is never modified:
// provisioning does not act on platform-wide privileges.
func (s *ProvisioningService) PutTenantMember(ctx context.Context, tenantID model.TenantID, userID model.UserID, p CommonMember) (CommonMember, error) {
	p.Email = strings.TrimSpace(p.Email)
	p.DisplayName = strings.TrimSpace(p.DisplayName)
	p.Locale = model.NormalizeLocale(p.Locale)
	if !validCommonText(p.Email, 3, maxCommonEmailLength) || !strings.Contains(p.Email, "@") ||
		!validCommonText(p.DisplayName, 0, maxCommonNameLength) || !model.IsValidLocale(p.Locale) || !p.TenantRole.Valid() || !p.Status.Valid() {
		return p, errors.WithStack(port.ErrInvalid)
	}
	if p.Identity.Kind == IdentityDeclare && !p.Identity.Identity.Valid() {
		return p, errors.Wrap(port.ErrInvalid, "invalid identity")
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
	if errors.Is(err, port.ErrNotFound) {
		return p, s.createTenantMember(ctx, tenantID, userID, p)
	}
	if err != nil {
		return p, errors.WithStack(err)
	}
	if old.TenantID() != tenantID {
		return p, errors.Wrap(port.ErrAlreadyExists, "user id is used by another tenant")
	}
	// The shadow user of an application is not a member: its lifecycle
	// follows the application.
	if old.Provider() == model.ApplicationProvider {
		return p, errors.Wrap(port.ErrNotFound, "user not found")
	}
	if old.Email() == p.Email && old.DisplayName() == p.DisplayName && memberLocale(old) == p.Locale &&
		old.TenantRole() == p.TenantRole && model.DeclaredStatus(old.Active()) == p.Status &&
		p.Identity.unchanged(old) {
		return p, nil
	}
	if isPlatformAdmin(old) {
		return p, errors.WithStack(port.ErrPlatformAdminProtected)
	}
	if !strings.EqualFold(old.Email(), p.Email) {
		if err := s.assertMemberEmailAvailable(ctx, tenantID, userID, p.Email); err != nil {
			return p, err
		}
	}

	user := model.CopyUser(old)
	user.SetEmail(p.Email)
	user.SetDisplayName(p.DisplayName)
	user.SetActive(p.Status == model.StatusActive)
	user.SetTenantRole(p.TenantRole)
	setMemberLocale(user, p.Locale)
	p.Identity.apply(user)
	return p, errors.WithStack(s.userStore.SaveUser(ctx, user))
}

// memberLocale returns the locale a member chose, empty when none.
func memberLocale(user model.User) string {
	if preferences := user.Preferences(); preferences != nil {
		return preferences.Locale()
	}
	return ""
}

// setMemberLocale replaces the locale of a member, keeping their other
// preferences.
func setMemberLocale(user *model.BaseUser, locale string) {
	preferences := model.CopyUserPreferences(user.Preferences())
	preferences.Set(model.SetUserPreferencesLocale(locale))
	user.SetPreferences(preferences)
}

// createTenantMember creates a member provisioned ahead of its first sign-in.
// Unlinking an account that does not exist yet changes nothing.
func (s *ProvisioningService) createTenantMember(ctx context.Context, tenantID model.TenantID, userID model.UserID, p CommonMember) error {
	if err := s.assertMemberEmailAvailable(ctx, tenantID, userID, p.Email); err != nil {
		return err
	}
	user := model.NewUser(tenantID, "", "", p.Email, p.DisplayName, p.Status == model.StatusActive, model.PlatformRoleUser)
	user.SetID(userID)
	user.SetTenantRole(p.TenantRole)
	setMemberLocale(user, p.Locale)
	p.Identity.apply(user)
	return errors.WithStack(s.userStore.SaveUser(ctx, user))
}

// assertMemberEmailAvailable refuses an email reserved for the instance
// administrators, and one another account of the tenant already holds in a
// different case: an ambiguous email can never attach a sign-in, so
// provisioning must not create one. Historical ambiguities are left as is.
func (s *ProvisioningService) assertMemberEmailAvailable(ctx context.Context, tenantID model.TenantID, userID model.UserID, email string) error {
	if err := s.assertEmailAllowed(&email); err != nil {
		return err
	}
	users, err := s.userStore.FindUsersByEmail(ctx, tenantID, email, 2)
	if err != nil {
		return errors.WithStack(err)
	}
	for _, u := range users {
		if u.ID() != userID {
			return errors.Wrapf(port.ErrEmailTaken, "email %q is already used by another user", email)
		}
	}
	return nil
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
