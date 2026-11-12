package join

import (
	"net/http"
	"strings"

	"github.com/a-h/templ"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/service"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/common"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/join/component"
)

type Handler struct {
	mux         *http.ServeMux
	invitations *service.InvitationService
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func NewHandler(invitations *service.InvitationService) *Handler {
	h := &Handler{mux: http.NewServeMux(), invitations: invitations}
	h.mux.HandleFunc("GET /{tokenID}", h.getJoinPage)
	h.mux.HandleFunc("POST /{tokenID}", h.acceptInvite)
	return h
}

func (h *Handler) getJoinPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	var userID model.UserID
	if user != nil {
		userID = user.ID()
	}
	tokenID := r.PathValue("tokenID")
	view, err := h.invitations.Prepare(ctx, httpCtx.TenantID(ctx), model.InviteTokenID(tokenID), userID)
	if err != nil {
		common.WriteInvitationError(w, r, err)
		return
	}
	baseURL := httpCtx.BaseURL(ctx)
	loginURL := baseURL.JoinPath("/auth/oidc/login").String()
	if view.LoginRequired {
		templ.Handler(component.JoinLogin(loginURL)).ServeHTTP(w, r)
		return
	}
	templ.Handler(component.JoinPage(component.JoinPageVModel{
		User: user, Org: view.Org, RoleName: view.Role.Name(), LoginURL: loginURL,
		AcceptURL:  baseURL.JoinPath("/join/" + tokenID).String(),
		DeclineURL: baseURL.JoinPath("/no-org/invitations/" + tokenID + "/decline").String(),
	})).ServeHTTP(w, r)
}

func (h *Handler) acceptInvite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	if user == nil {
		http.Redirect(w, r, "/auth/oidc/login", http.StatusSeeOther)
		return
	}
	result, err := h.invitations.Accept(ctx, httpCtx.TenantID(ctx), model.InviteTokenID(r.PathValue("tokenID")), user.ID())
	if err != nil {
		common.WriteInvitationError(w, r, err)
		return
	}
	names := make([]string, 0, len(result.Roles))
	destination := "/usage"
	for _, role := range result.Roles {
		names = append(names, role.Name())
		if role.Builtin() && (role.BuiltinKind() == model.BuiltinKindAdmin || role.BuiltinKind() == model.BuiltinKindOwner) {
			destination = "/orgs/" + result.Org.Slug() + "/usage"
		}
	}
	templ.Handler(component.JoinSuccess(component.JoinSuccessVModel{
		User: user, OrgName: result.Org.Name(), OrgSlug: result.Org.Slug(),
		RoleName: strings.Join(names, ", "), AlreadyMember: result.AlreadyMember, Destination: destination,
	})).ServeHTTP(w, r)
}

var _ http.Handler = (*Handler)(nil)
