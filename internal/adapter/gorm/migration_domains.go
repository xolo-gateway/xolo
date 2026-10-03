package gorm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type domainRoutingMigration struct {
	ID       int `gorm:"primaryKey;autoIncrement:false"`
	TenantID string
}

// InitializeDomainRouting materializes the previously configured tenant hosts
// once. Subsequent requests and restarts never synthesize domains from slugs.
// Existing explicit records, including suspended ones, are never overwritten.
func (s *Store) InitializeDomainRouting(ctx context.Context, legacyPattern string, defaultSlug ...string) error {
	return s.identityTransaction(ctx, func(bound *Store) error {
		db, err := bound.getDatabase(ctx)
		if err != nil {
			return err
		}
		if err = db.AutoMigrate(&domainRoutingMigration{}); err != nil {
			return err
		}
		var count int64
		if err = db.Model(&domainRoutingMigration{}).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
		if legacyPattern != "" {
			pattern := legacyPattern
			if host, _, e := net.SplitHostPort(pattern); e == nil {
				pattern = host
			}
			var tenants []Tenant
			if err = db.Find(&tenants).Error; err != nil {
				return err
			}
			for _, tenant := range tenants {
				host, e := model.NormalizeHostname(strings.ReplaceAll(pattern, "{tenant}", tenant.Slug))
				if e != nil {
					return fmt.Errorf("invalid legacy tenant hostname")
				}
				var reserved int64
				if e = db.Model(&ReservedDomain{}).Where("hostname = ?", host).Count(&reserved).Error; e != nil {
					return e
				}
				if reserved > 0 {
					return fmt.Errorf("legacy tenant hostname is reserved")
				}
				row := Domain{Hostname: host, TenantID: tenant.ID, Status: string(model.StatusActive)}
				if e = db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; e != nil {
					return e
				}
				var stored Domain
				if e = db.First(&stored, "hostname = ?", host).Error; e != nil {
					return e
				}
				// Materialized historical domains also participate in common lists.
				raw, e := mutationSnapshot(db, mutationKey{"domain", host})
				if e != nil {
					return e
				}
				rec, e := commonProjection(db, mutationKey{"domain", host}, raw)
				if e != nil {
					return e
				}
				rec.UpdatedAt = db.NowFunc().UTC()
				if e = db.Clauses(clause.OnConflict{DoNothing: true}).Create(rec).Error; e != nil {
					return e
				}
				if stored.TenantID != tenant.ID {
					return fmt.Errorf("legacy tenant hostname conflicts with explicit domain")
				}
			}
		}
		sharedID := ""
		if len(defaultSlug) > 0 {
			var shared Tenant
			if err := db.First(&shared, "slug = ?", defaultSlug[0]).Error; err != nil {
				return err
			}
			sharedID = shared.ID
		}
		return db.Create(&domainRoutingMigration{ID: 1, TenantID: sharedID}).Error
	})
}

// GetSharedTenant keeps the configured shared host attached across slug changes.
func (s *Store) GetSharedTenant(ctx context.Context, defaultSlug string) (model.Tenant, error) {
	var route domainRoutingMigration
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		if !db.Migrator().HasTable(&domainRoutingMigration{}) {
			return port.ErrNotFound
		}
		return db.First(&route, 1).Error
	})
	if errors.Is(err, port.ErrNotFound) || errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && route.TenantID == "") {
		return s.GetTenantBySlug(ctx, defaultSlug)
	}
	if err != nil {
		return nil, err
	}
	return s.GetTenantByID(ctx, model.TenantID(route.TenantID))
}
