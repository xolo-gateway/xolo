package setup

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/service"
	"github.com/pkg/errors"
)

var getQuotaServiceFromConfig = createFromConfigOnce(func(ctx context.Context, conf *config.Config) (*service.QuotaService, error) {
	quotaStore, err := getQuotaStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	orgStore, err := getOrgStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	teamStore, err := getTeamStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return service.NewQuotaService(quotaStore, orgStore, service.WithQuotaTeams(teamStore)), nil
})
