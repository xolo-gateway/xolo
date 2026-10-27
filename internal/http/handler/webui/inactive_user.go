package webui

import (
	"net/http"

	"github.com/xolo-gateway/xolo/internal/http/handler/webui/common"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

func (h *Handler) getInactiveUserPage(w http.ResponseWriter, r *http.Request) {
	common.HandleError(w, r, common.NewError("forbidden", i18n.T(r.Context(), "error.inactive_account"), http.StatusForbidden))
}

func (h *Handler) getForbiddenPage(w http.ResponseWriter, r *http.Request) {
	common.HandleError(w, r, common.NewError("forbidden", i18n.T(r.Context(), "error.forbidden_page"), http.StatusForbidden))
}
