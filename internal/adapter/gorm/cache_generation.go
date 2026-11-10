package gorm

import (
	"context"
	"time"

	"github.com/pkg/errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CacheGeneration counts the invalidations of one in-process cache, shared by
// every instance on the database so each one knows when to flush its own copy
// (see cache.Coherence). Holds no tenant, organization or user link.
type CacheGeneration struct {
	Name       string `gorm:"primaryKey;size:64"`
	Generation int64  `gorm:"not null;default:0"`
	UpdatedAt  time.Time
}

// CacheGenerationStore reads and bumps the cache generations.
type CacheGenerationStore struct {
	db *gorm.DB
}

func NewCacheGenerationStore(db *gorm.DB) *CacheGenerationStore {
	return &CacheGenerationStore{db: db}
}

// Bump increments the generation of the named cache, creating it on first use.
// A single upsert: concurrent bumps from several instances each count.
func (s *CacheGenerationStore) Bump(ctx context.Context, name string) error {
	now := time.Now()
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "name"}},
		DoUpdates: clause.Assignments(map[string]any{
			"generation": gorm.Expr("cache_generations.generation + 1"),
			"updated_at": now,
		}),
	}).Create(&CacheGeneration{Name: name, Generation: 1, UpdatedAt: now}).Error
	return errors.WithStack(err)
}

// Generations returns the generation of every cache bumped at least once.
func (s *CacheGenerationStore) Generations(ctx context.Context) (map[string]int64, error) {
	var rows []CacheGeneration
	if err := s.db.WithContext(ctx).Select("name", "generation").Find(&rows).Error; err != nil {
		return nil, errors.WithStack(err)
	}

	generations := make(map[string]int64, len(rows))
	for _, row := range rows {
		generations[row.Name] = row.Generation
	}
	return generations, nil
}
