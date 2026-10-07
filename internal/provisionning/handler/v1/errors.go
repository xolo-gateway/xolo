package v1

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// Machine-readable error codes. They are part of the API contract: a client
// branches on the code, not on the message.
const (
	codeInvalidRequest   = "invalid_request"
	codeInvalidParameter = "invalid_parameter"
	codeMethodNotAllowed = "method_not_allowed"

	// Common contract body errors.
	codeUnsupportedMediaType  = "unsupported_media_type"
	codeInvalidJSON           = "invalid_json"
	codeInvalidRepresentation = "invalid_representation"

	codeNotFound               = "not_found"
	codeParentNotFound         = "parent_not_found"
	codeConflict               = "conflict"
	codeLastOwner              = "last_owner"
	codePlatformAdminProtected = "platform_admin_protected"
	codeInvalidHostname        = "invalid_hostname"
	codeUnprocessable          = "unprocessable"
	codeInternalError          = "internal_error"
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
// Only messages explicitly built on top of a domain sentinel are echoed back.
// Anything else is reported as a generic internal error and logged server-side:
// stack traces, SQL errors, file paths, TLS details and secrets must never
// reach the client.
func writeServiceError(ctx context.Context, w http.ResponseWriter, err error, fallback string) {
	status, code := statusFromError(err)

	if status == http.StatusInternalServerError {
		slog.ErrorContext(ctx, "provisionning api request failed", slogx.Error(err))
		writeError(w, status, code, "an unexpected error occurred")
		return
	}

	slog.DebugContext(ctx, "provisionning api request refused", slog.Int("status", status), slogx.Error(err))

	message := fallback
	if detail := sentinelMessage(err); detail != "" {
		message = detail
	}

	writeError(w, status, code, message)
}

// statusFromError maps the domain sentinels to their HTTP status.
func statusFromError(err error) (int, string) {
	switch {
	case errors.Is(err, port.ErrParentNotFound):
		return http.StatusNotFound, codeParentNotFound
	case errors.Is(err, port.ErrLastOwner):
		return http.StatusConflict, codeLastOwner
	case errors.Is(err, port.ErrPlatformAdminProtected):
		return http.StatusConflict, codePlatformAdminProtected
	case errors.Is(err, port.ErrInvalidHostname):
		return http.StatusBadRequest, codeInvalidHostname
	case errors.Is(err, port.ErrNotFound):
		return http.StatusNotFound, codeNotFound
	case errors.Is(err, port.ErrAlreadyExists):
		return http.StatusConflict, codeConflict
	case errors.Is(err, port.ErrNotAllowed):
		return http.StatusConflict, codeConflict
	case errors.Is(err, port.ErrInvalid):
		return http.StatusUnprocessableEntity, codeUnprocessable
	default:
		return http.StatusInternalServerError, codeInternalError
	}
}

// sentinelMessage extracts the explanation a caller wrapped around a domain
// sentinel. It returns an empty string when the error carries nothing but the
// bare sentinel, so the caller falls back to its own wording.
func sentinelMessage(err error) string {
	for _, sentinel := range []error{port.ErrParentNotFound, port.ErrNotFound, port.ErrAlreadyExists, port.ErrNotAllowed, port.ErrInvalid} {
		if !errors.Is(err, sentinel) {
			continue
		}

		suffix := ": " + sentinel.Error()
		if message := err.Error(); strings.HasSuffix(message, suffix) {
			return strings.TrimSuffix(message, suffix)
		}

		return ""
	}

	return ""
}
