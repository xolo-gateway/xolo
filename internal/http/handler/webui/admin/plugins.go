package admin

import (
	"net/http"
	"slices"

	"github.com/a-h/templ"
	"github.com/pkg/errors"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/admin/component"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/common"
	commonComp "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authz"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

func (h *Handler) getPluginsDiagnosticsPage(w http.ResponseWriter, r *http.Request) {
	vmodel, err := h.fillPluginsDiagnosticsPageViewModel(r)
	if err != nil {
		common.HandleError(w, r, errors.WithStack(err))
		return
	}
	templ.Handler(component.PluginsDiagnosticsPage(*vmodel)).ServeHTTP(w, r)
}

func (h *Handler) fillPluginsDiagnosticsPageViewModel(r *http.Request) (*component.PluginsDiagnosticsPageVModel, error) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	if user == nil {
		return nil, errors.New("could not retrieve user from context")
	}

	isAdmin := slices.Contains(user.Roles(), authz.RoleAdmin)

	vmodel := &component.PluginsDiagnosticsPageVModel{
		AppLayoutVModel: commonComp.AppLayoutVModel{
			User:         user,
			IsAdmin:      isAdmin,
			SelectedItem: "plugins",
			Breadcrumbs: []commonComp.BreadcrumbItem{
				{Label: i18n.T(ctx, "admin.breadcrumb.platform"), Href: "/admin/"},
				{Label: i18n.T(ctx, "admin.breadcrumb.plugins"), Href: ""},
			},
			Context: commonComp.ContextPlatform,
		},
		Descriptors: h.pluginManager.List(),
	}

	return vmodel, nil
}
