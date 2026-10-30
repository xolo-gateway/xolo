package gorm

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"reflect"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
)

// OwnershipPolicy returns the write authority of each family, nil when the
// store checks none.
func (s *Store) OwnershipPolicy() model.OwnershipPolicy {
	return s.ownership
}

// checkOwnership refuses a write to family when the policy reserves it to
// another authority than the one of ctx. Writers whose effect has no
// projection (invitations, webhook subscriptions) call it themselves; every
// other identity write is checked when its projections are published.
func (s *Store) checkOwnership(ctx context.Context, family string) error {
	if !s.ownership.Allows(family, model.WriteAuthority(ctx)) {
		return errors.Wrapf(port.ErrOwnershipDenied, "%s", family)
	}
	return nil
}

// authorizeProjections checks the write authority of every projection a
// transaction changes, cascades included: the transaction rolls back as a
// whole when one of them belongs to another authority. Only the public
// representation is governed, so the fields outside the common contract
// (platform roles, organization settings, custom roles) stay local.
//
// A sign-in may write the very account it resolves, so that a member owned
// by the control plane can still link its proven identity. A member choosing
// their language may change the locale of their own representation, and
// nothing else of it: localeOnly reports whether the transaction changed
// nothing of the member row but its locale, columns the representation does
// not carry included.
func authorizeProjections(ctx context.Context, ownership model.OwnershipPolicy, changes []projectionChange, localeOnly func(model.UserID) (bool, error)) error {
	if ownership == nil {
		return nil
	}
	authority := model.WriteAuthority(ctx)
	signIn, signingIn := model.SignInAccount(ctx)
	localeOf, choosingLocale := model.LocaleChange(ctx)
	for _, change := range changes {
		family := change.id.family
		if ownership.Allows(family, authority) {
			continue
		}
		if authority == model.OwnerLocal && family == model.FamilyMember {
			if signingIn && change.id.key == string(signIn) {
				continue
			}
			if choosingLocale && change.id.key == string(localeOf) && differsOnlyByLocale(change) {
				only, err := localeOnly(localeOf)
				if err != nil {
					return err
				}
				if only {
					continue
				}
			}
		}
		return errors.Wrapf(port.ErrOwnershipDenied, "%s", family)
	}
	return nil
}

// differsOnlyByLocale reports whether a change keeps a member and every
// field of its representation but locale.
func differsOnlyByLocale(change projectionChange) bool {
	if change.stored == nil || change.want == nil {
		return false
	}
	var stored, want map[string]json.RawMessage
	if json.Unmarshal([]byte(change.stored.Representation), &stored) != nil || json.Unmarshal([]byte(change.want.Representation), &want) != nil {
		return false
	}
	return equalButLocale(stored, want)
}

// snapshotDiffersOnlyByLocale reports whether the user row of id, as its
// mutation snapshot records it, kept every column but locale since before.
func snapshotDiffersOnlyByLocale(db *gorm.DB, before map[mutationKey][]byte, id model.UserID) (bool, error) {
	key := mutationKey{kind: "user", id: string(id)}
	old, tracked := before[key]
	if !tracked {
		return false, nil
	}
	current, err := mutationSnapshot(db, key)
	if err != nil {
		return false, err
	}
	var stored, want map[string]json.RawMessage
	if json.Unmarshal(old, &stored) != nil || json.Unmarshal(current, &want) != nil || stored == nil || want == nil {
		return false, nil
	}
	return equalButLocale(stored, want), nil
}

// assertOnlyLocalePreference refuses, for a locale change, a preferences row
// that differs from the stored one by anything but its locale. The mutation
// snapshot carries only the locale of the preferences, so the other columns
// are checked here, where the row is written.
func assertOnlyLocalePreference(db *gorm.DB, want *UserPreferences) error {
	var rows []UserPreferences
	if err := db.Where("user_id = ?", want.UserID).Limit(1).Find(&rows).Error; err != nil {
		return errors.WithStack(err)
	}
	stored := UserPreferences{UserID: want.UserID}
	if len(rows) != 0 {
		stored = rows[0]
	}
	changed := *want
	stored.ID, changed.ID = 0, 0
	stored.Locale, changed.Locale = "", ""
	if !reflect.DeepEqual(stored, changed) {
		return errors.Wrapf(port.ErrOwnershipDenied, "%s", model.FamilyMember)
	}
	return nil
}

func equalButLocale(a, b map[string]json.RawMessage) bool {
	delete(a, "locale")
	delete(b, "locale")
	return maps.EqualFunc(a, b, func(x, y json.RawMessage) bool { return bytes.Equal(x, y) })
}
