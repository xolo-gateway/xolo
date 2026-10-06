package gorm

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/pkg/errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CreateRole implements port.RoleStore.
func (s *Store) CreateRole(ctx context.Context, role model.Role) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		if err := db.Create(fromRole(role)).Error; err != nil {
			// The (org_id, name) index is spelled "roles.org_id, roles.name" by
			// SQLite and "role_org_name_index" by PostgreSQL, whose Detail line
			// carries "Key (org_id, name)=…": match on the column names only.
			if isUniqueViolation(err, "org_id", "name") {
				return errors.Wrapf(port.ErrAlreadyExists, "role %q already exists in this organization", role.Name())
			}
			return errors.WithStack(err)
		}
		return nil
	})
}

// GetRoleByID implements port.RoleStore.
func (s *Store) GetRoleByID(ctx context.Context, id model.RoleID) (model.Role, error) {
	var role Role
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		if err := db.Preload("Permissions").Preload("ModelGrants").
			First(&role, "id = ?", string(id)).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.WithStack(port.ErrNotFound)
			}
			return errors.WithStack(err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &wrappedRole{&role}, nil
}

// ListOrgRoles implements port.RoleStore.
func (s *Store) ListOrgRoles(ctx context.Context, orgID model.OrgID) ([]model.Role, error) {
	var roles []*Role
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Preload("Permissions").Preload("ModelGrants").
			Where("org_id = ?", string(orgID)).
			Order("builtin DESC, name ASC").
			Find(&roles).Error)
	})
	if err != nil {
		return nil, err
	}
	result := make([]model.Role, 0, len(roles))
	for _, r := range roles {
		result = append(result, &wrappedRole{r})
	}
	return result, nil
}

// SaveRole implements port.RoleStore. It upserts the role and fully replaces
// its permissions and model grants.
func (s *Store) SaveRole(ctx context.Context, role model.Role) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		gormRole := fromRole(role)

		if err := db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			UpdateAll: true,
		}).Omit("Permissions", "ModelGrants", "Org").Create(gormRole).Error; err != nil {
			return errors.WithStack(err)
		}

		// Fully replace permissions.
		if err := db.Where("role_id = ?", gormRole.ID).Delete(&RolePermission{}).Error; err != nil {
			return errors.WithStack(err)
		}
		for _, p := range gormRole.Permissions {
			p.RoleID = gormRole.ID
			if err := db.Create(p).Error; err != nil {
				return errors.WithStack(err)
			}
		}

		// Fully replace model grants.
		if err := db.Where("role_id = ?", gormRole.ID).Delete(&RoleModel{}).Error; err != nil {
			return errors.WithStack(err)
		}
		for _, g := range gormRole.ModelGrants {
			g.RoleID = gormRole.ID
			if err := db.Create(g).Error; err != nil {
				return errors.WithStack(err)
			}
		}

		return nil
	})
}

// DeleteRole implements port.RoleStore. It refuses to delete builtin roles.
func (s *Store) DeleteRole(ctx context.Context, id model.RoleID) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		var role Role
		if err := db.First(&role, "id = ?", string(id)).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.WithStack(port.ErrNotFound)
			}
			return errors.WithStack(err)
		}
		if role.Builtin {
			return errors.WithStack(port.ErrNotAllowed)
		}
		if err := db.Where("role_id = ?", string(id)).Delete(&MembershipRole{}).Error; err != nil {
			return errors.WithStack(err)
		}
		if err := db.Where("role_id = ?", string(id)).Delete(&ApplicationRole{}).Error; err != nil {
			return errors.WithStack(err)
		}
		return errors.WithStack(db.Delete(&Role{}, "id = ?", string(id)).Error)
	})
}

// SetMembershipRoles implements port.RoleStore. It replaces the full set of
// roles assigned to a membership.
func (s *Store) SetMembershipRoles(ctx context.Context, membershipID model.MembershipID, roleIDs []model.RoleID) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		if err := db.Where("membership_id = ?", string(membershipID)).Delete(&MembershipRole{}).Error; err != nil {
			return errors.WithStack(err)
		}
		for _, roleID := range roleIDs {
			if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&MembershipRole{
				MembershipID: string(membershipID),
				RoleID:       string(roleID),
			}).Error; err != nil {
				return errors.WithStack(err)
			}
		}
		return nil
	})
}

// ListMembershipRoles implements port.RoleStore.
func (s *Store) ListMembershipRoles(ctx context.Context, membershipID model.MembershipID) ([]model.Role, error) {
	var roles []*Role
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Preload("Permissions").Preload("ModelGrants").
			Joins("JOIN membership_roles mr ON mr.role_id = roles.id").
			Where("mr.membership_id = ?", string(membershipID)).
			Find(&roles).Error)
	})
	if err != nil {
		return nil, err
	}
	result := make([]model.Role, 0, len(roles))
	for _, r := range roles {
		result = append(result, &wrappedRole{r})
	}
	return result, nil
}

// SetApplicationRoles implements port.RoleStore. It replaces the full set of
// roles assigned to an application.
func (s *Store) SetApplicationRoles(ctx context.Context, appID model.ApplicationID, roleIDs []model.RoleID) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		if err := db.Where("application_id = ?", string(appID)).Delete(&ApplicationRole{}).Error; err != nil {
			return errors.WithStack(err)
		}
		for _, roleID := range roleIDs {
			if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&ApplicationRole{
				ApplicationID: string(appID),
				RoleID:        string(roleID),
			}).Error; err != nil {
				return errors.WithStack(err)
			}
		}
		return nil
	})
}

// ListApplicationRoles implements port.RoleStore.
func (s *Store) ListApplicationRoles(ctx context.Context, appID model.ApplicationID) ([]model.Role, error) {
	var roles []*Role
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Preload("Permissions").Preload("ModelGrants").
			Joins("JOIN application_roles ar ON ar.role_id = roles.id").
			Where("ar.application_id = ?", string(appID)).
			Find(&roles).Error)
	})
	if err != nil {
		return nil, err
	}
	result := make([]model.Role, 0, len(roles))
	for _, r := range roles {
		result = append(result, &wrappedRole{r})
	}
	return result, nil
}

// EnsureBuiltinRoles implements port.RoleStore. It creates the owner/admin/member
// builtin roles for an organization if they do not already exist. Idempotent.
func (s *Store) EnsureBuiltinRoles(ctx context.Context, orgID model.OrgID) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		for _, spec := range builtinRoleSpecs() {
			var count int64
			if err := db.Model(&Role{}).
				Where("org_id = ? AND builtin_kind = ?", string(orgID), spec.kind).
				Count(&count).Error; err != nil {
				return errors.WithStack(err)
			}
			if count > 0 {
				continue
			}
			role := model.NewBuiltinRole(orgID, spec.kind, spec.name, spec.description, spec.permissions)
			gormRole := fromRole(role)
			if err := db.Omit("Org").Create(gormRole).Error; err != nil {
				return errors.WithStack(err)
			}
		}
		return nil
	})
}

// ResolveEffectivePermissions implements port.RoleStore. It returns the union
// of the permissions of all roles assigned to the user within the organization.
func (s *Store) ResolveEffectivePermissions(ctx context.Context, userID model.UserID, orgID model.OrgID) (rbac.PermissionSet, error) {
	var roles []*Role
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		var m Membership
		if err := db.Where("user_id = ? AND org_id = ?", string(userID), string(orgID)).First(&m).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil // not a member: no permissions
			}
			return errors.WithStack(err)
		}
		return errors.WithStack(db.Preload("Permissions").Preload("ModelGrants").
			Joins("JOIN membership_roles mr ON mr.role_id = roles.id").
			Where("mr.membership_id = ?", m.ID).
			Find(&roles).Error)
	})
	if err != nil {
		return rbac.PermissionSet{}, err
	}

	return permissionSetFromRoles(roles), nil
}

// ResolveApplicationPermissions implements port.RoleStore. It returns the union
// of the permissions of all roles assigned to the application. The application
// must belong to orgID: a token scoped to another organization resolves to an
// empty set, never to the application's own permissions.
func (s *Store) ResolveApplicationPermissions(ctx context.Context, appID model.ApplicationID, orgID model.OrgID) (rbac.PermissionSet, error) {
	var roles []*Role
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		var app Application
		if err := db.Where("id = ? AND org_id = ?", string(appID), string(orgID)).First(&app).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil // unknown application, or not scoped to this org: no permissions
			}
			return errors.WithStack(err)
		}
		if !app.Active {
			return nil // deactivated application: no permissions
		}
		// Only roles owned by the same organization are honoured, so a role that
		// was moved or mis-assigned can never widen the application's scope.
		return errors.WithStack(db.Preload("Permissions").Preload("ModelGrants").
			Joins("JOIN application_roles ar ON ar.role_id = roles.id").
			Where("ar.application_id = ? AND roles.org_id = ?", string(appID), string(orgID)).
			Find(&roles).Error)
	})
	if err != nil {
		return rbac.PermissionSet{}, err
	}

	return permissionSetFromRoles(roles), nil
}

// permissionSetFromRoles unions the permissions and model grants of the given
// roles. A builtin owner role short-circuits to the all-access set.
func permissionSetFromRoles(roles []*Role) rbac.PermissionSet {
	var codes []string
	var grants []rbac.ModelGrant
	for _, r := range roles {
		if r.BuiltinKind == model.BuiltinKindOwner {
			return rbac.OwnerPermissionSet()
		}
		for _, p := range r.Permissions {
			codes = append(codes, p.Code)
		}
		for _, g := range r.ModelGrants {
			grants = append(grants, rbac.ModelGrant{ModelID: g.ModelID, Kind: g.ModelKind})
		}
	}
	return rbac.NewPermissionSet(codes, grants)
}

type builtinRoleSpec struct {
	kind        string
	name        string
	description string
	permissions []string
}

// builtinRoleSpecs returns the definitions of the builtin roles created for
// every organization. The "owner" role bypasses permission checks entirely so
// its permission list is left empty.
func builtinRoleSpecs() []builtinRoleSpec {
	allAdmin := make([]string, 0)
	for _, group := range rbac.Catalog() {
		for _, def := range group.Perms {
			allAdmin = append(allAdmin, string(def.Code))
		}
	}
	return []builtinRoleSpec{
		{
			kind:        model.BuiltinKindOwner,
			name:        "Propriétaire",
			description: "Accès complet à l'organisation (non modifiable).",
		},
		{
			kind:        model.BuiltinKindAdmin,
			name:        "Administrateur",
			description: "Accès à toutes les sections d'administration.",
			permissions: allAdmin,
		},
		{
			kind:        model.BuiltinKindMember,
			name:        "Membre",
			description: "Accès de base : usage des modèles et de l'organisation.",
			permissions: []string{
				string(rbac.PermUsageRead),
				string(rbac.PermModelUseOrg),
				string(rbac.PermModelUseVirtual),
				string(rbac.PermPersonalVMCreate),
			},
		},
	}
}

var _ port.RoleStore = &Store{}
