package setup

import (
	"context"

	"github.com/pkg/errors"
	gormAdapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/config"
)

var getGormStoreFromConfig = createFromConfigOnce(func(ctx context.Context, conf *config.Config) (*gormAdapter.Store, error) {
	db, err := getGormDatabaseFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	store := gormAdapter.NewStore(db, gormAdapter.WithAutoMigrate(conf.Storage.AutoMigrate))
	if conf.Storage.AutoMigrate {
		err = store.Migrate(ctx)
	} else {
		err = store.CheckSchema(ctx)
	}
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return store, nil
})
