package common

import (
	"errors"
	"net/http"

	"github.com/xolo-gateway/xolo/internal/core/port"
)

// RejectLocked answers 403 when err is a write refused because the resource
// is locked — reserved to the control plane by the ownership policy, or
// frozen by its deletion — and reports whether it did. Full pages and htmx
// fragments go through the same error page.
func RejectLocked(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case errors.Is(err, port.ErrOwnershipDenied):
		HandleError(w, r, NewError(err.Error(), "Cette ressource est gérée par le système de pilotage : elle ne peut pas être modifiée depuis cette instance.", http.StatusForbidden))
	case errors.Is(err, port.ErrResourceDeleted):
		HandleError(w, r, NewError(err.Error(), "Cette ressource est en cours de suppression : elle ne peut plus être modifiée.", http.StatusForbidden))
	default:
		return false
	}
	return true
}
