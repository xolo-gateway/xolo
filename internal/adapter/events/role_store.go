package events

import (
	"context"
	"strconv"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// principalResolver resolves the org a membership or an application belongs to.
// It lets the role decorator attribute role-assignment changes (a cross-aggregate
// operation) to the right organization and principal.
type principalResolver interface {
	GetMembership(ctx context.Context, id model.MembershipID) (model.Membership, error)
	GetApplication(ctx context.Context, id model.ApplicationID) (model.Application, error)
}

// RoleStore decorates a port.RoleStore, emitting events on custom role
// mutations. Builtin roles (created via EnsureBuiltinRoles or otherwise flagged)
// never produce events.
type RoleStore struct {
	port.RoleStore
	emitter    port.EventEmitter
	principals principalResolver
}

func NewRoleStore(backend port.RoleStore, emitter port.EventEmitter, principals principalResolver) *RoleStore {
	return &RoleStore{RoleStore: backend, emitter: emitter, principals: principals}
}

func (s *RoleStore) CreateRole(ctx context.Context, role model.Role) error {
	if err := s.RoleStore.CreateRole(ctx, role); err != nil {
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

func (s *RoleStore) SaveRole(ctx context.Context, role model.Role) error {
	if err := s.RoleStore.SaveRole(ctx, role); err != nil {
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

func (s *RoleStore) DeleteRole(ctx context.Context, id model.RoleID) error {
	existing, _ := s.RoleStore.GetRoleByID(ctx, id)
	if err := s.RoleStore.DeleteRole(ctx, id); err != nil {
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

func (s *RoleStore) SetMembershipRoles(ctx context.Context, membershipID model.MembershipID, roleIDs []model.RoleID) error {
	if err := s.RoleStore.SetMembershipRoles(ctx, membershipID, roleIDs); err != nil {
		return err
	}
	orgID := model.OrgID("")
	userID := model.UserID("")
	if s.principals != nil {
		if m, err := s.principals.GetMembership(ctx, membershipID); err == nil && m != nil {
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

func (s *RoleStore) SetApplicationRoles(ctx context.Context, appID model.ApplicationID, roleIDs []model.RoleID) error {
	if err := s.RoleStore.SetApplicationRoles(ctx, appID, roleIDs); err != nil {
		return err
	}
	orgID := model.OrgID("")
	name := ""
	if s.principals != nil {
		if app, err := s.principals.GetApplication(ctx, appID); err == nil && app != nil {
			orgID = app.OrgID()
			name = app.Name()
		}
	}
	msg := "Rôles d'application modifiés"
	if name != "" {
		msg += " : " + name
	}
	emit(ctx, s.emitter, orgID, model.SeverityInfo, model.EventTypeApplicationUpdated, msg,
		map[string]string{
			"application_id":   string(appID),
			"application_name": name,
			"role_count":       strconv.Itoa(len(roleIDs)),
		})
	return nil
}

var _ port.RoleStore = &RoleStore{}
