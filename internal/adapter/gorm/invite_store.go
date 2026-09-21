package gorm

import (
	"context"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/pkg/errors"
	"gorm.io/gorm"
)

// CreateInvite implements port.InviteStore.
func (s *Store) CreateInvite(ctx context.Context, invite model.InviteToken) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Create(fromInviteToken(invite)).Error)
	})
}

// GetInviteByID implements port.InviteStore.
func (s *Store) GetInviteByID(ctx context.Context, id model.InviteTokenID) (model.InviteToken, error) {
	var t InviteToken
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		if err := db.Preload("Org").First(&t, "id = ?", string(id)).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.WithStack(port.ErrNotFound)
			}
			return errors.WithStack(err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &wrappedInviteToken{&t}, nil
}

// ListInvites implements port.InviteStore.
func (s *Store) ListInvites(ctx context.Context, orgID model.OrgID) ([]model.InviteToken, error) {
	var tokens []*InviteToken
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Preload("Org").
			Where("org_id = ?", string(orgID)).
			Order("created_at DESC").
			Find(&tokens).Error)
	})
	if err != nil {
		return nil, err
	}
	result := make([]model.InviteToken, 0, len(tokens))
	for _, t := range tokens {
		result = append(result, &wrappedInviteToken{t})
	}
	return result, nil
}

// RevokeInvite implements port.InviteStore.
func (s *Store) RevokeInvite(ctx context.Context, id model.InviteTokenID) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		now := time.Now()
		result := db.Model(&InviteToken{}).Where("id = ?", string(id)).Update("revoked_at", now)
		if result.Error != nil {
			return errors.WithStack(result.Error)
		}
		if result.RowsAffected == 0 {
			return errors.WithStack(port.ErrNotFound)
		}
		return nil
	})
}

// DeleteInvite implements port.InviteStore.
func (s *Store) DeleteInvite(ctx context.Context, id model.InviteTokenID) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		result := db.Delete(&InviteToken{}, "id = ?", string(id))
		if result.Error != nil {
			return errors.WithStack(result.Error)
		}
		if result.RowsAffected == 0 {
			return errors.WithStack(port.ErrNotFound)
		}
		return nil
	})
}

// IncrementInviteUses implements port.InviteStore.
func (s *Store) IncrementInviteUses(ctx context.Context, id model.InviteTokenID) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Model(&InviteToken{}).
			Where("id = ?", string(id)).
			UpdateColumn("uses_count", gorm.Expr("uses_count + 1")).Error)
	})
}

// ListPendingInvitesForEmail implements port.InviteStore.
func (s *Store) ListPendingInvitesForEmail(ctx context.Context, email string) ([]model.InviteToken, error) {
	var tokens []*InviteToken
	now := time.Now()
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		// E-mail addresses are compared case-insensitively: what an administrator
		// typed in the invitation form and what the identity provider returns
		// rarely agree on case, and a mismatch used to silently hide the
		// invitation from its addressee.
		return errors.WithStack(db.Preload("Org").
			Where("LOWER(invitee_email) = LOWER(?) AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)", email, now).
			Order("created_at DESC").
			Find(&tokens).Error)
	})
	if err != nil {
		return nil, err
	}
	result := make([]model.InviteToken, 0, len(tokens))
	for _, t := range tokens {
		result = append(result, &wrappedInviteToken{t})
	}
	return result, nil
}

var _ port.InviteStore = &Store{}
