package port

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

type UserStore interface {
	// FindOrCreateUser searches for a User in the store by its
	// tenant/provider/subject unique tuple and returns it if it exists, or
	// creates a new one otherwise.
	FindOrCreateUser(ctx context.Context, tenantID model.TenantID, provider, subject string) (model.User, error)

	// GetUserByID finds a user by its ID, or returns ErrNotFound if not found
	GetUserByID(ctx context.Context, userID model.UserID) (model.User, error)

	// GetUserByIdentity finds a user by its tenant/provider/subject unique
	// tuple, or returns ErrNotFound if not found. Unlike FindOrCreateUser, it
	// never creates anything.
	GetUserByIdentity(ctx context.Context, tenantID model.TenantID, provider, subject string) (model.User, error)

	// QueryUsers returns a paginated list of users
	QueryUsers(ctx context.Context, opts QueryUsersOptions) ([]model.User, error)

	// CountUsers returns the total number of users matching the given filters (pagination ignored)
	CountUsers(ctx context.Context, opts QueryUsersOptions) (int64, error)

	// SaveUser saves a user in the store
	SaveUser(ctx context.Context, user model.User) error

	// FindAuthToken searches for an AuthToken by its value, or returns ErrNotFound if not found
	FindAuthToken(ctx context.Context, token string) (model.AuthToken, error)

	// GetUserAuthTokens returns all the AuthToken associated to a User
	GetUserAuthTokens(ctx context.Context, userID model.UserID) ([]model.AuthToken, error)

	// CreateAuthToken creates a new AuthToken for a User
	CreateAuthToken(ctx context.Context, token model.AuthToken) error

	// DeleteAuthToken deletes an AuthToken by its ID
	DeleteAuthToken(ctx context.Context, tokenID model.AuthTokenID) error

	// DeleteUser deletes a user by its ID
	DeleteUser(ctx context.Context, userID model.UserID) error
}

type QueryUsersOptions struct {
	Page  *int
	Limit *int

	// Filters

	// TenantID restricts the result to a single tenant. A nil value spans the
	// whole instance and must stay reserved for maintenance loops that
	// legitimately cross tenants.
	TenantID *model.TenantID

	// Users with specific roles
	Roles []string

	// Active/inactive users
	Active *bool

	// Search restricts the result to the users whose display name, email or
	// subject contains the term, matched case-insensitively. Empty means no
	// restriction.
	Search string
}
