package setup

import (
	"context"
	"net"
	"net/url"

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

	if u, err := url.Parse(conf.HTTP.BaseURL); err == nil && u.Hostname() != "" && net.ParseIP(u.Hostname()) == nil {
		if err := store.ReserveDomain(ctx, u.Hostname()); err != nil {
			return nil, errors.Wrap(err, "reserve shared application hostname")
		}
	}
	return store, nil
})
