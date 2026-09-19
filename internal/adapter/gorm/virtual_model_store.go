package gorm

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/pkg/errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Store) CreateVirtualModel(ctx context.Context, vm model.VirtualModel) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		if err := db.Create(fromVirtualModel(vm)).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return errors.WithStack(port.ErrAlreadyExists)
			}
			return errors.WithStack(err)
		}
		return nil
	})
}

func (s *Store) GetVirtualModelByID(ctx context.Context, id model.VirtualModelID) (model.VirtualModel, error) {
	var vm VirtualModel
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		if err := db.First(&vm, "id = ?", string(id)).Error; err != nil {
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
	return &wrappedVirtualModel{&vm}, nil
}

func (s *Store) GetVirtualModelByName(ctx context.Context, orgID model.OrgID, name string) (model.VirtualModel, error) {
	var vm VirtualModel
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		if err := db.First(&vm, "org_id = ? AND name = ?", string(orgID), name).Error; err != nil {
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
	return &wrappedVirtualModel{&vm}, nil
}

func (s *Store) ListVirtualModels(ctx context.Context, orgID model.OrgID) ([]model.VirtualModel, error) {
	var vms []*VirtualModel
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Where("org_id = ?", string(orgID)).Order("name ASC").Find(&vms).Error)
	})
	if err != nil {
		return nil, err
	}
	result := make([]model.VirtualModel, 0, len(vms))
	for _, vm := range vms {
		result = append(result, &wrappedVirtualModel{vm})
	}
	return result, nil
}

func (s *Store) SaveVirtualModel(ctx context.Context, vm model.VirtualModel) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		// The ON CONFLICT (id) clause does not match a collision on the
		// idx_org_name unique index, so a rename racing another
		// concurrent rename surfaces as gorm.ErrDuplicatedKey. Translate
		// it to port.ErrAlreadyExists so callers can map the rename race
		// to the same user-facing error as a pre-check collision.
		err := db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			UpdateAll: true,
		}).Create(fromVirtualModel(vm)).Error
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return errors.WithStack(port.ErrAlreadyExists)
		}
		return errors.WithStack(err)
	})
}

func (s *Store) DeleteVirtualModel(ctx context.Context, id model.VirtualModelID) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		result := db.Delete(&VirtualModel{}, "id = ?", string(id))
		if result.Error != nil {
			return errors.WithStack(result.Error)
		}
		if result.RowsAffected == 0 {
			return errors.WithStack(port.ErrNotFound)
		}
		return nil
	})
}

var _ port.VirtualModelStore = &Store{}
