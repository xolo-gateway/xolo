package service

import (
	"context"
	"io"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/adoption"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// WithOwnershipPolicy declares the write authority of each family, as the
// stores enforce it. Omitted families are shared.
func WithOwnershipPolicy(policy model.OwnershipPolicy) ProvisioningServiceOptionFunc {
	return func(s *ProvisioningService) { s.ownership = policy.Effective() }
}

// WithInventoryReader serves the adoption export.
func WithInventoryReader(reader port.InventoryReader) ProvisioningServiceOptionFunc {
	return func(s *ProvisioningService) { s.inventory = reader }
}

// OwnershipPolicy returns the effective write authority of each family.
func (s *ProvisioningService) OwnershipPolicy() model.OwnershipPolicy {
	if s.ownership == nil {
		return model.OwnershipPolicy(nil).Effective()
	}
	return s.ownership
}

// ExportInventory writes the adoption export of the instance to w: every
// projection of the common contract on one snapshot, and the event cursor to
// resume from. It takes no lock.
func (s *ProvisioningService) ExportInventory(ctx context.Context, w io.Writer) error {
	if s.inventory == nil {
		return errors.New("adoption export requires an inventory reader")
	}
	return adoption.Export(ctx, s.inventory, w)
}
