package cache

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// ApplicationStore invalidates the cached tokens of an application when the
// application or one of its tokens changes. A cached token carries its
// application, whose active flag the token login checks: without this, a
// deactivated application or a revoked application token keeps
// authenticating until the token cache TTL, on this instance and, through
// coherence, on the others.
type ApplicationStore struct {
	port.ApplicationStore
	users *UserStore
}

func NewApplicationStore(backend port.ApplicationStore, users *UserStore) *ApplicationStore {
	return &ApplicationStore{ApplicationStore: backend, users: users}
}

func (s *ApplicationStore) UpdateApplication(ctx context.Context, app model.Application) error {
	defer s.dropTokensOf(app.ID())
	return s.ApplicationStore.UpdateApplication(ctx, app)
}

func (s *ApplicationStore) DeleteApplication(ctx context.Context, appID model.ApplicationID) error {
	defer s.dropTokensOf(appID)
	return s.ApplicationStore.DeleteApplication(ctx, appID)
}

func (s *ApplicationStore) DeleteApplicationAuthToken(ctx context.Context, tokenID model.AuthTokenID) error {
	defer s.users.authTokenCache.Remove(string(tokenID))
	return s.ApplicationStore.DeleteApplicationAuthToken(ctx, tokenID)
}

func (s *ApplicationStore) dropTokensOf(appID model.ApplicationID) {
	s.users.authTokenCache.RemoveMatching(func(t *CacheableAuthToken) bool {
		app := t.Application()
		return app != nil && app.ID() == appID
	})
}

var _ port.ApplicationStore = &ApplicationStore{}
