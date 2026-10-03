package gorm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
)

func (s *Store) GetUserByDeclaredIdentity(ctx context.Context, tid model.TenantID, identity model.Identity) (model.User, error) {
	db, err := s.getDatabase(ctx)
	if err != nil {
		return nil, err
	}
	var row User
	err = db.WithContext(ctx).Preload("Roles").Preload("Preferences").Where("tenant_id = ? AND identity_issuer = ? AND identity_subject = ?", tid, identity.Issuer, identity.Subject).First(&row).Error
	if err == gorm.ErrRecordNotFound {
		return nil, port.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &wrappedUser{&row}, nil
}
func (s *Store) GetUserByEmail(ctx context.Context, tid model.TenantID, email string) (model.User, error) {
	db, err := s.getDatabase(ctx)
	if err != nil {
		return nil, err
	}
	var row User
	err = db.WithContext(ctx).Preload("Roles").Preload("Preferences").Where("tenant_id = ? AND email = ?", tid, email).First(&row).Error
	if err == gorm.ErrRecordNotFound {
		return nil, port.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &wrappedUser{&row}, nil
}

// SaveAuthenticatedUser is the narrow trusted login path: the service preserves
// managed profile fields and only attaches the proven link / bootstrap roles.
func (s *Store) SaveAuthenticatedUser(ctx context.Context, u model.User) error {
	if s.mutations == nil {
		return port.ErrNotAllowed
	}
	db, err := s.getDatabase(ctx)
	if err != nil {
		return err
	}
	if err := requireLive(db, model.CommonScope{Family: "member", TenantID: string(u.TenantID())}, string(u.ID())); err != nil {
		return err
	}
	old, err := s.GetUserByID(ctx, u.ID())
	if err != nil && !errors.Is(err, port.ErrNotFound) {
		return err
	}
	if old == nil {
		if err := s.checkRetiredLogin(ctx, u); err != nil {
			return err
		}
	}
	if old != nil {
		a, b := fromUser(old), fromUser(u)
		// Ignore association back-pointers and generated timestamps for equality.
		aa, _ := json.Marshal([]any{a.Provider, a.Subject, a.Email, a.DisplayName, a.Active, a.TenantRole, old.Roles(), old.DeclaredIdentity()})
		bb, _ := json.Marshal([]any{b.Provider, b.Subject, b.Email, b.DisplayName, b.Active, b.TenantRole, u.Roles(), u.DeclaredIdentity()})
		if bytes.Equal(aa, bb) {
			return nil
		}
	}
	if err := s.track(ctx, "member", string(u.ID())); err != nil {
		return err
	}
	s.mutations.authenticatedMember = string(u.ID())
	s.mutations.actor = model.ActorFromContext(ctx)
	return s.saveUser(ctx, u)
}

// ConfigureIdentityProviders installs the trusted discovery issuer mapping at startup.
func (s *Store) ConfigureIdentityProviders(providers map[string]string) {
	s.identityProviders = make(map[string]string, len(providers))
	for k, v := range providers {
		s.identityProviders[k] = v
	}
}

func (s *Store) identityIssuer(provider string) string {
	if issuer := s.identityProviders[provider]; issuer != "" {
		return issuer
	}
	return provider
}
func (s *Store) identityAliases(provider string) []string {
	issuer := s.identityIssuer(provider)
	out := []string{issuer}
	for alias, v := range s.identityProviders {
		if v == issuer && alias != issuer {
			out = append(out, alias)
		}
	}
	return out
}
