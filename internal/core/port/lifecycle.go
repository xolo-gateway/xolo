package port

import (
	"context"
	"errors"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

var (
	ErrResourceDeleted      = errors.New("resource deleted")
	ErrConfirmationRequired = errors.New("explicit deleted version required")
	ErrExportMismatch       = errors.New("complete scope export required")
	ErrPurgeNotReady        = errors.New("purge not eligible")
	ErrLifecycleDisabled    = errors.New("lifecycle extension disabled")
)

type LifecycleStore interface {
	ScheduleDeletion(context.Context, model.CommonScope, string, model.MatchCondition, time.Duration) (model.Deletion, error)
	ReadDeletion(context.Context, model.CommonScope, string) (model.Deletion, error)
	ExportDeletion(context.Context, model.CommonScope, string) ([]byte, error)
	ConfirmDeletion(context.Context, model.CommonScope, string, model.MatchCondition, string) (model.Deletion, error)
	DeleteCommonLeaf(context.Context, model.CommonScope, string, model.MatchCondition) error
	PurgeDeletion(context.Context, model.CommonScope, string) error
	DueDeletions(context.Context, int) ([]model.Deletion, error)
}
