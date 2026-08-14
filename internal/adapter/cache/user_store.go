package cache

import (
	"context"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

type UserStore struct {
	backend        port.UserStore
	userCache      *MultiIndexCache[*CacheableUser]
	authTokenCache *MultiIndexCache[*CacheableAuthToken]
}

// CreateAuthToken implements [port.UserStore].
func (s *UserStore) CreateAuthToken(ctx context.Context, token model.AuthToken) error {
	defer s.authTokenCache.Remove(string(token.ID()))

	return s.backend.CreateAuthToken(ctx, token)
}

// DeleteAuthToken implements [port.UserStore].
func (s *UserStore) DeleteAuthToken(ctx context.Context, tokenID model.AuthTokenID) error {
	defer s.authTokenCache.Remove(string(tokenID))

	return s.backend.DeleteAuthToken(ctx, tokenID)
}

// FindAuthToken implements [port.UserStore].
func (s *UserStore) FindAuthToken(ctx context.Context, token string) (model.AuthToken, error) {
	if authToken, exists := s.authTokenCache.Get(token); exists {
		return authToken, nil
	}

	authToken, err := s.backend.FindAuthToken(ctx, token)
	if err != nil {
		return nil, err
	}

	s.authTokenCache.Add(NewCacheableAuthToken(authToken))

	return authToken, nil
}

// FindOrCreateUser implements [port.UserStore].
func (s *UserStore) FindOrCreateUser(ctx context.Context, tenantID model.TenantID, provider string, subject string) (model.User, error) {
	if user, exists := s.userCache.Get(getUserProviderSubjectCacheKey(tenantID, provider, subject)); exists {
		return user, nil
	}

	user, err := s.backend.FindOrCreateUser(ctx, tenantID, provider, subject)
	if err != nil {
		return nil, err
	}

	s.userCache.Add(NewCacheableUser(user))

	return user, nil
}

// GetUserByIdentity implements [port.UserStore].
func (s *UserStore) GetUserByIdentity(ctx context.Context, tenantID model.TenantID, provider string, subject string) (model.User, error) {
	if user, exists := s.userCache.Get(getUserProviderSubjectCacheKey(tenantID, provider, subject)); exists {
		return user, nil
	}

	user, err := s.backend.GetUserByIdentity(ctx, tenantID, provider, subject)
	if err != nil {
		return nil, err
	}

	s.userCache.Add(NewCacheableUser(user))

	return user, nil
}

// GetUserAuthTokens implements [port.UserStore].
func (s *UserStore) GetUserAuthTokens(ctx context.Context, userID model.UserID) ([]model.AuthToken, error) {
	return s.backend.GetUserAuthTokens(ctx, userID)
}

// GetUserByID implements [port.UserStore].
func (s *UserStore) GetUserByID(ctx context.Context, userID model.UserID) (model.User, error) {
	if user, exists := s.userCache.Get(string(userID)); exists {
		return user, nil
	}

	return s.backend.GetUserByID(ctx, userID)
}

// QueryUsers implements [port.UserStore].
func (s *UserStore) QueryUsers(ctx context.Context, opts port.QueryUsersOptions) ([]model.User, error) {
	return s.backend.QueryUsers(ctx, opts)
}

// CountUsers implements [port.UserStore].
func (s *UserStore) CountUsers(ctx context.Context, opts port.QueryUsersOptions) (int64, error) {
	return s.backend.CountUsers(ctx, opts)
}

// SaveUser implements [port.UserStore].
func (s *UserStore) SaveUser(ctx context.Context, user model.User) error {
	defer s.userCache.Remove(string(user.ID()))

	return s.backend.SaveUser(ctx, user)
}

// DeleteUser implements [port.UserStore].
func (s *UserStore) DeleteUser(ctx context.Context, userID model.UserID) error {
	defer s.userCache.Remove(string(userID))

	return s.backend.DeleteUser(ctx, userID)
}

func NewUserStore(backend port.UserStore, size int, ttl time.Duration) *UserStore {
	return &UserStore{
		backend:        backend,
		userCache:      NewMultiIndexCache[*CacheableUser](size, ttl),
		authTokenCache: NewMultiIndexCache[*CacheableAuthToken](size, ttl),
	}
}

var _ port.UserStore = &UserStore{}
