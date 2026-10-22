package gorm

import (
	"context"

	"github.com/pkg/errors"
	"github.com/rs/xid"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GetSecret implements port.SecretStore.
func (s *Store) GetSecret(ctx context.Context, scopeID, pluginName, nodeID, key string) (string, bool, error) {
	if err := port.ValidateSecretKey(scopeID, pluginName, nodeID, key); err != nil {
		return "", false, err
	}
	var secret PluginNodeSecret
	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		if err := db.Where("org_id = ? AND plugin_name = ? AND node_id = ? AND key = ?", scopeID, pluginName, nodeID, key).First(&secret).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return errors.WithStack(err)
		}
		return nil
	})
	if err != nil {
		return "", false, err
	}
	if secret.ID == "" {
		return "", false, nil
	}
	return secret.ValueEncrypted, true, nil
}

// SetSecret implements port.SecretStore.
func (s *Store) SetSecret(ctx context.Context, scopeID, pluginName, nodeID, key, value string) error {
	if err := port.ValidateSecretKey(scopeID, pluginName, nodeID, key); err != nil {
		return err
	}
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		secret := &PluginNodeSecret{
			ID:             xid.New().String(),
			OrgID:          scopeID,
			PluginName:     pluginName,
			NodeID:         nodeID,
			Key:            key,
			ValueEncrypted: value,
		}
		return errors.WithStack(db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "org_id"}, {Name: "plugin_name"}, {Name: "node_id"}, {Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"value_encrypted", "updated_at"}),
		}).Create(secret).Error)
	})
}

// DeleteSecret implements port.SecretStore.
func (s *Store) DeleteSecret(ctx context.Context, scopeID, pluginName, nodeID, key string) error {
	if err := port.ValidateSecretKey(scopeID, pluginName, nodeID, key); err != nil {
		return err
	}
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Where("org_id = ? AND plugin_name = ? AND node_id = ? AND key = ?", scopeID, pluginName, nodeID, key).Delete(&PluginNodeSecret{}).Error)
	})
}

// DeleteAllForNode implements port.SecretStore.
func (s *Store) DeleteAllForNode(ctx context.Context, scopeID, pluginName, nodeID string) error {
	if err := port.ValidateSecretScope(scopeID, pluginName, nodeID); err != nil {
		return err
	}
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Where("org_id = ? AND plugin_name = ? AND node_id = ?", scopeID, pluginName, nodeID).Delete(&PluginNodeSecret{}).Error)
	})
}

// ListSecretKeys returns the keys of the secrets set on a node, sorted,
// never their values.
func (s *Store) ListSecretKeys(ctx context.Context, scopeID, pluginName, nodeID string) ([]string, error) {
	if err := port.ValidateSecretScope(scopeID, pluginName, nodeID); err != nil {
		return nil, err
	}
	keys := []string{}
	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Model(&PluginNodeSecret{}).Where("org_id = ? AND plugin_name = ? AND node_id = ?", scopeID, pluginName, nodeID).
			Order("key").Pluck("key", &keys).Error)
	})
	if err != nil {
		return nil, err
	}
	return keys, nil
}

var _ port.SecretStore = &Store{}
