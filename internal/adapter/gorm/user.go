package gorm

import (
	"slices"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

type User struct {
	ID string `gorm:"primaryKey;autoIncrement:false"`

	CreatedAt time.Time
	UpdatedAt time.Time

	// TenantID scopes the identity: (tenant_id, provider, subject) is the unique
	// key, so the same person signing in on two tenants owns two accounts.
	TenantID string `gorm:"index;uniqueIndex:idx_users_tenant_identity,priority:1;uniqueIndex:idx_users_tenant_email_nonempty,priority:1;not null"`

	Subject  string `gorm:"index;uniqueIndex:idx_users_tenant_identity,priority:2"`
	Provider string `gorm:"index;uniqueIndex:idx_users_tenant_identity,priority:3"`

	DisplayName string
	Email       string `gorm:"uniqueIndex:idx_users_tenant_email_nonempty,priority:2,where:email != ''"`

	AuthTokens []*AuthToken `gorm:"foreignKey:OwnerID;constraint:OnDelete:CASCADE;"`

	Roles []*UserRole `gorm:"constraint:OnDelete:CASCADE;"`

	Active      bool
	Preferences *UserPreferences `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE"`
}

// wrappedUserPreference implements model.UserPreferences
type wrappedUserPreference struct {
	p *UserPreferences
}

// DarkMode implements model.UserPreferences.
func (p *wrappedUserPreference) DarkMode() (bool, bool) {
	if p.p.DarkMode == nil {
		return false, false
	}
	return *p.p.DarkMode, true
}

var _ model.UserPreferences = &wrappedUserPreference{}

// UserPreference is a separate table for storing user preferences
type UserPreferences struct {
	ID       uint   `gorm:"primaryKey"`
	UserID   string `gorm:"unique;index"`
	DarkMode *bool
}

// fromUser converts a model.User to a GORM User
func fromUser(u model.User) *User {
	user := &User{
		ID:          string(u.ID()),
		TenantID:    string(u.TenantID()),
		Subject:     u.Subject(),
		Provider:    u.Provider(),
		DisplayName: u.DisplayName(),
		Email:       u.Email(),
		Active:      u.Active(),
	}

	user.Preferences = &UserPreferences{
		UserID:   string(u.ID()),
		DarkMode: nil,
	}

	// A model.User implementation may carry no preferences at all.
	if prefs := u.Preferences(); prefs != nil {
		if darkMode, exists := prefs.DarkMode(); exists {
			user.Preferences.DarkMode = &darkMode
		}
	}

	for _, r := range u.Roles() {
		user.Roles = append(user.Roles, &UserRole{
			User:   user,
			UserID: user.ID,
			Role:   r,
		})
	}

	return user
}

// Update AuthToken to support Application owner
type AuthToken struct {
	ID string `gorm:"primaryKey;autoIncrement:false"`

	CreatedAt time.Time
	UpdatedAt time.Time

	Owner   *User
	OwnerID *string // Nullable for Application

	Application   *Application
	ApplicationID *string // Nullable for User

	Label     string
	Value     string     `gorm:"unique"`
	OrgID     string     `gorm:"index"`
	ExpiresAt *time.Time `gorm:"index"`
}

type Application struct {
	ID          string `gorm:"primaryKey;autoIncrement:false"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
	OrgID       string `gorm:"index"`
	Name        string
	Description string
	Active      bool

	AuthTokens []*AuthToken `gorm:"foreignKey:ApplicationID;constraint:OnDelete:CASCADE;"`
}

type wrappedApplication struct {
	a *Application
}

func (w *wrappedApplication) ID() model.ApplicationID { return model.ApplicationID(w.a.ID) }
func (w *wrappedApplication) OrgID() model.OrgID      { return model.OrgID(w.a.OrgID) }
func (w *wrappedApplication) Name() string            { return w.a.Name }
func (w *wrappedApplication) Description() string     { return w.a.Description }
func (w *wrappedApplication) Active() bool            { return w.a.Active }
func (w *wrappedApplication) CreatedAt() time.Time    { return w.a.CreatedAt }
func (w *wrappedApplication) UpdatedAt() time.Time    { return w.a.UpdatedAt }

var _ model.Application = &wrappedApplication{}

type UserRole struct {
	ID uint `gorm:"primaryKey"`

	CreatedAt time.Time

	User   *User
	UserID string `gorm:"index:user_role_index,unique"`

	Role string `gorm:"index:user_role_index,unique"`
}

// wrappedUser implements the model.User interface
type wrappedUser struct {
	u *User
}

// Active implements [model.User].
func (w *wrappedUser) Active() bool {
	return w.u.Active
}

// ID implements model.User.
// TenantID implements model.User.
func (w *wrappedUser) TenantID() model.TenantID {
	return model.TenantID(w.u.TenantID)
}

func (w *wrappedUser) ID() model.UserID {
	return model.UserID(w.u.ID)
}

// Email implements model.User.
func (w *wrappedUser) Email() string {
	return w.u.Email
}

// Subject implements model.User.
func (w *wrappedUser) Subject() string {
	return w.u.Subject
}

// Provider implements model.User.
func (w *wrappedUser) Provider() string {
	return w.u.Provider
}

// DisplayName implements model.User.
func (w *wrappedUser) DisplayName() string {
	return w.u.DisplayName
}

// Roles implements model.User.
func (w *wrappedUser) Roles() []string {
	return slices.Collect(func(yield func(string) bool) {
		for _, r := range w.u.Roles {
			if !yield(r.Role) {
				return
			}
		}
	})
}

// Preferences implements model.User.
func (w *wrappedUser) Preferences() model.UserPreferences {
	if w.u.Preferences == nil {
		return model.NewUserPreferences()
	}
	return &wrappedUserPreference{w.u.Preferences}
}

var _ model.User = &wrappedUser{}

// wrappedAuthToken implements the model.AuthToken interface
type wrappedAuthToken struct {
	t *AuthToken
}

func (w *wrappedAuthToken) ID() model.AuthTokenID { return model.AuthTokenID(w.t.ID) }
func (w *wrappedAuthToken) Owner() model.User {
	if w.t.Owner == nil {
		return nil
	}
	return &wrappedUser{w.t.Owner}
}
func (w *wrappedAuthToken) Application() model.Application {
	if w.t.Application == nil {
		return nil
	}
	return &wrappedApplication{w.t.Application}
}
func (w *wrappedAuthToken) Label() string         { return w.t.Label }
func (w *wrappedAuthToken) Value() string         { return w.t.Value }
func (w *wrappedAuthToken) OrgID() model.OrgID    { return model.OrgID(w.t.OrgID) }
func (w *wrappedAuthToken) ExpiresAt() *time.Time { return w.t.ExpiresAt }

var _ model.AuthToken = &wrappedAuthToken{}

// wrappedApplicationAuthToken implements the model.AuthToken interface for application tokens
type wrappedApplicationAuthToken struct {
	t *AuthToken
}

func (w *wrappedApplicationAuthToken) ID() model.AuthTokenID { return model.AuthTokenID(w.t.ID) }
func (w *wrappedApplicationAuthToken) Owner() model.User {
	if w.t.Owner != nil {
		return &wrappedUser{w.t.Owner}
	}
	return nil
}
func (w *wrappedApplicationAuthToken) Application() model.Application {
	if w.t.Application != nil {
		return &wrappedApplication{w.t.Application}
	}
	return nil
}
func (w *wrappedApplicationAuthToken) Label() string         { return w.t.Label }
func (w *wrappedApplicationAuthToken) Value() string         { return w.t.Value }
func (w *wrappedApplicationAuthToken) OrgID() model.OrgID    { return model.OrgID(w.t.OrgID) }
func (w *wrappedApplicationAuthToken) ExpiresAt() *time.Time { return w.t.ExpiresAt }

var _ model.AuthToken = &wrappedApplicationAuthToken{}
