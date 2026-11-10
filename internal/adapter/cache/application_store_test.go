package cache

import (
	"context"
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

type applicationBackend struct{ port.ApplicationStore }

func (applicationBackend) UpdateApplication(context.Context, model.Application) error   { return nil }
func (applicationBackend) DeleteApplication(context.Context, model.ApplicationID) error { return nil }
func (applicationBackend) DeleteApplicationAuthToken(context.Context, model.AuthTokenID) error {
	return nil
}

// Deactivating an application, deleting it or revoking one of its tokens
// drops the cached tokens carrying it, and announces it to the other
// instances.
func TestApplicationChangesDropItsCachedTokens(t *testing.T) {
	cases := map[string]func(*ApplicationStore, model.Application, model.AuthToken) error{
		"update": func(s *ApplicationStore, app model.Application, _ model.AuthToken) error {
			return s.UpdateApplication(context.Background(), app)
		},
		"delete": func(s *ApplicationStore, app model.Application, _ model.AuthToken) error {
			return s.DeleteApplication(context.Background(), app.ID())
		},
		"revoke": func(s *ApplicationStore, _ model.Application, token model.AuthToken) error {
			return s.DeleteApplicationAuthToken(context.Background(), token.ID())
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			generations := &sharedGenerations{generations: map[string]int64{}}
			users := NewUserStore(nil, 10, time.Hour)
			users.ShareInvalidations(NewCoherence(generations, time.Hour))

			app := model.NewApplication("org-1", "ci", "", true)
			token := model.NewApplicationAuthToken(app, "org-1", "ci", "secret", nil)
			users.authTokenCache.Add(NewCacheableAuthToken(token, "hash-1"))

			if err := change(NewApplicationStore(applicationBackend{}, users), app, token); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if _, cached := users.authTokenCache.Get("hash-1"); cached {
				t.Fatal("the application token is still cached")
			}
			if generations.generations["auth_tokens"] == 0 {
				t.Fatal("the other instances were not told")
			}
		})
	}
}
