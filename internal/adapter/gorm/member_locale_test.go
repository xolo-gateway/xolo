package gorm_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	gormpkg "gorm.io/gorm"
)

// controlPlaneRequestID correlates the writes of the control plane in these
// tests: a change Xolo makes itself must never reuse it.
const controlPlaneRequestID = "0123456789abcdef0123456789abcdef"

func memberLocaleOf(t *testing.T, store *xologorm.Store, id model.UserID) (string, string) {
	t.Helper()
	item := memberProjection(t, store, testTenantID, id)
	var rep map[string]any
	require.NoError(t, json.Unmarshal(item.Representation, &rep))
	locale, _ := rep["locale"].(string)
	return locale, item.ETag
}

func eventsSince(t *testing.T, db *gormpkg.DB, after int64) []model.CommonEvent {
	t.Helper()
	var rows []xologorm.ProvisioningEvent
	require.NoError(t, db.Where("sequence > ?", after).Order("sequence").Find(&rows).Error)
	out := make([]model.CommonEvent, 0, len(rows))
	for _, row := range rows {
		var event model.CommonEvent
		require.NoError(t, json.Unmarshal([]byte(row.Payload), &event))
		out = append(out, event)
	}
	return out
}

func lastSequence(t *testing.T, db *gormpkg.DB) int64 {
	t.Helper()
	var head int64
	require.NoError(t, db.Model(&xologorm.ProvisioningEvent{}).Select("COALESCE(MAX(sequence), 0)").Scan(&head).Error)
	return head
}

// TestMemberLocaleUnderOwnership: while the control plane owns members, a
// member still chooses their own language, and only that.
func TestMemberLocaleUnderOwnership(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		newSeededStore(t, db)
		store := ownedStore(t, db, model.OwnershipPolicy{model.FamilyMember: model.OwnerControlPlane})
		svc := newIdentityService(store)
		locales := service.NewMemberLocale(store)

		cp := model.WithActor(controlPlane(ctx), model.Actor{URI: "urn:test:control-plane", RequestID: controlPlaneRequestID})
		alice := model.NewUserID()
		member := service.CommonMember{Email: "alice@example.test", DisplayName: "Alice", Locale: "fr", TenantRole: model.TenantRoleMember, Status: model.StatusActive}
		_, err := svc.PutTenantMember(cp, testTenantID, alice, member)
		require.NoError(t, err)
		locale, etag := memberLocaleOf(t, store, alice)
		require.Equal(t, "fr", locale)

		head, audits := lastSequence(t, db), auditCount(t, db)
		require.NoError(t, locales.Set(ctx, testTenantID, alice, "es"))
		locale, changed := memberLocaleOf(t, store, alice)
		require.Equal(t, "es", locale)
		require.NotEqual(t, etag, changed)
		events := eventsSince(t, db, head)
		require.Len(t, events, 1)
		require.Equal(t, "member.updated.v1", events[0].Type)
		require.Equal(t, changed, events[0].Data.ETag)
		require.Len(t, events[0].RequestID, 32)
		require.NotEqual(t, controlPlaneRequestID, events[0].RequestID, "Xolo generates its own request ID")
		require.Equal(t, audits+1, auditCount(t, db), "the change is audited")
		require.Contains(t, signInActors(t, db, alice), alice, "with the member as actor")

		stored, err := store.GetUserByID(ctx, alice)
		require.NoError(t, err)
		require.Equal(t, "Alice", stored.DisplayName(), "every other field is kept")
		require.Equal(t, "alice@example.test", stored.Email())

		t.Run("choosing the stored locale again is a no-op", func(t *testing.T) {
			head, audits := lastSequence(t, db), auditCount(t, db)
			require.NoError(t, locales.Set(ctx, testTenantID, alice, "es"))
			require.NoError(t, locales.Set(ctx, testTenantID, alice, " es "))
			require.Empty(t, eventsSince(t, db, head))
			require.Equal(t, audits, auditCount(t, db))
			_, etag := memberLocaleOf(t, store, alice)
			require.Equal(t, changed, etag)
		})

		t.Run("a non-canonical tag is refused", func(t *testing.T) {
			for _, invalid := range []string{"es-es", "ES", "es_ES", "toolong-Latn-US"} {
				require.ErrorIs(t, locales.Set(ctx, testTenantID, alice, invalid), port.ErrInvalid, invalid)
			}
		})

		t.Run("only an active member of the tenant", func(t *testing.T) {
			require.ErrorIs(t, locales.Set(ctx, model.TenantID(model.NewUserID()), alice, "en"), port.ErrNotFound, "another tenant")
			carol := model.NewUserID()
			_, err := svc.PutTenantMember(cp, testTenantID, carol, service.CommonMember{Email: "carol@example.test", TenantRole: model.TenantRoleMember, Status: model.StatusSuspended})
			require.NoError(t, err)
			require.ErrorIs(t, locales.Set(ctx, testTenantID, carol, "en"), port.ErrNotAllowed, "suspended")
		})

		t.Run("a tag Xolo does not offer is kept", func(t *testing.T) {
			require.NoError(t, locales.Set(ctx, testTenantID, alice, "pt-BR"))
			locale, _ := memberLocaleOf(t, store, alice)
			require.Equal(t, "pt-BR", locale)
		})

		t.Run("clearing omits the locale", func(t *testing.T) {
			head := lastSequence(t, db)
			require.NoError(t, locales.Set(ctx, testTenantID, alice, ""))
			locale, _ := memberLocaleOf(t, store, alice)
			require.Empty(t, locale)
			require.Len(t, eventsSince(t, db, head), 1)
		})

		t.Run("every other member field stays refused locally", func(t *testing.T) {
			user, err := store.GetUserByID(ctx, alice)
			require.NoError(t, err)
			next := model.CopyUser(user)
			next.SetDisplayName("Changed")
			next.SetPreferences(model.NewUserPreferences(model.SetUserPreferencesLocale("en")))
			require.ErrorIs(t, store.SaveUser(model.WithLocaleChange(ctx, alice), next), port.ErrOwnershipDenied, "a locale change writes nothing else")

			// Columns the representation does not carry are checked too.
			admin := model.CopyUser(user)
			admin.SetRoles(model.PlatformRoleAdmin)
			admin.SetPreferences(model.NewUserPreferences(model.SetUserPreferencesLocale("en")))
			require.ErrorIs(t, store.SaveUser(model.WithLocaleChange(ctx, alice), admin), port.ErrOwnershipDenied, "nor platform roles")
			linked := model.CopyUser(user)
			linked.SetAuthenticationLink("oidc", "elsewhere")
			linked.SetPreferences(model.NewUserPreferences(model.SetUserPreferencesLocale("en")))
			require.ErrorIs(t, store.SaveUser(model.WithLocaleChange(ctx, alice), linked), port.ErrOwnershipDenied, "nor the sign-in link")
			dark, enabled := model.CopyUser(user), true
			dark.SetPreferences(model.NewUserPreferences(model.SetUserPreferencesLocale("en"), model.SetUserPrefencesDarkMode(&enabled)))
			require.ErrorIs(t, store.SaveUser(model.WithLocaleChange(ctx, alice), dark), port.ErrOwnershipDenied, "nor the other preferences")

			bob := model.NewUserID()
			_, err = svc.PutTenantMember(cp, testTenantID, bob, service.CommonMember{Email: "bob@example.test", TenantRole: model.TenantRoleMember, Status: model.StatusActive})
			require.NoError(t, err)
			other, err := store.GetUserByID(ctx, bob)
			require.NoError(t, err)
			changed := model.CopyUser(other)
			changed.SetPreferences(model.NewUserPreferences(model.SetUserPreferencesLocale("en")))
			require.ErrorIs(t, store.SaveUser(model.WithLocaleChange(ctx, alice), changed), port.ErrOwnershipDenied, "only the member's own locale")
			require.ErrorIs(t, store.SaveUser(ctx, changed), port.ErrOwnershipDenied, "an ordinary local write stays refused")
		})

		t.Run("a later control-plane PUT replaces the locale", func(t *testing.T) {
			require.NoError(t, locales.Set(ctx, testTenantID, alice, "es"))
			member.Locale = "en"
			_, err := svc.PutTenantMember(cp, testTenantID, alice, member)
			require.NoError(t, err)
			locale, _ := memberLocaleOf(t, store, alice)
			require.Equal(t, "en", locale)
		})
	})
}

// TestMemberLocaleRollback: a change rolled back after its event was
// appended exposes neither the locale, the audit nor the event.
func TestMemberLocaleRollback(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		newSeededStore(t, db)
		store := ownedStore(t, db, model.OwnershipPolicy{model.FamilyMember: model.OwnerControlPlane})
		svc := newIdentityService(store)
		alice := model.NewUserID()
		_, err := svc.PutTenantMember(controlPlane(ctx), testTenantID, alice, service.CommonMember{Email: "alice@example.test", Locale: "fr", TenantRole: model.TenantRoleMember, Status: model.StatusActive})
		require.NoError(t, err)
		_, etag := memberLocaleOf(t, store, alice)

		boom := errors.New("boom")
		require.NoError(t, db.Callback().Create().After("gorm:create").Register("test:fail_member_event", func(tx *gormpkg.DB) {
			if event, ok := tx.Statement.Dest.(*xologorm.ProvisioningEvent); ok && event.Family == model.FamilyMember {
				_ = tx.AddError(boom)
			}
		}))
		head, audits := lastSequence(t, db), auditCount(t, db)
		err = service.NewMemberLocale(store).Set(ctx, testTenantID, alice, "es")
		require.NoError(t, db.Callback().Create().Remove("test:fail_member_event"))
		require.ErrorIs(t, err, boom)

		locale, after := memberLocaleOf(t, store, alice)
		require.Equal(t, "fr", locale)
		require.Equal(t, etag, after)
		require.Empty(t, eventsSince(t, db, head))
		require.Equal(t, audits, auditCount(t, db))
		stored, err := store.GetUserByID(context.Background(), alice)
		require.NoError(t, err)
		require.Equal(t, "fr", stored.Preferences().Locale())
	})
}

// TestMemberPutLocale: the locale is part of the member: an identical PUT
// keeps its ETag and publishes nothing, a changed one publishes one event.
func TestMemberPutLocale(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := newSeededStore(t, db)
		svc := newIdentityService(store)
		alice := model.NewUserID()
		member := service.CommonMember{Email: "alice@example.test", Locale: " fr-CA ", TenantRole: model.TenantRoleMember, Status: model.StatusActive}
		_, err := svc.PutTenantMember(ctx, testTenantID, alice, member)
		require.NoError(t, err)
		locale, etag := memberLocaleOf(t, store, alice)
		require.Equal(t, "fr-CA", locale, "trimmed, case kept")

		head := lastSequence(t, db)
		member.Locale = "fr-CA"
		_, err = svc.PutTenantMember(ctx, testTenantID, alice, member)
		require.NoError(t, err)
		_, same := memberLocaleOf(t, store, alice)
		require.Equal(t, etag, same)
		require.Empty(t, eventsSince(t, db, head))

		member.Locale = ""
		_, err = svc.PutTenantMember(ctx, testTenantID, alice, member)
		require.NoError(t, err)
		locale, _ = memberLocaleOf(t, store, alice)
		require.Empty(t, locale, "omitting the locale clears it")
		require.Len(t, eventsSince(t, db, head), 1)

		member.Locale = "fr-fr"
		_, err = svc.PutTenantMember(ctx, testTenantID, alice, member)
		require.ErrorIs(t, err, port.ErrInvalid)
	})
}
