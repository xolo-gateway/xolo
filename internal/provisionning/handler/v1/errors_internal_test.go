package v1

import (
	"net/http"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// TestLifecycleErrorCodes: the lifecycle refusals keep their own codes ahead
// of the ErrNotAllowed they wrap.
func TestLifecycleErrorCodes(t *testing.T) {
	for err, want := range map[error]string{
		port.ErrLifecycleDisabled: codeLifecycleDisabled,
		port.ErrResourceDeleted:   codeResourceDeleted,
		port.ErrNotAllowed:        codeConflict,
	} {
		status, code := statusFromError(errors.WithStack(err))
		require.Equal(t, http.StatusConflict, status, want)
		require.Equal(t, want, code)
	}
}
