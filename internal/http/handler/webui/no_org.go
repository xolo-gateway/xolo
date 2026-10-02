package webui

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/slogx"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/profile/component"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authz"
)

func (h *Handler) getNoOrgPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	memberships := httpCtx.Memberships(ctx)

	baseURL := httpCtx.BaseURL(ctx)

	// If user has any membership, redirect to /usage
	if len(memberships) > 0 {
		http.Redirect(w, r, baseURL.JoinPath("/usage").String(), http.StatusTemporaryRedirect)
		return
	}

	// Fetch pending invitations for the user's email
	invites, err := h.inviteStore.ListPendingInvitesForEmail(ctx, httpCtx.Tenant(ctx).ID(), user.Email())
	if err != nil {
		slog.ErrorContext(ctx, "could not fetch invitations", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	// Collect declined invite IDs from cookies
	var declinedIDs []string
	for _, inv := range invites {
		cookieName := fmt.Sprintf("declined_invite_%s", string(inv.ID()))
		if _, err := r.Cookie(cookieName); err == nil {
			declinedIDs = append(declinedIDs, string(inv.ID()))
		}
	}

	vmodel := component.NoOrgPageVModel{
		User:        user,
		Invites:     invites,
		DeclinedIDs: declinedIDs,
		IsAdmin:     slices.Contains(user.Roles(), authz.RoleAdmin),
	}

	templ.Handler(component.NoOrgPage(vmodel)).ServeHTTP(w, r)
}
