package gorm

import (
	"context"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/crypto"
	"gorm.io/gorm"
)

func fromApplication(a model.Application) *Application {
	return &Application{
		ID:          string(a.ID()),
		OrgID:       string(a.OrgID()),
		Name:        a.Name(),
		Description: a.Description(),
		Active:      a.Active(),
	}
}

func fromAuthTokenForApplication(t model.AuthToken) *AuthToken {
	var ownerID, applicationID *string

	if t.Owner() != nil {
		id := string(t.Owner().ID())
		ownerID = &id
	}
	if t.Application() != nil {
		id := string(t.Application().ID())
		applicationID = &id
	}

	return &AuthToken{
		ID:            string(t.ID()),
		OwnerID:       ownerID,
		ApplicationID: applicationID,
		Label:         t.Label(),
		Value:         crypto.HashToken(t.Value()),
		OrgID:         string(t.OrgID()),
		ExpiresAt:     t.ExpiresAt(),
	}
}

// CreateApplication implements port.ApplicationStore.
func (s *Store) CreateApplication(ctx context.Context, app model.Application) error {
	err := s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		gormApp := fromApplication(app)

		if err := db.Create(gormApp).Error; err != nil {
			return errors.WithStack(err)
		}

		return nil
	})
	if err != nil {
		return errors.WithStack(err)
	}

	return nil
}

// GetApplication implements port.ApplicationStore.
func (s *Store) GetApplication(ctx context.Context, appID model.ApplicationID) (model.Application, error) {
	var app Application

	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		if err := db.First(&app, "id = ?", string(appID)).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.WithStack(port.ErrNotFound)
			}
			return errors.WithStack(err)
		}
		return nil
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return &wrappedApplication{&app}, nil
}

// QueryApplications implements port.ApplicationStore.
func (s *Store) QueryApplications(ctx context.Context, orgID model.OrgID) ([]model.Application, error) {
	var apps []*Application

	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		query := db.Model(&Application{}).Where("org_id = ?", string(orgID))

		if err := query.Find(&apps).Error; err != nil {
			return errors.WithStack(err)
		}

		return nil
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	wrappedApps := make([]model.Application, 0, len(apps))
	for _, a := range apps {
		wrappedApps = append(wrappedApps, &wrappedApplication{a})
	}

	return wrappedApps, nil
}

// UpdateApplication implements port.ApplicationStore.
func (s *Store) UpdateApplication(ctx context.Context, app model.Application) error {
	err := s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		gormApp := fromApplication(app)

		result := db.Model(&Application{}).Where("id = ?", string(app.ID())).Updates(map[string]interface{}{
			"name":        gormApp.Name,
			"description": gormApp.Description,
			"active":      gormApp.Active,
		})
		if result.Error != nil {
			return errors.WithStack(result.Error)
		}

		if result.RowsAffected == 0 {
			return errors.WithStack(port.ErrNotFound)
		}

		return nil
	})
	if err != nil {
		return errors.WithStack(err)
	}

	return nil
}

// DeleteApplication implements port.ApplicationStore.
func (s *Store) DeleteApplication(ctx context.Context, appID model.ApplicationID) error {
	err := s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		result := db.Delete(&Application{}, "id = ?", string(appID))
		if result.Error != nil {
			return errors.WithStack(result.Error)
		}

		if result.RowsAffected == 0 {
			return errors.WithStack(port.ErrNotFound)
		}

		// Role assignments are not covered by a FK constraint, so drop them here
		// to avoid leaving grants behind that a recycled ID could inherit.
		if err := db.Where("application_id = ?", string(appID)).Delete(&ApplicationRole{}).Error; err != nil {
			return errors.WithStack(err)
		}

		return nil
	})
	if err != nil {
		return errors.WithStack(err)
	}

	return nil
}

// FindApplicationAuthToken implements port.ApplicationStore.
func (s *Store) FindApplicationAuthToken(ctx context.Context, token string) (model.AuthToken, error) {
	var authToken AuthToken

	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		if err := db.Preload("Application").First(&authToken, "value = ?", crypto.HashToken(token)).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.WithStack(port.ErrNotFound)
			}
			return errors.WithStack(err)
		}
		return nil
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	// Enforce expiry: treat expired tokens as not found
	if authToken.ExpiresAt != nil && time.Now().After(*authToken.ExpiresAt) {
		return nil, errors.WithStack(port.ErrNotFound)
	}

	return &wrappedApplicationAuthToken{&authToken}, nil
}

// GetApplicationAuthToken implements port.ApplicationStore.
func (s *Store) GetApplicationAuthToken(ctx context.Context, tokenID model.AuthTokenID) (model.AuthToken, error) {
	var authToken AuthToken

	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		if err := db.Preload("Application").First(&authToken, "id = ?", string(tokenID)).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.WithStack(port.ErrNotFound)
			}
			return errors.WithStack(err)
		}
		return nil
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return &wrappedApplicationAuthToken{&authToken}, nil
}

// GetApplicationAuthTokens implements port.ApplicationStore.
func (s *Store) GetApplicationAuthTokens(ctx context.Context, appID model.ApplicationID) ([]model.AuthToken, error) {
	var authTokens []*AuthToken

	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		if err := db.Where("application_id = ?", string(appID)).Find(&authTokens).Error; err != nil {
			return errors.WithStack(err)
		}
		return nil
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	wrappedTokens := make([]model.AuthToken, 0, len(authTokens))
	for _, t := range authTokens {
		wrappedTokens = append(wrappedTokens, &wrappedApplicationAuthToken{t})
	}

	return wrappedTokens, nil
}

// CreateApplicationAuthToken implements port.ApplicationStore.
func (s *Store) CreateApplicationAuthToken(ctx context.Context, token model.AuthToken) error {
	err := s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		gormToken := fromAuthTokenForApplication(token)

		if err := db.Create(gormToken).Error; err != nil {
			return errors.WithStack(err)
		}

		return nil
	})
	if err != nil {
		return errors.WithStack(err)
	}

	return nil
}

// DeleteApplicationAuthToken implements port.ApplicationStore.
func (s *Store) DeleteApplicationAuthToken(ctx context.Context, tokenID model.AuthTokenID) error {
	err := s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		result := db.Delete(&AuthToken{}, "id = ?", string(tokenID))
		if result.Error != nil {
			return errors.WithStack(result.Error)
		}

		if result.RowsAffected == 0 {
			return errors.WithStack(port.ErrNotFound)
		}

		return nil
	})
	if err != nil {
		return errors.WithStack(err)
	}

	return nil
}
