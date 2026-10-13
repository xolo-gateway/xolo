package gorm

import (
	"context"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Store) CreatePersonalVirtualModel(ctx context.Context, vm model.PersonalVirtualModel) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		if err := db.Create(fromPersonalVirtualModel(vm)).Error; err != nil {
			// See CreateVirtualModel: gorm.ErrDuplicatedKey is never
			// produced here because TranslateError is off; use the
			// isUniqueViolation helper to translate a collision on
			// idx_pvm_user_name into port.ErrAlreadyExists.
			if isUniqueViolation(err, "user_id", "name") {
				return errors.WithStack(port.ErrAlreadyExists)
			}
			return errors.WithStack(err)
		}
		return nil
	})
}

func (s *Store) GetPersonalVirtualModelByID(ctx context.Context, id model.PersonalVirtualModelID) (model.PersonalVirtualModel, error) {
	var vm PersonalVirtualModel
	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
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
	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
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
	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
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
		// idx_pvm_user_name unique index, so a rename racing another
		// concurrent rename raises the driver's unique-constraint error.
		// The driver error reaches us as *sqlite3.Error (SQLITE_CONSTRAINT)
		// or *pgconn.PgError (SQLSTATE 23505) — gorm.ErrDuplicatedKey is
		// only produced when db.Config.TranslateError is true, which we do
		// not set. isUniqueViolation (dialect.go) handles both backends and
		// matches the index name fragments to disambiguate from any other
		// constraint. Translate to port.ErrAlreadyExists so callers map the
		// rename race to the same user-facing error as a pre-check
		// collision.
		err := db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			UpdateAll: true,
		}).Create(fromPersonalVirtualModel(vm)).Error
		if isUniqueViolation(err, "user_id", "name") {
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
