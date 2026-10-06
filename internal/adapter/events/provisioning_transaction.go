package events

import (
	"context"
	"strconv"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

type ProvisioningTransaction struct {
	backend port.ProvisioningTransaction
	emitter port.EventEmitter
}

func NewProvisioningTransaction(backend port.ProvisioningTransaction, emitter port.EventEmitter) *ProvisioningTransaction {
	return &ProvisioningTransaction{backend, emitter}
}
func (s *ProvisioningTransaction) WithProvisioningTransaction(ctx context.Context, fn func(port.ProvisioningTx) error) error {
	ctx = model.EnsureActor(ctx)
	var committed []model.Event
	err := s.backend.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
		committed = nil
		buffer := &invitationEvents{}
		if err := fn(&provisioningTx{ProvisioningTx: tx, emitter: buffer}); err != nil {
			return err
		}
		committed = buffer.events
		return nil
	})
	if err != nil {
		return err
	}
	if s.emitter != nil {
		for _, event := range committed {
			s.emitter.Emit(ctx, event)
		}
	}
	return nil
}

type provisioningTx struct {
	port.ProvisioningTx
	emitter port.EventEmitter
}

func (s *provisioningTx) AddMember(ctx context.Context, membership model.Membership) error {
	if err := s.ProvisioningTx.AddMember(ctx, membership); err != nil {
		return err
	}
	emit(ctx, s.emitter, membership.OrgID(), model.SeverityInfo, model.EventTypeMemberAdded,
		"Membre ajouté à l'organisation",
		map[string]string{
			"membership_id":  string(membership.ID()),
			"member_user_id": string(membership.UserID()),
		})
	return nil
}

func (s *provisioningTx) RemoveMember(ctx context.Context, id model.MembershipID) error {
	existing, _ := s.ProvisioningTx.GetMembership(ctx, id)
	if err := s.ProvisioningTx.RemoveMember(ctx, id); err != nil {
		return err
	}
	orgID := model.OrgID("")
	attrs := map[string]string{"membership_id": string(id)}
	if existing != nil {
		orgID = existing.OrgID()
		attrs["member_user_id"] = string(existing.UserID())
	}
	emit(ctx, s.emitter, orgID, model.SeverityWarning, model.EventTypeMemberRemoved,
		"Membre retiré de l'organisation", attrs)
	return nil
}

func (s *provisioningTx) CreateRole(ctx context.Context, role model.Role) error {
	if err := s.ProvisioningTx.CreateRole(ctx, role); err != nil {
		return err
	}
	if role.Builtin() {
		return nil
	}
	emit(ctx, s.emitter, role.OrgID(), model.SeverityInfo, model.EventTypeRoleCreated,
		"Rôle créé : "+role.Name(),
		map[string]string{"role_id": string(role.ID()), "role_name": role.Name()})
	return nil
}

func (s *provisioningTx) SaveRole(ctx context.Context, role model.Role) error {
	if err := s.ProvisioningTx.SaveRole(ctx, role); err != nil {
		return err
	}
	if role.Builtin() {
		return nil
	}
	emit(ctx, s.emitter, role.OrgID(), model.SeverityInfo, model.EventTypeRoleUpdated,
		"Rôle modifié : "+role.Name(),
		map[string]string{"role_id": string(role.ID()), "role_name": role.Name()})
	return nil
}

func (s *provisioningTx) DeleteRole(ctx context.Context, id model.RoleID) error {
	existing, _ := s.ProvisioningTx.GetRoleByID(ctx, id)
	if err := s.ProvisioningTx.DeleteRole(ctx, id); err != nil {
		return err
	}
	if existing != nil && existing.Builtin() {
		return nil
	}
	orgID := model.OrgID("")
	attrs := map[string]string{"role_id": string(id)}
	msg := "Rôle supprimé"
	if existing != nil {
		orgID = existing.OrgID()
		attrs["role_name"] = existing.Name()
		msg = "Rôle supprimé : " + existing.Name()
	}
	emit(ctx, s.emitter, orgID, model.SeverityWarning, model.EventTypeRoleDeleted, msg, attrs)
	return nil
}

func (s *provisioningTx) SetMembershipRoles(ctx context.Context, membershipID model.MembershipID, roleIDs []model.RoleID) error {
	if err := s.ProvisioningTx.SetMembershipRoles(ctx, membershipID, roleIDs); err != nil {
		return err
	}
	orgID := model.OrgID("")
	userID := model.UserID("")
	if s.ProvisioningTx != nil {
		if m, err := s.ProvisioningTx.GetMembership(ctx, membershipID); err == nil && m != nil {
			orgID = m.OrgID()
			userID = m.UserID()
		}
	}
	emit(ctx, s.emitter, orgID, model.SeverityInfo, model.EventTypeMemberUpdated,
		"Rôles de membre modifiés",
		map[string]string{
			"membership_id":  string(membershipID),
			"member_user_id": string(userID),
			"role_count":     strconv.Itoa(len(roleIDs)),
		})
	return nil
}

var _ port.ProvisioningTransaction = (*ProvisioningTransaction)(nil)
