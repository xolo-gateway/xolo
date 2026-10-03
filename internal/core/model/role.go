package model

import (
	"time"

	"github.com/rs/xid"
)

type RoleID string

func NewRoleID() RoleID {
	return RoleID(xid.New().String())
}

// Builtin role kinds. These roles are created automatically for every
// organization and cannot be deleted.
const (
	BuiltinKindOwner  = "owner"
	BuiltinKindAdmin  = "admin"
	BuiltinKindMember = "member"
)

// ModelGrant authorizes a role to use a specific model resource.
type ModelGrant struct {
	ModelID string
	Kind    string // ModelKindLLM | ModelKindVirtual (see rbac package)
}

// Role is an organization-scoped set of permissions and model grants that can
// be assigned to memberships.
type Role interface {
	WithID[RoleID]

	OrgID() OrgID
	Name() string
	Description() string
	Builtin() bool
	BuiltinKind() string
	Permissions() []string
	ModelGrants() []ModelGrant
	CreatedAt() time.Time
	UpdatedAt() time.Time
}

type BaseRole struct {
	id          RoleID
	orgID       OrgID
	name        string
	description string
	builtin     bool
	builtinKind string
	permissions []string
	modelGrants []ModelGrant
	createdAt   time.Time
	updatedAt   time.Time
}

func (r *BaseRole) ID() RoleID                { return r.id }
func (r *BaseRole) OrgID() OrgID              { return r.orgID }
func (r *BaseRole) Name() string              { return r.name }
func (r *BaseRole) Description() string       { return r.description }
func (r *BaseRole) Builtin() bool             { return r.builtin }
func (r *BaseRole) BuiltinKind() string       { return r.builtinKind }
func (r *BaseRole) Permissions() []string     { return r.permissions }
func (r *BaseRole) ModelGrants() []ModelGrant { return r.modelGrants }
func (r *BaseRole) CreatedAt() time.Time      { return r.createdAt }
func (r *BaseRole) UpdatedAt() time.Time      { return r.updatedAt }

func (r *BaseRole) SetPermissions(permissions []string) { r.permissions = permissions }
func (r *BaseRole) SetModelGrants(grants []ModelGrant)  { r.modelGrants = grants }

var _ Role = &BaseRole{}

func NewRole(orgID OrgID, name, description string) *BaseRole {
	return &BaseRole{
		id:          NewRoleID(),
		orgID:       orgID,
		name:        name,
		description: description,
		createdAt:   time.Now(),
		updatedAt:   time.Now(),
	}
}

// NewBuiltinRole creates a builtin (non-deletable) role for an organization.
func NewBuiltinRole(orgID OrgID, kind, name, description string, permissions []string) *BaseRole {
	r := NewRole(orgID, name, description)
	r.builtin = true
	r.builtinKind = kind
	r.permissions = permissions
	return r
}

type RoleOption func(*BaseRole)

func WithRoleName(name string) RoleOption {
	return func(r *BaseRole) { r.name = name }
}

func WithRoleDescription(desc string) RoleOption {
	return func(r *BaseRole) { r.description = desc }
}

func WithRolePermissions(permissions []string) RoleOption {
	return func(r *BaseRole) { r.permissions = permissions }
}

func WithRoleModelGrants(grants []ModelGrant) RoleOption {
	return func(r *BaseRole) { r.modelGrants = grants }
}

func UpdateRole(role Role, opts ...RoleOption) *BaseRole {
	b := &BaseRole{
		id:          role.ID(),
		orgID:       role.OrgID(),
		name:        role.Name(),
		description: role.Description(),
		builtin:     role.Builtin(),
		builtinKind: role.BuiltinKind(),
		permissions: role.Permissions(),
		modelGrants: role.ModelGrants(),
		createdAt:   role.CreatedAt(),
		updatedAt:   time.Now(),
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// SetID assigns the caller-owned provisioning key before persistence.
func (v *BaseRole) SetID(id RoleID) { v.id = id }
