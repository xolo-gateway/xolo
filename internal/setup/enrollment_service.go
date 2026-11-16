package setup

import (
	"context"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

// getEnrollmentServiceFromConfig returns nil when rules can not apply: a rule
// writes members and memberships locally, which the ownership policy may
// reserve to the control plane. Fixed at startup, so the bridge and the web
// UI agree.
var getEnrollmentServiceFromConfig = createFromConfigOnce(func(ctx context.Context, conf *config.Config) (*service.EnrollmentService, error) {
	ownership := conf.Ownership.Effective()
	if !ownership.Allows(model.FamilyMember, model.OwnerLocal) || !ownership.Allows(model.FamilyOrganizationMembership, model.OwnerLocal) {
		return nil, nil
	}
	store, err := getGormStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	transactions, err := getProvisioningTransactionFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return service.NewEnrollmentService(store, transactions), nil
})
