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

	store := gormAdapter.NewStore(db)
	if err := store.Migrate(ctx); err != nil {
		return nil, errors.WithStack(err)
	}

	if u, err := url.Parse(conf.HTTP.BaseURL); err == nil && u.Hostname() != "" && net.ParseIP(u.Hostname()) == nil {
		if err := store.ReserveDomain(ctx, u.Hostname()); err != nil {
			return nil, errors.Wrap(err, "reserve shared application hostname")
		}
	}
	pattern := ""
	if conf.Multitenancy.Enabled {
		pattern = conf.Multitenancy.HostPattern
	}
	if err := store.InitializeDomainRouting(ctx, pattern, conf.Multitenancy.DefaultTenantSlug); err != nil {
		return nil, errors.Wrap(err, "initialize explicit tenant domains")
	}
	if err := store.ConfigureOwnership(conf.Ownership); err != nil {
		return nil, err
	}
	store.ConfigureLifecycle(conf.Lifecycle.Enabled, conf.Lifecycle.Retention)
	store.ConfigureBusiness(conf.BusinessResources, conf.SecretKey)
	return store, nil
})
