package setup

import (
	"context"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/adapter/cache"
	"github.com/xolo-gateway/xolo/internal/adapter/events"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

// getProvisioningServiceFromConfig shares the ordinary stores for reads. Mutations
// use database-bound stores, with cache invalidation and local events after commit.
var getProvisioningServiceFromConfig = createFromConfigOnce(func(ctx context.Context, conf *config.Config) (*service.ProvisioningService, error) {
	tenantStore, err := getTenantStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	orgStore, err := getOrgStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	userStore, err := getUserStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	roleStore, err := getRoleStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	backend, err := getGormStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	emitter, err := getEventEmitterFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	var transactions port.ProvisioningTransaction = backend
	if cached, ok := userStore.(*cache.UserStore); ok {
		transactions = cache.NewProvisioningTransaction(transactions, cached)
	}
	transactions = events.NewProvisioningTransaction(transactions, emitter)

	// The default administrators are granted the platform admin role by the
	// authentication bridge on sign-in: the API must not be able to hand one of
	// those addresses to an arbitrary user.
	return service.NewProvisioningService(tenantStore, orgStore, userStore, roleStore,
		service.WithProvisioningTransaction(transactions),
		service.WithReservedEmails(conf.HTTP.Authn.DefaultAdmins...),
		service.WithMultiTenant(conf.Multitenancy.Enabled),
	), nil
})
