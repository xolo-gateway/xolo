package common

import (
	"errors"
	"net/http"

	"github.com/xolo-gateway/xolo/internal/core/port"
)

// RejectOwnership answers 403 when err is a write the ownership policy
// reserves to the control plane, and reports whether it did. Full pages and
// htmx fragments go through the same error page.
func RejectOwnership(w http.ResponseWriter, r *http.Request, err error) bool {
	if !errors.Is(err, port.ErrOwnershipDenied) {
		return false
	}
	HandleError(w, r, NewError(err.Error(), "Cette ressource est gérée par le système de pilotage : elle ne peut pas être modifiée depuis cette instance.", http.StatusForbidden))
	return true
}
