package gorm

import (
	"context"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
)

// Domain routes requests addressed to Hostname to its tenant. The hostname is
// the primary key: a hostname belongs to at most one tenant instance-wide.
type Domain struct {
	Hostname  string `gorm:"primaryKey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	TenantID  string `gorm:"index;not null"`
	Status    string `gorm:"not null"`
}

// DomainRouting records that the legacy host pattern has been expanded into
// domains, so the expansion runs once and never overwrites later changes.
type DomainRouting struct {
	ID        uint `gorm:"primaryKey;autoIncrement:false"`
	CreatedAt time.Time
	Pattern   string `gorm:"not null"`
}

const domainRoutingID = 1

func toDomain(d *Domain) model.Domain {
	return model.Domain{Hostname: d.Hostname, TenantID: model.TenantID(d.TenantID), Status: model.Status(d.Status)}
}

// GetDomain implements port.DomainStore.
func (s *Store) GetDomain(ctx context.Context, hostname string) (model.Domain, error) {
	var d Domain
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		if err := db.First(&d, "hostname = ?", hostname).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.WithStack(port.ErrNotFound)
			}
			return errors.WithStack(err)
		}
		return nil
	})
	if err != nil {
		return model.Domain{}, err
	}
	return toDomain(&d), nil
}

// ListTenantDomains implements port.DomainStore.
func (s *Store) ListTenantDomains(ctx context.Context, tenantID model.TenantID) ([]model.Domain, error) {
	var rows []*Domain
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Where("tenant_id = ?", string(tenantID)).Order("hostname").Find(&rows).Error)
	})
	if err != nil {
		return nil, err
	}
	domains := make([]model.Domain, 0, len(rows))
	for _, d := range rows {
		domains = append(domains, toDomain(d))
	}
	return domains, nil
}

// SaveDomain implements port.DomainStore. It creates the domain or updates its
// status; a hostname held by another tenant is a conflict.
func (s *Store) SaveDomain(ctx context.Context, domain model.Domain) error {
	host, err := model.NormalizeHostname(domain.Hostname)
	if err != nil || host != domain.Hostname {
		return errors.WithStack(port.ErrInvalidHostname)
	}
	if !domain.Status.Valid() {
		return errors.WithStack(port.ErrInvalid)
	}
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		var existing Domain
		err := db.First(&existing, "hostname = ?", host).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			return errors.WithStack(db.Create(&Domain{Hostname: host, TenantID: string(domain.TenantID), Status: string(domain.Status)}).Error)
		case err != nil:
			return errors.WithStack(err)
		case existing.TenantID != string(domain.TenantID):
			return errors.Wrapf(port.ErrAlreadyExists, "hostname %q belongs to another tenant", host)
		}
		return errors.WithStack(db.Model(&existing).Update("status", string(domain.Status)).Error)
	})
}

var _ port.DomainStore = &Store{}
