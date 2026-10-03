package v1

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// Machine-readable error codes. They are part of the API contract: a client
// branches on the code, not on the message.
const (
	codeInvalidRequest = "invalid_request"

	codeNotFound      = "not_found"
	codeConflict      = "conflict"
	codeInternalError = "internal_error"
)

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if payload == nil {
		return
	}

	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("could not encode provisionning api response", slogx.Error(errors.WithStack(err)))
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorEnvelope{Error: errorBody{Code: code, Message: message}})
}

// writeServiceError maps a domain error to its HTTP status and returns a
// message safe to expose to a client.
//
// Caller-supplied fixed messages are returned for expected domain failures.
// Everything else is a generic internal error and logged server-side:
// stack traces, SQL errors, file paths, TLS details and secrets must never
// reach the client.
func writeServiceError(ctx context.Context, w http.ResponseWriter, err error, fallback string) {
	status, code := statusFromError(err)

	if status == http.StatusInternalServerError {
		slog.ErrorContext(ctx, "provisionning api request failed", slogx.Error(err), slog.String("requestID", model.ActorFromContext(ctx).RequestID))
		writeError(w, status, code, "an unexpected error occurred")
		return
	}

	slog.DebugContext(ctx, "provisionning api request refused", slog.Int("status", status), slogx.Error(err), slog.String("requestID", model.ActorFromContext(ctx).RequestID))

	if code == "conflict" {
		fallback = "resource conflict"
	}
	writeError(w, status, code, fallback)
}

// statusFromError maps the domain sentinels to their HTTP status.
func statusFromError(err error) (int, string) {
	switch {
	case errors.Is(err, port.ErrResourceDeleted):
		return 410, "resource_deleted"
	case errors.Is(err, port.ErrConfirmationRequired):
		return 428, "precondition_required"
	case errors.Is(err, port.ErrExportMismatch):
		return 409, "export_mismatch"
	case errors.Is(err, port.ErrPurgeNotReady):
		return 409, "purge_not_ready"
	case errors.Is(err, port.ErrLifecycleDisabled):
		return 409, "lifecycle_disabled"
	case errors.Is(err, port.ErrOwnershipDenied):
		return 403, "ownership_denied"
	case errors.Is(err, port.ErrWebhookCapacity):
		return 409, "webhook_capacity"
	case errors.Is(err, port.ErrInvalidCursor):
		return 400, "invalid_cursor"
	case errors.Is(err, port.ErrCursorExpired):
		return 410, "cursor_expired"
	case errors.Is(err, port.ErrInvalidPrecondition):
		return 400, "invalid_precondition"
	case errors.Is(err, port.ErrPreconditionFailed):
		return 412, "precondition_failed"
	case errors.Is(err, port.ErrLastOwner):
		return 409, "last_owner"
	case errors.Is(err, port.ErrInvalidHostname):
		return 400, "invalid_hostname"
	case errors.Is(err, port.ErrParentNotFound):
		return 404, "parent_not_found"
	case errors.Is(err, port.ErrNotFound):
		return http.StatusNotFound, codeNotFound
	case errors.Is(err, port.ErrAlreadyExists):
		return http.StatusConflict, codeConflict
	case errors.Is(err, port.ErrNotAllowed):
		return http.StatusConflict, codeConflict
	case errors.Is(err, port.ErrInvalid):
		return http.StatusBadRequest, "invalid_representation"
	default:
		return http.StatusInternalServerError, codeInternalError
	}
}
