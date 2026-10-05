package port

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

type DomainStore interface {
	SaveDomain(context.Context, model.Domain) error
	GetDomain(context.Context, string) (model.Domain, error)
	ListDomains(context.Context, model.TenantID) ([]model.Domain, error)
	SetCommonMembership(context.Context, model.OrgID, model.UserID, model.MembershipRole, model.Status) error
}
