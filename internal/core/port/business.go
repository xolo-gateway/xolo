package port

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

type BusinessStore interface {
	PutBusiness(context.Context, model.CommonScope, string, model.MatchCondition, model.BusinessSettings) (model.CommonItem, error)
}
