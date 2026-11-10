package gorm

import (
	"context"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PluginUIConfig is the node config handed between the pipeline editor and a
// plugin UI (see port.PluginUIConfigStore). One row per scope and plugin,
// overwritten by each handoff.
type PluginUIConfig struct {
	// OrgID stores the scope: an organization ID or "~:<userID>".
	OrgID      string `gorm:"primaryKey;size:255"`
	PluginName string `gorm:"primaryKey;size:255"`
	ConfigJSON string `gorm:"type:text;not null"`
	UpdatedAt  time.Time
}

// SavePluginUIConfig implements port.PluginUIConfigStore.
func (s *Store) SavePluginUIConfig(ctx context.Context, scopeID, pluginName, configJSON string) error {
	if scopeID == "" || pluginName == "" {
		return errors.New("plugin ui config: scope and plugin name are required")
	}
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "org_id"}, {Name: "plugin_name"}},
			DoUpdates: clause.AssignmentColumns([]string{"config_json", "updated_at"}),
		}).Create(&PluginUIConfig{OrgID: scopeID, PluginName: pluginName, ConfigJSON: configJSON}).Error)
	})
}

// GetPluginUIConfig implements port.PluginUIConfigStore.
func (s *Store) GetPluginUIConfig(ctx context.Context, scopeID, pluginName string) (string, error) {
	var rows []PluginUIConfig
	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Where("org_id = ? AND plugin_name = ?", scopeID, pluginName).Limit(1).Find(&rows).Error)
	})
	if err != nil || len(rows) == 0 {
		return "", err
	}
	return rows[0].ConfigJSON, nil
}

var _ port.PluginUIConfigStore = &Store{}
