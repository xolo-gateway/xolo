package setup

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/service"
	"github.com/pkg/errors"
)

// getProvisioningServiceFromConfig builds the provisioning service on top of
// the very same store instances the public HTTP server uses — cache and event
// decorators included. No second database connection, no second repository
// implementation.
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

	// The default administrators are granted the platform admin role by the
	// authentication bridge on sign-in: the API must not be able to hand one of
	// those addresses to an arbitrary user.
	return service.NewProvisioningService(tenantStore, orgStore, userStore, roleStore,
		service.WithReservedEmails(conf.HTTP.Authn.DefaultAdmins...),
		service.WithMultiTenant(conf.Multitenancy.Enabled),
	), nil
})
