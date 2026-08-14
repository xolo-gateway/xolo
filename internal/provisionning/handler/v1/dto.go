package v1

import (
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
)

// The API exposes its own representations: the domain interfaces are never
// serialized directly, so the wire contract stays stable when the domain moves.
//
// The resource hierarchy mirrors the domain: a tenant owns organizations and
// users, an organization owns members and roles.

type tenantDTO struct {
	ID          string    `json:"id"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Active      bool      `json:"active"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func newTenantDTO(tenant model.Tenant) tenantDTO {
	return tenantDTO{
		ID:          string(tenant.ID()),
		Slug:        tenant.Slug(),
		Name:        tenant.Name(),
		Description: tenant.Description(),
		Active:      tenant.Active(),
		CreatedAt:   tenant.CreatedAt(),
		UpdatedAt:   tenant.UpdatedAt(),
	}
}

type organizationDTO struct {
	ID                string    `json:"id"`
	TenantID          string    `json:"tenantId"`
	Slug              string    `json:"slug"`
	Name              string    `json:"name"`
	Description       string    `json:"description"`
	Active            bool      `json:"active"`
	Currency          string    `json:"currency"`
	ShareQuotaEqually bool      `json:"shareQuotaEqually"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

func newOrganizationDTO(org model.Organization) organizationDTO {
	return organizationDTO{
		ID:                string(org.ID()),
		TenantID:          string(org.TenantID()),
		Slug:              org.Slug(),
		Name:              org.Name(),
		Description:       org.Description(),
		Active:            org.Active(),
		Currency:          org.Currency(),
		ShareQuotaEqually: org.ShareQuotaEqually(),
		CreatedAt:         org.CreatedAt(),
		UpdatedAt:         org.UpdatedAt(),
	}
}

type userDTO struct {
	ID          string `json:"id"`
	Provider    string `json:"provider"`
	Subject     string `json:"subject"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	Active      bool   `json:"active"`

	// PlatformRoles are instance-wide roles. They are read-only here: the
	// Provisionning API never grants platform privileges.
	PlatformRoles []string `json:"platformRoles"`
}

func newUserDTO(user model.User) userDTO {
	roles := user.Roles()
	if roles == nil {
		roles = []string{}
	}

	return userDTO{
		ID:            string(user.ID()),
		Provider:      user.Provider(),
		Subject:       user.Subject(),
		Email:         user.Email(),
		DisplayName:   user.DisplayName(),
		Active:        user.Active(),
		PlatformRoles: roles,
	}
}

// userRefDTO identifies a user without its platform roles. Memberships embed a
// reference because the store does not preload the user's platform roles in
// that context: an empty list there would be misleading. Read the user through
// /v1/users to get them.
type userRefDTO struct {
	ID          string `json:"id"`
	Provider    string `json:"provider"`
	Subject     string `json:"subject"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	Active      bool   `json:"active"`
}

func newUserRefDTO(user model.User) userRefDTO {
	return userRefDTO{
		ID:          string(user.ID()),
		Provider:    user.Provider(),
		Subject:     user.Subject(),
		Email:       user.Email(),
		DisplayName: user.DisplayName(),
		Active:      user.Active(),
	}
}

type modelGrantDTO struct {
	ModelID string `json:"modelId"`
	Kind    string `json:"kind"`
}

type roleDTO struct {
	ID             string          `json:"id"`
	OrganizationID string          `json:"organizationId"`
	Name           string          `json:"name"`
	Description string          `json:"description"`
	Builtin     bool            `json:"builtin"`
	BuiltinKind string          `json:"builtinKind,omitempty"`
	Permissions []string        `json:"permissions"`
	ModelGrants []modelGrantDTO `json:"modelGrants"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

func newRoleDTO(role model.Role) roleDTO {
	permissions := role.Permissions()
	if permissions == nil {
		permissions = []string{}
	}

	grants := make([]modelGrantDTO, 0, len(role.ModelGrants()))
	for _, grant := range role.ModelGrants() {
		grants = append(grants, modelGrantDTO{ModelID: grant.ModelID, Kind: grant.Kind})
	}

	return roleDTO{
		ID:             string(role.ID()),
		OrganizationID: string(role.OrgID()),
		Name:        role.Name(),
		Description: role.Description(),
		Builtin:     role.Builtin(),
		BuiltinKind: role.BuiltinKind(),
		Permissions: permissions,
		ModelGrants: grants,
		CreatedAt:   role.CreatedAt(),
		UpdatedAt:   role.UpdatedAt(),
	}
}

// roleRefDTO identifies a role without its permissions. Memberships are listed
// with role references because the store does not preload role permissions in
// that context: returning an empty permission list there would be misleading.
type roleRefDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Builtin     bool   `json:"builtin"`
	BuiltinKind string `json:"builtinKind,omitempty"`
}

func newRoleRefDTOs(roles []model.Role) []roleRefDTO {
	refs := make([]roleRefDTO, 0, len(roles))
	for _, role := range roles {
		refs = append(refs, roleRefDTO{
			ID:          string(role.ID()),
			Name:        role.Name(),
			Builtin:     role.Builtin(),
			BuiltinKind: role.BuiltinKind(),
		})
	}
	return refs
}

type membershipDTO struct {
	ID             string       `json:"id"`
	OrganizationID string       `json:"organizationId"`
	UserID         string       `json:"userId"`
	User      *userRefDTO  `json:"user,omitempty"`
	Roles     []roleRefDTO `json:"roles"`
	CreatedAt time.Time    `json:"createdAt"`
}

func newMembershipDTO(membership model.Membership) membershipDTO {
	dto := membershipDTO{
		ID:             string(membership.ID()),
		OrganizationID: string(membership.OrgID()),
		UserID:    string(membership.UserID()),
		Roles:     newRoleRefDTOs(membership.Roles()),
		CreatedAt: membership.CreatedAt(),
	}

	if user := membership.User(); user != nil {
		ref := newUserRefDTO(user)
		dto.User = &ref
	}

	return dto
}

type permissionDTO struct {
	Code  string `json:"code"`
	Label string `json:"label"`
	// Implies is the permission this one grants implicitly, if any.
	Implies string `json:"implies,omitempty"`
}

type permissionGroupDTO struct {
	Section     string          `json:"section"`
	Label       string          `json:"label"`
	Permissions []permissionDTO `json:"permissions"`
}

func newPermissionCatalogDTO() []permissionGroupDTO {
	catalog := rbac.Catalog()

	groups := make([]permissionGroupDTO, 0, len(catalog))
	for _, group := range catalog {
		permissions := make([]permissionDTO, 0, len(group.Perms))
		for _, def := range group.Perms {
			permission := permissionDTO{Code: string(def.Code), Label: def.Label}
			if implied, ok := rbac.Implies(def.Code); ok {
				permission.Implies = string(implied)
			}
			permissions = append(permissions, permission)
		}

		groups = append(groups, permissionGroupDTO{
			Section:     group.Section,
			Label:       group.Label,
			Permissions: permissions,
		})
	}

	return groups
}

// listDTO is the shape of every paginated collection returned by the API.
type listDTO[T any] struct {
	Items []T   `json:"items"`
	Page  int   `json:"page"`
	Limit int   `json:"limit"`
	Total int64 `json:"total"`
}

func newListDTO[T any](items []T, page, limit int, total int64) listDTO[T] {
	if items == nil {
		items = []T{}
	}
	return listDTO[T]{Items: items, Page: page, Limit: limit, Total: total}
}

// Request payloads.

type userIdentityRequest struct {
	Provider    string  `json:"provider"`
	Subject     string  `json:"subject"`
	Email       *string `json:"email"`
	DisplayName *string `json:"displayName"`
	Active      *bool   `json:"active"`
}

type createTenantRequest struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Active      *bool  `json:"active"`
}

type updateTenantRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Active      *bool   `json:"active"`
}

type createOrganizationRequest struct {
	Slug        string               `json:"slug"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Currency    string               `json:"currency"`
	Active      *bool                `json:"active"`
	Owner       *userIdentityRequest `json:"owner"`
}

type updateOrganizationRequest struct {
	Name              *string `json:"name"`
	Description       *string `json:"description"`
	Active            *bool   `json:"active"`
	Currency          *string `json:"currency"`
	ShareQuotaEqually *bool   `json:"shareQuotaEqually"`
}

type addMemberRequest struct {
	UserID       string               `json:"userId"`
	User         *userIdentityRequest `json:"user"`
	RoleIDs      []string             `json:"roleIds"`
	BuiltinRoles []string             `json:"builtinRoles"`
}

type setMemberRolesRequest struct {
	RoleIDs      []string `json:"roleIds"`
	BuiltinRoles []string `json:"builtinRoles"`
}

type roleRequest struct {
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	Permissions []string        `json:"permissions"`
	ModelGrants []modelGrantDTO `json:"modelGrants"`
}

type updateUserRequest struct {
	Email       *string `json:"email"`
	DisplayName *string `json:"displayName"`
	Active      *bool   `json:"active"`
}

func toModelGrants(grants []modelGrantDTO) []model.ModelGrant {
	if grants == nil {
		return nil
	}

	converted := make([]model.ModelGrant, 0, len(grants))
	for _, grant := range grants {
		converted = append(converted, model.ModelGrant{ModelID: grant.ModelID, Kind: grant.Kind})
	}

	return converted
}

func toRoleIDs(ids []string) []model.RoleID {
	converted := make([]model.RoleID, 0, len(ids))
	for _, id := range ids {
		converted = append(converted, model.RoleID(id))
	}
	return converted
}
