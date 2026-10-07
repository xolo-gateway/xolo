package model

import (
	"time"

	"github.com/google/uuid"
	"github.com/rs/xid"
)

// Platform-wide roles, carried by User.Roles(). They are distinct from the
// organization-scoped roles of the RBAC system: holding an org owner/admin role
// never grants any of these.
const (
	PlatformRoleUser  = "user"
	PlatformRoleAdmin = "admin"
)

type UserID string

func NewUserID() UserID {
	return UserID(uuid.NewString())
}

type User interface {
	WithID[UserID]

	// TenantID is the owning tenant. The identity tuple (provider, subject) is
	// unique per tenant, not instance-wide: the same person authenticating on
	// two tenants owns two distinct accounts.
	TenantID() TenantID

	Email() string

	// Subject and Provider form the authentication link: the local provider ID
	// and subject of the last sign-in bound to this account. Both are empty
	// for an account no sign-in is linked to yet.
	Subject() string
	Provider() string

	// DeclaredIdentity is the external identity provisioning declared for
	// this account, or nil. A sign-in proving it is linked to the account.
	DeclaredIdentity() *Identity

	DisplayName() string

	Roles() []string

	// TenantRole is the role declared by provisioning within the tenant. It
	// grants no platform privilege.
	TenantRole() TenantRole

	Active() bool

	Preferences() UserPreferences
}

type BaseUser struct {
	id          UserID
	tenantID    TenantID
	displayName string
	email       string
	subject     string
	provider    string
	declared    *Identity
	roles       []string
	tenantRole  TenantRole
	active      bool
	preferences UserPreferences
}

// TenantRole implements [User].
func (u *BaseUser) TenantRole() TenantRole {
	return u.tenantRole
}

func (u *BaseUser) SetTenantRole(role TenantRole) {
	u.tenantRole = role
}

// Preferences implements [User].
func (u *BaseUser) Preferences() UserPreferences {
	return u.preferences
}

// Active implements [User].
func (u *BaseUser) Active() bool {
	return u.active
}

// Email implements [User].
func (u *BaseUser) Email() string {
	return u.email
}

// ID implements [User].
func (u *BaseUser) ID() UserID {
	return u.id
}

// TenantID implements [User].
func (u *BaseUser) TenantID() TenantID {
	return u.tenantID
}

// DisplayName implements User.
func (u *BaseUser) DisplayName() string {
	return u.displayName
}

// Provider implements User.
func (u *BaseUser) Provider() string {
	return u.provider
}

// Roles implements User.
func (u *BaseUser) Roles() []string {
	return u.roles
}

// Subject implements User.
func (u *BaseUser) Subject() string {
	return u.subject
}

// DeclaredIdentity implements User.
func (u *BaseUser) DeclaredIdentity() *Identity {
	return copyIdentity(u.declared)
}

// SetDeclaredIdentity replaces the declared identity; nil removes it.
func (u *BaseUser) SetDeclaredIdentity(identity *Identity) {
	u.declared = copyIdentity(identity)
}

// SetAuthenticationLink binds the account to a sign-in; empty values unlink it.
func (u *BaseUser) SetAuthenticationLink(provider, subject string) {
	u.provider, u.subject = provider, subject
}

func copyIdentity(identity *Identity) *Identity {
	if identity == nil {
		return nil
	}
	c := *identity
	return &c
}

var _ User = &BaseUser{}

func CopyUser(user User) *BaseUser {
	return &BaseUser{
		id:          user.ID(),
		tenantID:    user.TenantID(),
		displayName: user.DisplayName(),
		email:       user.Email(),
		subject:     user.Subject(),
		provider:    user.Provider(),
		declared:    user.DeclaredIdentity(),
		active:      user.Active(),
		preferences: user.Preferences(),
		roles:       append([]string{}, user.Roles()...),
		tenantRole:  user.TenantRole(),
	}
}

func NewUser(tenantID TenantID, provider, subject, email string, displayName string, active bool, roles ...string) *BaseUser {
	return &BaseUser{
		id:          NewUserID(),
		tenantID:    tenantID,
		displayName: displayName,
		email:       email,
		subject:     subject,
		provider:    provider,
		roles:       roles,
		tenantRole:  TenantRoleMember,
		active:      active,
		preferences: NewUserPreferences(),
	}
}

func (u *BaseUser) SetDisplayName(displayName string) {
	u.displayName = displayName
}

func (u *BaseUser) SetActive(active bool) {
	u.active = active
}

func (u *BaseUser) SetEmail(email string) {
	u.email = email
}

func (u *BaseUser) SetRoles(roles ...string) {
	u.roles = roles
}

func (u *BaseUser) SetPreferences(preferences UserPreferences) {
	u.preferences = preferences
}

type AuthTokenID string

func NewAuthTokenID() AuthTokenID {
	return AuthTokenID(xid.New().String())
}

type AuthToken interface {
	WithID[AuthTokenID]
	WithOwner
	WithApplication

	Label() string
	Value() string
	OrgID() OrgID
	ExpiresAt() *time.Time
}

type BaseAuthToken struct {
	id          AuthTokenID
	owner       User
	application Application
	label       string
	value       string
	orgID       OrgID
	expiresAt   *time.Time
}

// ID implements AuthToken.
func (t *BaseAuthToken) ID() AuthTokenID { return t.id }

// Owner implements AuthToken.
func (t *BaseAuthToken) Owner() User { return t.owner }

// Application implements AuthToken.
func (t *BaseAuthToken) Application() Application { return t.application }

// Label implements AuthToken.
func (t *BaseAuthToken) Label() string { return t.label }

// Value implements AuthToken.
func (t *BaseAuthToken) Value() string { return t.value }

// OrgID implements AuthToken.
func (t *BaseAuthToken) OrgID() OrgID { return t.orgID }

// ExpiresAt implements AuthToken.
func (t *BaseAuthToken) ExpiresAt() *time.Time { return t.expiresAt }

var _ AuthToken = &BaseAuthToken{}

func NewAuthToken(owner User, orgID OrgID, label, value string, expiresAt *time.Time) *BaseAuthToken {
	return &BaseAuthToken{
		id:        NewAuthTokenID(),
		owner:     owner,
		orgID:     orgID,
		label:     label,
		value:     value,
		expiresAt: expiresAt,
	}
}

func NewApplicationAuthToken(application Application, orgID OrgID, label, value string, expiresAt *time.Time) *BaseAuthToken {
	return &BaseAuthToken{
		id:          NewAuthTokenID(),
		application: application,
		orgID:       orgID,
		label:       label,
		value:       value,
		expiresAt:   expiresAt,
	}
}

type UserPreferences interface {
	DarkMode() (bool, bool)
}

type BaseUserPreferences struct {
	darkMode *bool
}

// DarkMode implements [UserPreferences].
func (p *BaseUserPreferences) DarkMode() (bool, bool) {
	if p.darkMode == nil {
		return false, false
	}

	return *p.darkMode, true
}

func SetUserPrefencesDarkMode(darkMode *bool) BaseUserPreferencesSetter {
	return func(p *BaseUserPreferences) {
		p.darkMode = darkMode
	}
}

func (p *BaseUserPreferences) Set(setters ...BaseUserPreferencesSetter) {
	for _, s := range setters {
		s(p)
	}
}

type BaseUserPreferencesSetter func(p *BaseUserPreferences)

func NewUserPreferences(setters ...BaseUserPreferencesSetter) *BaseUserPreferences {
	preferences := &BaseUserPreferences{
		darkMode: nil,
	}
	preferences.Set(setters...)
	return preferences
}

var _ UserPreferences = &BaseUserPreferences{}

// SetID accepts a validated external UUID; callers should use ParseUserID first.
func (u *BaseUser) SetID(id UserID) { u.id = id }
