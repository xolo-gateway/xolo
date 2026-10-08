package gorm

import (
	"context"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
)

// CreateInvite implements port.InviteStore.
func (s *Store) CreateInvite(ctx context.Context, invite model.InviteToken) error {
	// An invitation produces memberships: it belongs to their authority.
	if err := s.checkOwnership(ctx, model.FamilyOrganizationMembership); err != nil {
		return err
	}
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
	// An invitation produces memberships: it belongs to their authority.
	if err := s.checkOwnership(ctx, model.FamilyOrganizationMembership); err != nil {
		return err
	}
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
	// An invitation produces memberships: it belongs to their authority.
	if err := s.checkOwnership(ctx, model.FamilyOrganizationMembership); err != nil {
		return err
	}
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
		result := db.Model(&InviteToken{}).
			Where("id = ? AND revoked_at IS NULL", string(id)).
			Scopes(unexpiredInvitations(time.Now())).
			Where("max_uses IS NULL OR uses_count < max_uses").
			Where("invitee_email IS NULL OR uses_count = 0").
			UpdateColumn("uses_count", gorm.Expr("uses_count + 1"))
		if result.Error != nil {
			return errors.WithStack(result.Error)
		}
		if result.RowsAffected != 1 {
			return port.ErrInvalid
		}
		return nil
	})
}

// ListPendingInvitesForEmail implements port.InviteStore. Invitee addresses are
// stored normalized (model.NormalizeEmail, with migration 202610070002 for older
// rows), so normalizing the parameter is enough for a plain equality, which
// keeps the invitee_email index usable on this sign-in path.
func (s *Store) ListPendingInvitesForEmail(ctx context.Context, tenantID model.TenantID, email string) ([]model.InviteToken, error) {
	if tenantID == "" {
		return nil, port.ErrInvalid
	}
	email = model.NormalizeEmail(email)
	// A blank address names nobody, even an account that has none.
	if email == "" {
		return nil, nil
	}
	var tokens []*InviteToken
	now := time.Now()
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Preload("Org").
			Joins("JOIN organizations ON organizations.id = invite_tokens.org_id").
			Where("organizations.tenant_id = ? AND organizations.active <> 0", string(tenantID)).
			Where("invite_tokens.invitee_email = ? AND invite_tokens.revoked_at IS NULL", email).
			Scopes(unexpiredInvitations(now)).
			Where("(invite_tokens.max_uses IS NULL OR invite_tokens.uses_count < invite_tokens.max_uses) AND invite_tokens.uses_count = 0").
			Order("invite_tokens.created_at DESC").
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

// SQLite stores RFC3339 text, whose offsets and fractional seconds cannot be
// compared lexically. Compare instants for legacy rows as well as new UTC dates.
func unexpiredInvitations(now time.Time) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if isSQLite(db) {
			return db.Where("invite_tokens.expires_at IS NULL OR julianday(invite_tokens.expires_at) > julianday(?)", now)
		}
		return db.Where("invite_tokens.expires_at IS NULL OR invite_tokens.expires_at > ?", now)
	}
}
