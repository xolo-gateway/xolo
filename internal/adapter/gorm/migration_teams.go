package gorm

import (
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/rs/xid"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const teamsMigrationID = "202610150002"

// migrateTeams introduces teams and moves model access from roles to them.
//
// Every custom role carrying model grants becomes a team with the same grants
// and, as members, the memberships holding the role: nobody loses access. An
// application holding one such role is bound to its team; one holding several
// gets a team of its own with the union of their grants, since an application
// is bound to a single team. The role keeps its permissions, and the
// role_models table is dropped.
//
// The builtin admin roles gain the teams permissions, and the projections of
// the custom roles (which no longer carry grants), applications (now bound to
// a team) and quotas (now with a cascade) are published again.
//
// The migration must run with the other instances stopped: an older instance
// still reading role_models would fail once the table is dropped, and one
// still writing it would lose its grants.
func migrateTeams(tx *gorm.DB) error {
	if err := tx.AutoMigrate(
		&Team{}, &TeamModelGrant{}, &TeamMiddleware{}, &TeamMember{}, &TeamMemberRole{},
		&Quota{}, &Application{}, &AuthToken{}, &PersonalVirtualModel{}, &UsageRecord{}, &InviteToken{},
	); err != nil {
		return errors.WithStack(err)
	}
	if err := grantBuiltinAdminTeams(tx); err != nil {
		return err
	}
	var created []string
	if tx.Migrator().HasTable("role_models") {
		var err error
		if created, err = convertRoleGrantsToTeams(tx); err != nil {
			return err
		}
		if err := tx.Migrator().DropTable("role_models"); err != nil {
			return errors.WithStack(err)
		}
	}
	return republishProjections(tx, created)
}

// grantBuiltinAdminTeams adds the permissions introduced with teams to the
// builtin admin roles, which hold every permission of the catalog.
func grantBuiltinAdminTeams(tx *gorm.DB) error {
	var roleIDs []string
	if err := tx.Model(&Role{}).Where("builtin_kind = ?", model.BuiltinKindAdmin).Order("id").Pluck("id", &roleIDs).Error; err != nil {
		return errors.WithStack(err)
	}
	for _, roleID := range roleIDs {
		for _, code := range []rbac.Permission{rbac.PermTeamsRead, rbac.PermTeamsWrite} {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&RolePermission{RoleID: roleID, Code: string(code)}).Error; err != nil {
				return errors.WithStack(err)
			}
		}
	}
	return nil
}

// convertRoleGrantsToTeams creates the teams replacing the role grants and
// returns their ids.
func convertRoleGrantsToTeams(tx *gorm.DB) ([]string, error) {
	var grants []legacyRoleModel
	if err := tx.Order("role_id, model_id, model_kind").Find(&grants).Error; err != nil {
		return nil, errors.WithStack(err)
	}
	if len(grants) == 0 {
		return nil, nil
	}
	byRole := map[string][]legacyRoleModel{}
	var roleIDs []string
	for _, g := range grants {
		if _, seen := byRole[g.RoleID]; !seen {
			roleIDs = append(roleIDs, g.RoleID)
		}
		byRole[g.RoleID] = append(byRole[g.RoleID], g)
	}
	var roles []Role
	if err := tx.Where("id IN ?", roleIDs).Order("org_id, name, id").Find(&roles).Error; err != nil {
		return nil, errors.WithStack(err)
	}

	logOrphanRoleGrants(tx, roleIDs, roles, grants)

	var created []string
	teamOfRole := map[string]string{}
	for _, role := range roles {
		team, err := createMigratedTeam(tx, role.OrgID, role.Name, "Créée à partir du rôle « "+role.Name+" » lors de l'introduction des équipes.", byRole[role.ID])
		if err != nil {
			return nil, err
		}
		created = append(created, team)
		teamOfRole[role.ID] = team
		var memberships []string
		if err := tx.Table("membership_roles").Where("role_id = ?", role.ID).Order("membership_id").Pluck("membership_id", &memberships).Error; err != nil {
			return nil, errors.WithStack(err)
		}
		for _, membership := range memberships {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&TeamMember{TeamID: team, MembershipID: membership, CreatedAt: time.Now()}).Error; err != nil {
				return nil, errors.WithStack(err)
			}
		}
		slog.InfoContext(tx.Statement.Context, "role model grants moved to a team",
			slog.String("role", role.ID), slog.String("team", team), slog.Int("members", len(memberships)))
	}

	// Applications are bound to one team at most.
	var appRoles []ApplicationRole
	if err := tx.Where("role_id IN ?", roleIDs).Order("application_id, role_id").Find(&appRoles).Error; err != nil {
		return nil, errors.WithStack(err)
	}
	byApp := map[string][]string{}
	var appIDs []string
	// An application role whose role is gone matches nothing. Its application
	// is named so that an operator can bind it to a team after the upgrade.
	var skipped []string
	for _, ar := range appRoles {
		if _, ok := teamOfRole[ar.RoleID]; !ok {
			if !slices.Contains(skipped, ar.ApplicationID) {
				skipped = append(skipped, ar.ApplicationID)
			}
			continue
		}
		if _, seen := byApp[ar.ApplicationID]; !seen {
			appIDs = append(appIDs, ar.ApplicationID)
		}
		byApp[ar.ApplicationID] = append(byApp[ar.ApplicationID], ar.RoleID)
	}
	for _, appID := range appIDs {
		var app Application
		if err := tx.First(&app, "id = ?", appID).Error; err != nil {
			return nil, errors.WithStack(err)
		}
		team := teamOfRole[byApp[appID][0]]
		if len(byApp[appID]) > 1 {
			var union []legacyRoleModel
			for _, roleID := range byApp[appID] {
				union = append(union, byRole[roleID]...)
			}
			var err error
			if team, err = createMigratedTeam(tx, app.OrgID, "Application "+app.Name, "Créée pour l'application « "+app.Name+" », qui tenait plusieurs rôles donnant accès à des modèles.", union); err != nil {
				return nil, err
			}
			created = append(created, team)
		}
		if err := tx.Model(&Application{}).Where("id = ?", appID).Update("team_id", team).Error; err != nil {
			return nil, errors.WithStack(err)
		}
	}
	if len(skipped) > 0 {
		slog.WarnContext(tx.Statement.Context, "applications holding a deleted role bound to no team",
			slog.Any("applications", skipped))
	}
	return created, nil
}

// logOrphanRoleGrants logs the role grants left behind by a deleted role: they
// matched nothing and are dropped with role_models.
func logOrphanRoleGrants(tx *gorm.DB, roleIDs []string, roles []Role, grants []legacyRoleModel) {
	found := make(map[string]bool, len(roles))
	for _, role := range roles {
		found[role.ID] = true
	}
	orphans := 0
	var missing []string
	for _, id := range roleIDs {
		if found[id] {
			continue
		}
		missing = append(missing, id)
		for _, g := range grants {
			if g.RoleID == id {
				orphans++
			}
		}
	}
	if orphans > 0 {
		slog.InfoContext(tx.Statement.Context, "role model grants of a deleted role dropped",
			slog.Int("grants", orphans), slog.Any("roles", missing))
	}
}

// createMigratedTeam creates a team of the organization with the given grants
// and a slug derived from name, free in the organization and clashing with no
// model name.
func createMigratedTeam(tx *gorm.DB, orgID, name, description string, grants []legacyRoleModel) (string, error) {
	base := model.Slugify(name, "equipe")
	slug := base
	for i := 2; ; i++ {
		taken, err := teamSlugTaken(tx, orgID, slug)
		if err != nil {
			return "", err
		}
		if !taken {
			break
		}
		suffix := "-" + strconv.Itoa(i)
		slug = strings.TrimRight(base[:min(len(base), model.MaxSlugLength-len(suffix))], "-") + suffix
	}
	now := time.Now()
	team := &Team{
		ID: xid.New().String(), CreatedAt: now, UpdatedAt: now,
		OrgID: orgID, Slug: slug, Name: name, Description: description,
	}
	if err := tx.Omit("Org", "ModelGrants", "Middlewares").Create(team).Error; err != nil {
		return "", errors.WithStack(err)
	}
	seen := map[[2]string]bool{}
	for _, g := range grants {
		key := [2]string{g.ModelID, g.ModelKind}
		if seen[key] {
			continue
		}
		seen[key] = true
		if err := tx.Create(&TeamModelGrant{TeamID: team.ID, ModelID: g.ModelID, ModelKind: g.ModelKind}).Error; err != nil {
			return "", errors.WithStack(err)
		}
	}
	return team.ID, nil
}

// teamSlugTaken reports whether slug names a team of the organization, or
// would make a model name of the organization ambiguous.
func teamSlugTaken(tx *gorm.DB, orgID, slug string) (bool, error) {
	var count int64
	if err := tx.Model(&Team{}).Where("org_id = ? AND slug = ?", orgID, slug).Count(&count).Error; err != nil {
		return false, errors.WithStack(err)
	}
	if count > 0 {
		return true, nil
	}
	return modelNamePrefixTaken(tx, orgID, slug)
}

// modelNamePrefixTaken reports whether a real or virtual model of the
// organization has a local name starting with "<slug>/". A slug is a DNS
// label, which holds no LIKE wildcard.
func modelNamePrefixTaken(db *gorm.DB, orgID, slug string) (bool, error) {
	var count int64
	if err := db.Model(&LLMModel{}).Where("org_id = ? AND proxy_name LIKE ?", orgID, slug+"/%").Count(&count).Error; err != nil {
		return false, errors.WithStack(err)
	}
	if count > 0 {
		return true, nil
	}
	if err := db.Model(&VirtualModel{}).Where("org_id = ? AND name LIKE ?", orgID, slug+"/%").Count(&count).Error; err != nil {
		return false, errors.WithStack(err)
	}
	return count > 0, nil
}

// republishProjections publishes again the projections whose representation
// changed with teams, and those of the teams just created.
func republishProjections(tx *gorm.DB, teams []string) error {
	before := map[mutationKey][]byte{}
	var keys []mutationKey
	add := func(kind, id string) error {
		key := mutationKey{kind, id}
		raw, err := mutationSnapshot(tx, key)
		if err != nil {
			return err
		}
		before[key] = raw
		keys = append(keys, key)
		return nil
	}
	for _, kind := range []string{"role", "application", "quota"} {
		var ids []string
		if err := tx.Table(resourceTables[kind]).Order("id").Pluck("id", &ids).Error; err != nil {
			return errors.WithStack(err)
		}
		for _, id := range ids {
			if err := add(kind, id); err != nil {
				return err
			}
		}
	}
	for _, id := range slices.Sorted(slices.Values(teams)) {
		// Created in this transaction: there was nothing before.
		before[mutationKey{"team", id}] = []byte("null")
		keys = append(keys, mutationKey{"team", id})
	}
	return publishProjections(tx.Statement.Context, tx, nil, before, keys)
}
