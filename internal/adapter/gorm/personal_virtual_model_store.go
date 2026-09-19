package gorm

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/pkg/errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Store) CreatePersonalVirtualModel(ctx context.Context, vm model.PersonalVirtualModel) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		if err := db.Create(fromPersonalVirtualModel(vm)).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return errors.WithStack(port.ErrAlreadyExists)
			}
			return errors.WithStack(err)
		}
		return nil
	})
}

func (s *Store) GetPersonalVirtualModelByID(ctx context.Context, id model.PersonalVirtualModelID) (model.PersonalVirtualModel, error) {
	var vm PersonalVirtualModel
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
	return &wrappedPersonalVirtualModel{&vm}, nil
}

func (s *Store) GetPersonalVirtualModelByName(ctx context.Context, userID model.UserID, name string) (model.PersonalVirtualModel, error) {
	var vm PersonalVirtualModel
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		if err := db.First(&vm, "user_id = ? AND name = ?", string(userID), name).Error; err != nil {
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
	return &wrappedPersonalVirtualModel{&vm}, nil
}

func (s *Store) ListPersonalVirtualModels(ctx context.Context, userID model.UserID) ([]model.PersonalVirtualModel, error) {
	var vms []*PersonalVirtualModel
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Where("user_id = ?", string(userID)).Order("name ASC").Find(&vms).Error)
	})
	if err != nil {
		return nil, err
	}
	result := make([]model.PersonalVirtualModel, 0, len(vms))
	for _, vm := range vms {
		result = append(result, &wrappedPersonalVirtualModel{vm})
	}
	return result, nil
}

func (s *Store) SavePersonalVirtualModel(ctx context.Context, vm model.PersonalVirtualModel) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		// The ON CONFLICT (id) clause does not match a collision on the
		// (user_id, name) unique index, so a rename racing another
		// concurrent rename surfaces as gorm.ErrDuplicatedKey. Translate
		// it to port.ErrAlreadyExists so callers can map the rename race
		// to the same user-facing error as a pre-check collision.
		err := db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			UpdateAll: true,
		}).Create(fromPersonalVirtualModel(vm)).Error
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return errors.WithStack(port.ErrAlreadyExists)
		}
		return errors.WithStack(err)
	})
}

func (s *Store) DeletePersonalVirtualModel(ctx context.Context, id model.PersonalVirtualModelID) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		result := db.Delete(&PersonalVirtualModel{}, "id = ?", string(id))
		if result.Error != nil {
			return errors.WithStack(result.Error)
		}
		if result.RowsAffected == 0 {
			return errors.WithStack(port.ErrNotFound)
		}
		return nil
	})
}

var _ port.PersonalVirtualModelStore = &Store{}
