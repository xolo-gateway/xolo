package gorm

import (
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

type Tenant struct {
	ID          string `gorm:"primaryKey;autoIncrement:false"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Slug        string `gorm:"uniqueIndex;not null"`
	Name        string `gorm:"not null"`
	Description string
	// Active is an int for the same reason as Organization.Active: GORM omits a
	// zero-valued field carrying a DB default from the INSERT, which would make
	// it impossible to ever persist a deactivation.
	Active int
}

type wrappedTenant struct {
	t *Tenant
}

func (w *wrappedTenant) ID() model.TenantID   { return model.TenantID(w.t.ID) }
func (w *wrappedTenant) Slug() string         { return w.t.Slug }
func (w *wrappedTenant) Name() string         { return w.t.Name }
func (w *wrappedTenant) Description() string  { return w.t.Description }
func (w *wrappedTenant) Active() bool         { return w.t.Active != 0 }
func (w *wrappedTenant) CreatedAt() time.Time { return w.t.CreatedAt }
func (w *wrappedTenant) UpdatedAt() time.Time { return w.t.UpdatedAt }

var _ model.Tenant = &wrappedTenant{}

func fromTenant(tenant model.Tenant) *Tenant {
	return &Tenant{
		ID:          string(tenant.ID()),
		Slug:        tenant.Slug(),
		Name:        tenant.Name(),
		Description: tenant.Description(),
		Active:      boolToInt(tenant.Active()),
	}
}
