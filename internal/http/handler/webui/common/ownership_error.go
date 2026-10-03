package common

import (
	"errors"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"net/http"
)

// RejectOwnership keeps full documents and fragment writers on the same 403 path.
func RejectOwnership(w http.ResponseWriter, r *http.Request, err error) bool {
	if !errors.Is(err, port.ErrOwnershipDenied) {
		return false
	}
	HandleError(w, r, NewError("resource owned by control plane", "Cette ressource est gérée par la console de provisioning.", http.StatusForbidden))
	return true
}
