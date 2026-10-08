package setup

import (
	"context"
	"log/slog"

	"github.com/pkg/errors"
	gormAdapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/config"
)

var getGormStoreFromConfig = createFromConfigOnce(func(ctx context.Context, conf *config.Config) (*gormAdapter.Store, error) {
	db, err := getGormDatabaseFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	store := gormAdapter.NewStore(db,
		gormAdapter.WithAutoMigrate(conf.Storage.AutoMigrate),
		gormAdapter.WithIdentityIssuers(getIdentityIssuersFromConfig(ctx, conf)),
		gormAdapter.WithOwnership(conf.Ownership),
	)
	// Every replica must run the same policy: logged so that a divergence
	// shows up when comparing their startups.
	slog.InfoContext(ctx, "write authority policy", slog.String("ownership", store.OwnershipPolicy().String()))
	if conf.Storage.AutoMigrate {
		err = store.Migrate(ctx)
	} else {
		err = store.CheckSchema(ctx)
	}
	if err != nil {
		return nil, errors.WithStack(err)
	}

	// Routing data rather than schema: it also runs when the schema is
	// migrated by hand. The legacy host pattern only matters to a multi-tenant
	// instance upgrading from host-pattern routing.
	if conf.Multitenancy.Enabled {
		if err := store.InitializeDomainRouting(ctx, conf.Multitenancy.HostPattern); err != nil {
			return nil, errors.WithStack(err)
		}
	}

	return store, nil
})
