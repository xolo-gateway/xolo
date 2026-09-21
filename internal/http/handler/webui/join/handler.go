package join

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/slogx"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/join/component"
	"github.com/pkg/errors"
)

type Handler struct {
	mux         *http.ServeMux
	orgStore    port.OrgStore
	roleStore   port.RoleStore
	inviteStore port.InviteStore
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func NewHandler(orgStore port.OrgStore, roleStore port.RoleStore, inviteStore port.InviteStore) *Handler {
	h := &Handler{
		mux:         http.NewServeMux(),
		orgStore:    orgStore,
		roleStore:   roleStore,
		inviteStore: inviteStore,
	}

	h.mux.HandleFunc("GET /{tokenID}", h.getJoinPage)
	h.mux.HandleFunc("POST /{tokenID}", h.acceptInvite)

	return h
}

func (h *Handler) getJoinPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	tokenID := r.PathValue("tokenID")

	invite, err := h.inviteStore.GetInviteByID(ctx, model.InviteTokenID(tokenID))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			h.renderError(w, r, user, "Cette invitation est introuvable ou a expiré.")
			return
		}
		slog.ErrorContext(ctx, "could not get invite", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if !model.IsInviteValid(invite) {
		h.renderError(w, r, user, "Cette invitation n'est plus valide (expirée ou révoquée).")
		return
	}

	// Targeted invites can only be accepted by their addressee. The comparison
	// is case-insensitive: the case an administrator typed and the one the
	// identity provider returns rarely agree.
	if user != nil && invite.InviteeEmail() != nil && !strings.EqualFold(*invite.InviteeEmail(), user.Email()) {
		h.renderError(w, r, user, "Cette invitation n'est pas destinée à votre adresse email.")
		return
	}

	baseURL := httpCtx.BaseURL(ctx)
	loginURL := baseURL.JoinPath("/auth/oidc/login").String()
	declineURL := baseURL.JoinPath("/no-org/invitations/" + tokenID + "/decline").String()

	vmodel := component.JoinPageVModel{
		User:       user,
		Invite:     invite,
		LoginURL:   loginURL,
		DeclineURL: declineURL,
	}

	templ.Handler(component.JoinPage(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) acceptInvite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	tokenID := r.PathValue("tokenID")

	if user == nil {
		http.Redirect(w, r, "/auth/oidc/login", http.StatusSeeOther)
		return
	}

	invite, err := h.inviteStore.GetInviteByID(ctx, model.InviteTokenID(tokenID))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			h.renderError(w, r, user, "Cette invitation est introuvable ou a expiré.")
			return
		}
		slog.ErrorContext(ctx, "could not get invite", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if !model.IsInviteValid(invite) {
		h.renderError(w, r, user, "Cette invitation n'est plus valide (expirée ou révoquée).")
		return
	}

	// Check if targeted invite matches current user's email
	if invite.InviteeEmail() != nil && !strings.EqualFold(*invite.InviteeEmail(), user.Email()) {
		h.renderError(w, r, user, "Cette invitation n'est pas destinée à votre adresse email.")
		return
	}

	// Check if already a member
	already, err := h.orgStore.IsMember(ctx, user.ID(), invite.OrgID())
	if err != nil {
		slog.ErrorContext(ctx, "could not check membership", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if !already {
		membership := model.NewMembership(user.ID(), invite.OrgID())
		if err := h.orgStore.AddMember(ctx, membership); err != nil {
			slog.ErrorContext(ctx, "could not add member", slogx.Error(err))
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}

		// Assign the builtin role matching the invite's role to the new member.
		if err := h.assignInviteRole(ctx, membership.ID(), invite.OrgID(), invite.Role()); err != nil {
			slog.ErrorContext(ctx, "could not assign role to new member", slogx.Error(err))
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
	}

	if err := h.inviteStore.IncrementInviteUses(ctx, invite.ID()); err != nil {
		slog.WarnContext(ctx, "could not increment invite uses", slogx.Error(err))
	}

	// Targeted invites are single-use by design: delete them after acceptance.
	if invite.InviteeEmail() != nil {
		if err := h.inviteStore.DeleteInvite(ctx, invite.ID()); err != nil {
			slog.WarnContext(ctx, "could not delete targeted invite after acceptance", slogx.Error(err))
		}
	}

	org, err := h.orgStore.GetOrgByID(ctx, invite.OrgID())
	if err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	vmodel := component.JoinSuccessVModel{
		User:    user,
		OrgName: org.Name(),
		OrgSlug: org.Slug(),
		Role:    invite.Role(),
	}

	templ.Handler(component.JoinSuccess(vmodel)).ServeHTTP(w, r)
}

// assignInviteRole assigns a role to the given membership.
// inviteRole may be a role ID (for custom or new-style invites) or a legacy
// builtin string ("member", "org:admin", "org:owner") for backward compat.
func (h *Handler) assignInviteRole(ctx context.Context, membershipID model.MembershipID, orgID model.OrgID, inviteRole string) error {
	if err := h.roleStore.EnsureBuiltinRoles(ctx, orgID); err != nil {
		return errors.WithStack(err)
	}

	// Try direct role ID lookup (new-style invites store a role ID).
	role, err := h.roleStore.GetRoleByID(ctx, model.RoleID(inviteRole))
	if err == nil {
		return errors.WithStack(h.roleStore.SetMembershipRoles(ctx, membershipID, []model.RoleID{role.ID()}))
	}
	if !errors.Is(err, port.ErrNotFound) {
		return errors.WithStack(err)
	}

	// Fall back to builtin kind matching for legacy invite strings.
	var kind string
	switch inviteRole {
	case model.RoleOrgOwner:
		kind = model.BuiltinKindOwner
	case model.RoleOrgAdmin:
		kind = model.BuiltinKindAdmin
	default:
		kind = model.BuiltinKindMember
	}

	roles, err := h.roleStore.ListOrgRoles(ctx, orgID)
	if err != nil {
		return errors.WithStack(err)
	}
	for _, r := range roles {
		if r.BuiltinKind() == kind {
			return errors.WithStack(h.roleStore.SetMembershipRoles(ctx, membershipID, []model.RoleID{r.ID()}))
		}
	}
	return nil
}

func (h *Handler) renderError(w http.ResponseWriter, r *http.Request, user model.User, msg string) {
	vmodel := component.JoinErrorVModel{
		User:    user,
		Message: msg,
	}
	w.WriteHeader(http.StatusBadRequest)
	templ.Handler(component.JoinError(vmodel)).ServeHTTP(w, r)
}

var _ http.Handler = &Handler{}
