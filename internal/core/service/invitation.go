package service

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

type InvitationService struct {
	transactions port.InvitationTransaction
}

func NewInvitationService(transactions port.InvitationTransaction) *InvitationService {
	return &InvitationService{transactions: transactions}
}

// InvitationView never contains invitation details when LoginRequired is true.
type InvitationView struct {
	Invite        model.InviteToken
	Org           model.Organization
	Role          model.Role
	LoginRequired bool
}

type InvitationAcceptance struct {
	InvitationView
	AlreadyMember bool
	Roles         []model.Role
}

type CreateInvitationInput struct {
	Role, Email, ExpiresAt, MaxUses string
}

// Prepare checks scope and recipient before exposing any invitation details.
// An empty userID denotes an anonymous visitor.
func (s *InvitationService) Prepare(ctx context.Context, tenantID model.TenantID, id model.InviteTokenID, userID model.UserID) (*InvitationView, error) {
	var result *InvitationView
	err := s.transactions.WithInvitationTransaction(ctx, func(tx port.InvitationTx) error {
		result = nil
		view, err := prepareInvitation(ctx, tx, tenantID, id, userID, true)
		if err != nil {
			return err
		}
		result = view
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func invitationUser(ctx context.Context, tx port.InvitationTx, tenantID model.TenantID, userID model.UserID) (model.User, error) {
	if userID == "" || tenantID == "" {
		return nil, port.ErrNotFound
	}
	user, err := tx.GetUserByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get invitation user: %w", err)
	}
	if user.TenantID() != tenantID {
		return nil, port.ErrNotFound
	}
	if !user.Active() {
		return nil, port.ErrNotAllowed
	}
	return user, nil
}

func prepareInvitation(ctx context.Context, tx port.InvitationTx, tenantID model.TenantID, id model.InviteTokenID, userID model.UserID, anonymous bool) (*InvitationView, error) {
	if tenantID == "" {
		return nil, port.ErrNotFound
	}
	invite, org, err := NewInvitationResolver(tx, tx).Resolve(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if userID != "" || !anonymous {
		user, err := invitationUser(ctx, tx, tenantID, userID)
		if err != nil {
			return nil, err
		}
		if email := invite.InviteeEmail(); email != nil && !strings.EqualFold(*email, user.Email()) {
			return nil, port.ErrNotFound
		}
	}
	if !org.Active() || !model.IsInviteValid(invite) || (invite.InviteeEmail() != nil && invite.UsesCount() > 0) {
		return nil, port.ErrInvalid
	}
	role, err := resolveInvitationRole(ctx, tx, org.ID(), invite.Role())
	if err != nil {
		return nil, err
	}
	if anonymous && userID == "" && invite.InviteeEmail() != nil {
		return &InvitationView{LoginRequired: true}, nil
	}
	return &InvitationView{Invite: invite, Org: org, Role: role}, nil
}

func resolveInvitationRole(ctx context.Context, tx port.InvitationTx, orgID model.OrgID, value string) (model.Role, error) {
	var kind string
	switch value {
	case model.RoleMember:
		kind = model.BuiltinKindMember
	case model.RoleOrgAdmin:
		kind = model.BuiltinKindAdmin
	case model.RoleOrgOwner:
		kind = model.BuiltinKindOwner
	default:
		role, err := tx.GetRoleByID(ctx, model.RoleID(value))
		if errors.Is(err, port.ErrNotFound) {
			return nil, port.ErrInvalid
		}
		if err != nil {
			return nil, fmt.Errorf("get invitation role: %w", err)
		}
		if role.OrgID() != orgID {
			return nil, port.ErrInvalid
		}
		return role, nil
	}
	roles, err := tx.ListOrgRoles(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list invitation roles: %w", err)
	}
	for _, role := range roles {
		if role.OrgID() == orgID && role.Builtin() && role.BuiltinKind() == kind {
			return role, nil
		}
	}
	return nil, port.ErrInvalid
}

// Create expects the caller to have checked invites:write for orgID.
func (s *InvitationService) Create(ctx context.Context, tenantID model.TenantID, orgID model.OrgID, actorID model.UserID, input CreateInvitationInput) (model.InviteToken, error) {
	var result model.InviteToken
	err := s.transactions.WithInvitationTransaction(ctx, func(tx port.InvitationTx) error {
		result = nil
		org, err := tx.GetOrgByID(ctx, orgID)
		if err != nil {
			return err
		}
		if tenantID == "" || org.TenantID() != tenantID {
			return port.ErrNotFound
		}
		if _, err := invitationUser(ctx, tx, tenantID, actorID); err != nil {
			return err
		}
		if !org.Active() {
			return port.ErrInvalid
		}
		value := input.Role
		if value == "" {
			value = model.RoleMember
		}
		role, err := resolveInvitationRole(ctx, tx, orgID, value)
		if err != nil {
			return err
		}
		var email *string
		if input.Email != "" {
			address, err := mail.ParseAddress(input.Email)
			if err != nil || address.Address != input.Email {
				return fmt.Errorf("invalid invitation email: %w", port.ErrInvalid)
			}
			email = &input.Email
		}
		var expires *time.Time
		if input.ExpiresAt != "" {
			date, err := time.Parse("2006-01-02", input.ExpiresAt)
			if err != nil || !date.After(time.Now()) {
				return fmt.Errorf("invalid invitation expiration: %w", port.ErrInvalid)
			}
			expires = &date
		}
		var limit *int
		if input.MaxUses != "" {
			n, err := strconv.Atoi(input.MaxUses)
			if err != nil || n <= 0 {
				return fmt.Errorf("invalid invitation limit: %w", port.ErrInvalid)
			}
			limit = &n
		}
		if email != nil {
			n := 1
			limit = &n
		}
		invite := model.NewInviteToken(orgID, string(role.ID()), email, expires, limit, actorID)
		if err := tx.CreateInvite(ctx, invite); err != nil {
			return fmt.Errorf("create invitation: %w", err)
		}
		result = invite
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *InvitationService) Accept(ctx context.Context, tenantID model.TenantID, id model.InviteTokenID, userID model.UserID) (*InvitationAcceptance, error) {
	var result *InvitationAcceptance
	err := s.transactions.WithInvitationTransaction(ctx, func(tx port.InvitationTx) error {
		result = nil
		view, err := prepareInvitation(ctx, tx, tenantID, id, userID, false)
		if err != nil {
			return err
		}
		membership, err := tx.GetUserOrgMembership(ctx, userID, view.Org.ID())
		if err == nil {
			result = &InvitationAcceptance{InvitationView: *view, AlreadyMember: true, Roles: membership.Roles()}
			return nil
		}
		if !errors.Is(err, port.ErrNotFound) {
			return fmt.Errorf("get invitation membership: %w", err)
		}
		membership = model.NewMembership(userID, view.Org.ID())
		created, err := tx.InsertInvitationMember(ctx, membership)
		if err != nil {
			return fmt.Errorf("insert invitation membership: %w", err)
		}
		if !created {
			existing, err := tx.GetUserOrgMembership(ctx, userID, view.Org.ID())
			if err != nil {
				return err
			}
			result = &InvitationAcceptance{InvitationView: *view, AlreadyMember: true, Roles: existing.Roles()}
			return nil
		}
		if err := tx.SetMembershipRoles(ctx, membership.ID(), []model.RoleID{view.Role.ID()}); err != nil {
			return fmt.Errorf("assign invitation role: %w", err)
		}
		if err := tx.IncrementInviteUses(ctx, id); err != nil {
			return fmt.Errorf("consume invitation: %w", err)
		}
		if view.Invite.InviteeEmail() != nil {
			if err := tx.DeleteInvite(ctx, id); err != nil {
				return fmt.Errorf("delete accepted invitation: %w", err)
			}
		}
		result = &InvitationAcceptance{InvitationView: *view, Roles: []model.Role{view.Role}}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Decline deletes a targeted invitation, or returns true for an open invitation
// that the handler should hide locally using its existing cookie.
func (s *InvitationService) Decline(ctx context.Context, tenantID model.TenantID, id model.InviteTokenID, userID model.UserID) (bool, error) {
	open := false
	err := s.transactions.WithInvitationTransaction(ctx, func(tx port.InvitationTx) error {
		open = false
		view, err := prepareInvitation(ctx, tx, tenantID, id, userID, false)
		if err != nil {
			return err
		}
		if view.Invite.InviteeEmail() == nil {
			open = true
			return nil
		}
		return tx.DeleteInvite(ctx, id)
	})
	if err != nil {
		return false, err
	}
	return open, nil
}
