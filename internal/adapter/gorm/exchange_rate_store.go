package gorm

import (
	"context"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Store) GetRate(ctx context.Context, from, to string) (*model.ExchangeRate, error) {
	var row ExchangeRate
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		if err := db.First(&row, "from_currency = ? AND to_currency = ?", from, to).Error; err != nil {
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

	r := model.ExchangeRate{
		FromCurrency: row.FromCurrency,
		ToCurrency:   row.ToCurrency,
		Rate:         row.Rate,
		FetchedAt:    row.FetchedAt,
	}
	return &r, nil
}

func (s *Store) ListRates(ctx context.Context) ([]model.ExchangeRate, error) {
	var rows []ExchangeRate
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Order("from_currency, to_currency").Find(&rows).Error)
	})
	if err != nil {
		return nil, err
	}

	result := make([]model.ExchangeRate, len(rows))
	for i, r := range rows {
		result[i] = model.ExchangeRate{
			FromCurrency: r.FromCurrency,
			ToCurrency:   r.ToCurrency,
			Rate:         r.Rate,
			FetchedAt:    r.FetchedAt,
		}
	}
	return result, nil
}

func (s *Store) UpsertRate(ctx context.Context, rate model.ExchangeRate) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		row := ExchangeRate{
			FromCurrency: rate.FromCurrency,
			ToCurrency:   rate.ToCurrency,
			Rate:         rate.Rate,
			FetchedAt:    rate.FetchedAt,
		}
		return errors.WithStack(db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "from_currency"}, {Name: "to_currency"}},
			DoUpdates: clause.AssignmentColumns([]string{"rate", "fetched_at"}),
		}).Create(&row).Error)
	})
}

var _ port.ExchangeRateStore = &Store{}
