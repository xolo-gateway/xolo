package port

import (
	"context"
	"errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

var (
	ErrInvalidCursor       = errors.New("invalid cursor")
	ErrCursorExpired       = errors.New("cursor expired")
	ErrPreconditionFailed  = errors.New("precondition failed")
	ErrInvalidPrecondition = errors.New("invalid precondition")
)

// CommonStore returns each representation and validator from one committed row.
// The write callback is transaction bound and must have no external effects.
type CommonStore interface {
	ReadCommon(context.Context, model.CommonScope, string) (model.CommonItem, error)
	ListCommon(context.Context, model.CommonScope, string, int) (model.CommonPage, error)
	WriteCommon(context.Context, model.CommonScope, string, model.MatchCondition, func(ProvisioningTx) error) (model.CommonItem, error)
	CaptureCommonCursor(context.Context) (string, error)
	ReadCommonEvents(context.Context, string, int) (model.CommonEventPage, error)
}
