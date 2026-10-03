package common

import (
	"log/slog"
	"net/http"

	"github.com/a-h/templ"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
)

type HTTPError interface {
	error
	StatusCode() int
}

type UserFacingError interface {
	error
	UserMessage() string
}

type WithErrorLinks interface {
	error
	Links() []component.LinkItem
}

func HandleError(w http.ResponseWriter, r *http.Request, err error) {
	vmodel := component.ErrorPageVModel{}

	statusCode := http.StatusInternalServerError
	if errors.Is(err, port.ErrResourceDeleted) {
		statusCode = http.StatusGone
	}
	if errors.Is(err, port.ErrLifecycleDisabled) {
		statusCode = http.StatusConflict
	}
	if errors.Is(err, port.ErrNotAllowed) {
		statusCode = http.StatusForbidden
	}

	var httpErr HTTPError
	if errors.As(err, &httpErr) {
		statusCode = httpErr.StatusCode()
	}

	w.WriteHeader(statusCode)

	var userFacingErr UserFacingError
	if errors.As(err, &userFacingErr) {
		vmodel.Message = userFacingErr.UserMessage()
	} else {
		vmodel.Message = http.StatusText(statusCode)
	}

	var errLinks WithErrorLinks
	if errors.As(err, &errLinks) {
		vmodel.Links = errLinks.Links()
	}

	if httpErr == nil && userFacingErr == nil {
		slog.ErrorContext(r.Context(), "unexpected error", slog.Any("error", errors.WithStack(err)))
	}

	errorPage := component.ErrorPage(vmodel)

	templ.Handler(errorPage).ServeHTTP(w, r)
}
