package gorm

import (
	"context"
	"encoding/json"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// ConfigureOwnership must run before any application writers start.
func (s *Store) ConfigureOwnership(p model.OwnershipPolicy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	s.ownership = p.Effective()
	return nil
}
func (s *Store) OwnershipPolicy() model.OwnershipPolicy { return s.ownership.Effective() }
func (s *Store) checkOwnership(ctx context.Context, family string) error {
	// Unconfigured stores are used by migrations and low-level embedding callers.
	if s.ownership == nil {
		return nil
	}
	switch family {
	case "domain":
		family = "tenant_domain"
	case "role":
		if s.businessEnabled {
			family = "custom_role"
		} else {
			family = "organization_membership"
		}
	case "membership", "invitation":
		family = "organization_membership"
	}
	if s.ownership[family] != model.WriteAuthority(ctx) {
		return port.ErrOwnershipDenied
	}
	return nil
}

// auditControlOperation records operator/subscription actions without credentials.
func (s *Store) auditControlOperation(ctx context.Context, resource, id, action string) error {
	db, err := s.getDatabase(ctx)
	if err != nil {
		return err
	}
	if err := db.Exec("UPDATE publication_clocks SET sequence = sequence + 1 WHERE id = 1").Error; err != nil {
		return err
	}
	var clock PublicationClock
	if err := db.First(&clock, 1).Error; err != nil {
		return err
	}
	actor, err := json.Marshal(s.mutations.actor)
	if err != nil {
		return err
	}
	after, err := json.Marshal(map[string]string{"action": action})
	if err != nil {
		return err
	}
	return db.Create(&MutationAudit{Sequence: clock.Sequence, Actor: string(actor), RequestID: s.mutations.actor.RequestID, Resource: resource, ResourceID: id, Before: "null", After: string(after)}).Error
}
