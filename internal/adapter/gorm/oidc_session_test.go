package gorm_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	gormpkg "gorm.io/gorm"
)

const (
	sessionIssuer  = "https://id.example.test/"
	sessionSubject = "subject"
)

func newOIDCSession(tenant model.TenantID, subject string, startedAt time.Time) model.OIDCSession {
	return model.OIDCSession{
		TenantID:        tenant,
		Issuer:          sessionIssuer,
		Subject:         subject,
		AuthenticatedAt: startedAt,
		ExpiresAt:       time.Now().Add(time.Hour),
	}
}

func TestOIDCSessionLifecycle(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := context.Background()
		id, err := store.OpenSession(ctx, newOIDCSession(testTenantID, sessionSubject, time.Now()))
		require.NoError(t, err)
		require.NoError(t, store.CheckSession(ctx, id, testTenantID, sessionIssuer, sessionSubject))

		// A cookie only authenticates the identity and tenant it was opened for.
		require.ErrorIs(t, store.CheckSession(ctx, id, "other-tenant", sessionIssuer, sessionSubject), port.ErrNotFound)
		require.ErrorIs(t, store.CheckSession(ctx, id, testTenantID, "https://other.example.test/", sessionSubject), port.ErrNotFound)
		require.ErrorIs(t, store.CheckSession(ctx, id, testTenantID, sessionIssuer, "other"), port.ErrNotFound)
		require.ErrorIs(t, store.CheckSession(ctx, "", testTenantID, sessionIssuer, sessionSubject), port.ErrNotFound)

		require.NoError(t, store.CloseSession(ctx, id))
		require.ErrorIs(t, store.CheckSession(ctx, id, testTenantID, sessionIssuer, sessionSubject), port.ErrNotFound)
		require.NoError(t, store.CloseSession(ctx, id))
	})
}

func TestOIDCSessionRefusesMalformed(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := context.Background()
		expired := newOIDCSession(testTenantID, sessionSubject, time.Now())
		expired.ExpiresAt = time.Now().Add(-time.Second)
		future := newOIDCSession(testTenantID, sessionSubject, time.Now().Add(time.Hour))
		for _, session := range []model.OIDCSession{
			expired,
			future,
			newOIDCSession("", sessionSubject, time.Now()),
			newOIDCSession(testTenantID, "", time.Now()),
			newOIDCSession(testTenantID, sessionSubject, time.Time{}),
		} {
			_, err := store.OpenSession(ctx, session)
			require.ErrorIs(t, err, port.ErrNotAllowed)
		}
	})
}

// A logout revokes every session of the identity, in every tenant, refuses
// the sign-ins started before it, and is processed once.
func TestOIDCSessionRevocation(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := context.Background()
		before := time.Now().Add(-time.Second)
		first, err := store.OpenSession(ctx, newOIDCSession(testTenantID, sessionSubject, before))
		require.NoError(t, err)
		second, err := store.OpenSession(ctx, newOIDCSession("other-tenant", sessionSubject, before))
		require.NoError(t, err)
		bystander, err := store.OpenSession(ctx, newOIDCSession(testTenantID, "bystander", before))
		require.NoError(t, err)

		issued := time.Now()
		revoked, err := store.RevokeIdentitySessions(ctx, sessionIssuer, sessionSubject, "jti-1", issued, issued.Add(time.Minute))
		require.NoError(t, err)
		require.True(t, revoked)

		require.ErrorIs(t, store.CheckSession(ctx, first, testTenantID, sessionIssuer, sessionSubject), port.ErrNotFound)
		require.ErrorIs(t, store.CheckSession(ctx, second, "other-tenant", sessionIssuer, sessionSubject), port.ErrNotFound)
		require.NoError(t, store.CheckSession(ctx, bystander, testTenantID, sessionIssuer, "bystander"))

		// A sign-in started before the logout can not open a replacement.
		_, err = store.OpenSession(ctx, newOIDCSession(testTenantID, sessionSubject, before))
		require.ErrorIs(t, err, port.ErrNotAllowed)

		after, err := store.OpenSession(ctx, newOIDCSession(testTenantID, sessionSubject, time.Now().Add(time.Second)))
		require.NoError(t, err)

		// Replaying the token changes nothing: the new session stands.
		revoked, err = store.RevokeIdentitySessions(ctx, sessionIssuer, sessionSubject, "jti-1", issued, issued.Add(time.Minute))
		require.NoError(t, err)
		require.False(t, revoked)
		require.NoError(t, store.CheckSession(ctx, after, testTenantID, sessionIssuer, sessionSubject))

		_, err = store.RevokeIdentitySessions(ctx, sessionIssuer, sessionSubject, "", issued, issued)
		require.ErrorIs(t, err, port.ErrInvalid)
	})
}

// Sign-ins started before a logout and racing it never leave a session behind:
// each one either commits first and is revoked, or sees the revocation. Some
// commit before the revocation starts and one more opens after it ends, so
// that both outcomes are asserted whatever the interleaving of the others.
func TestOIDCSessionRevocationRacesSignIns(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := context.Background()
		started := time.Now().Add(-time.Second)
		const committed, racing = 4, 16

		type outcome struct {
			id  string
			err error
		}
		signIn := func() outcome {
			id, err := store.OpenSession(ctx, newOIDCSession(testTenantID, sessionSubject, started))
			return outcome{id, err}
		}

		outcomes := make([]outcome, committed, committed+racing)
		for i := range committed {
			outcomes[i] = signIn()
			require.NoError(t, outcomes[i].err)
		}

		raced := make([]outcome, racing)
		var revokeErr error
		var wg sync.WaitGroup
		for i := range racing {
			wg.Go(func() { raced[i] = signIn() })
		}
		wg.Go(func() {
			issued := time.Now()
			_, revokeErr = store.RevokeIdentitySessions(ctx, sessionIssuer, sessionSubject, "race", issued, issued.Add(time.Minute))
		})
		wg.Wait()
		require.NoError(t, revokeErr)

		require.ErrorIs(t, signIn().err, port.ErrNotAllowed)
		for _, o := range append(outcomes, raced...) {
			if o.err != nil {
				require.ErrorIs(t, o.err, port.ErrNotAllowed)
				continue
			}
			require.ErrorIs(t, store.CheckSession(ctx, o.id, testTenantID, sessionIssuer, sessionSubject), port.ErrNotFound)
		}
	})
}

// The sweep removes what can no longer serve, and keeps a replay key while
// its token may still be accepted.
func TestOIDCSessionSweep(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := context.Background()
		store := newSeededStore(t, db)
		const ttl = time.Hour
		now := time.Now()

		id, err := store.OpenSession(ctx, newOIDCSession(testTenantID, sessionSubject, now))
		require.NoError(t, err)
		revoked, err := store.RevokeIdentitySessions(ctx, sessionIssuer, "revoked", "jti", now, now.Add(time.Minute))
		require.NoError(t, err)
		require.True(t, revoked)

		count := func(table string) int64 {
			var n int64
			require.NoError(t, db.Table(table).Count(&n).Error)
			return n
		}

		// Within every window, nothing goes.
		require.NoError(t, store.SweepSessions(ctx, now.Add(9*time.Minute), ttl))
		require.NoError(t, store.CheckSession(ctx, id, testTenantID, sessionIssuer, sessionSubject))
		require.EqualValues(t, 1, count("oidc_logout_replays"))
		revoked, err = store.RevokeIdentitySessions(ctx, sessionIssuer, "revoked", "jti", now, now.Add(time.Minute))
		require.NoError(t, err)
		require.False(t, revoked, "a token still acceptable must stay refused as a replay")

		// Past its expiry, the token is refused for its age: its key goes.
		require.NoError(t, store.SweepSessions(ctx, now.Add(11*time.Minute), ttl))
		require.EqualValues(t, 0, count("oidc_logout_replays"))
		require.EqualValues(t, 1, count("oidc_sessions"))
		require.EqualValues(t, 2, count("oidc_identities"))

		require.NoError(t, store.SweepSessions(ctx, now.Add(ttl+time.Minute), ttl))
		require.EqualValues(t, 0, count("oidc_sessions"))
		require.EqualValues(t, 2, count("oidc_identities"), "a watermark outlives the sessions and sign-ins it refuses")

		require.NoError(t, store.SweepSessions(ctx, now.Add(model.OIDCIdentityRetention(ttl)+time.Minute), ttl))
		require.EqualValues(t, 0, count("oidc_identities"))
	})
}

// Checking a session, on every request, never waits on the publication lock
// of the event feed.
func TestOIDCSessionIgnoresFeedLock(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := context.Background()
		store := newSeededStore(t, db)
		id, err := store.OpenSession(ctx, newOIDCSession(testTenantID, sessionSubject, time.Now()))
		require.NoError(t, err)

		tx := db.Begin()
		require.NoError(t, tx.Error)
		t.Cleanup(func() { tx.Rollback() })
		require.NoError(t, xologorm.LockProvisioningFeed(tx))

		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		require.NoError(t, store.CheckSession(ctx, id, testTenantID, sessionIssuer, sessionSubject))
		if db.Dialector.Name() == "postgres" {
			// SQLite has a single writer: only PostgreSQL can tell.
			_, err := store.OpenSession(ctx, newOIDCSession(testTenantID, sessionSubject, time.Now()))
			require.NoError(t, err)
			issued := time.Now()
			_, err = store.RevokeIdentitySessions(ctx, sessionIssuer, sessionSubject, "lock", issued, issued.Add(time.Minute))
			require.NoError(t, err)
		}
	})
}
