package gorm

import (
	"context"
	"log/slog"
	"strings"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// InitializeDomainRouting turns the legacy host pattern into one active domain
// per existing tenant, so an upgraded multi-tenant instance keeps serving
// every tenant on its former hostname. It runs once: later slug renames,
// tenants and domain changes are never overwritten. An empty pattern does
// nothing and records nothing, so the expansion still happens the first time
// a pattern is configured.
//
// A hostname already routed to a tenant is kept as is and logged: the
// expansion never blocks the startup.
func (s *Store) InitializeDomainRouting(ctx context.Context, pattern string) error {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil
	}
	ctx = model.EnsureActor(ctx)
	return s.withRetry(ctx, true, func(ctx context.Context, tx *gorm.DB) error {
		recorder := newMutationRecorder(tx)
		var done int64
		if err := tx.Model(&DomainRouting{}).Where("id = ?", domainRoutingID).Count(&done).Error; err != nil {
			return errors.WithStack(err)
		}
		if done > 0 {
			return nil
		}
		var tenants []Tenant
		if err := tx.Order("slug").Find(&tenants).Error; err != nil {
			return errors.WithStack(err)
		}
		for _, tenant := range tenants {
			host, err := model.ExpandHostPattern(pattern, tenant.Slug)
			if err != nil {
				return errors.Wrapf(err, "XOLO_MULTITENANCY_HOST_PATTERN %q gives tenant %q an invalid hostname", pattern, tenant.Slug)
			}
			if err := recorder.track("domain", host); err != nil {
				return err
			}
			result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&Domain{Hostname: host, TenantID: tenant.ID, Status: string(model.StatusActive)})
			if result.Error != nil {
				return errors.WithStack(result.Error)
			}
			if result.RowsAffected == 0 {
				slog.WarnContext(ctx, "legacy tenant hostname already routed, kept unchanged; declare another domain through the provisioning API if needed",
					slog.String("hostname", host), slog.String("tenant", tenant.Slug))
			}
		}
		// Concurrent startups may both expand the pattern: the domains and
		// this marker are inserted idempotently.
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&DomainRouting{ID: domainRoutingID, Pattern: pattern}).Error; err != nil {
			return errors.WithStack(err)
		}
		return recorder.flush(ctx)
	})
}
