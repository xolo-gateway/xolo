package gorm

import (
	"context"
	"slices"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CreateTeam implements port.TeamStore.
func (s *Store) CreateTeam(ctx context.Context, team model.Team) error {
	return s.recorded(ctx, tracking("team", string(team.ID())), func(ctx context.Context, db *gorm.DB) error {
		if err := db.Omit("Org").Create(fromTeam(team)).Error; err != nil {
			if isUniqueViolation(err, "org_id", "slug") {
				return errors.Wrapf(port.ErrAlreadyExists, "team %q already exists in this organization", team.Slug())
			}
			return errors.WithStack(err)
		}
		return nil
	})
}

// SaveTeam implements port.TeamStore.
func (s *Store) SaveTeam(ctx context.Context, team model.Team) error {
	return s.recorded(ctx, tracking("team", string(team.ID())), func(ctx context.Context, db *gorm.DB) error {
		row := fromTeam(team)
		if err := lockTeamMiddlewares(db, row.ID); err != nil {
			return err
		}
		result := db.Model(&Team{}).Where("id = ? AND org_id = ?", row.ID, row.OrgID).Updates(map[string]any{
			"slug":                row.Slug,
			"name":                row.Name,
			"description":         row.Description,
			"share_quota_equally": row.ShareQuotaEqually,
			"updated_at":          row.UpdatedAt,
		})
		if result.Error != nil {
			if isUniqueViolation(result.Error, "org_id", "slug") {
				return errors.Wrapf(port.ErrAlreadyExists, "team %q already exists in this organization", row.Slug)
			}
			return errors.WithStack(result.Error)
		}
		if result.RowsAffected == 0 {
			return errors.WithStack(port.ErrNotFound)
		}
		var attached []string
		if err := db.Model(&TeamMiddleware{}).Where("team_id = ?", row.ID).Pluck("middleware_id", &attached).Error; err != nil {
			return errors.WithStack(err)
		}
		var detached []string
		for _, id := range attached {
			if !slices.ContainsFunc(row.Middlewares, func(m *TeamMiddleware) bool { return m.MiddlewareID == id }) {
				detached = append(detached, id)
			}
		}
		if len(detached) > 0 {
			if err := refuseSoleHeldMiddlewares(db, row.ID, detached); err != nil {
				return err
			}
		}
		if err := db.Where("team_id = ?", row.ID).Delete(&TeamModelGrant{}).Error; err != nil {
			return errors.WithStack(err)
		}
		if len(row.ModelGrants) > 0 {
			if err := db.Create(row.ModelGrants).Error; err != nil {
				return errors.WithStack(err)
			}
		}
		if err := db.Where("team_id = ?", row.ID).Delete(&TeamMiddleware{}).Error; err != nil {
			return errors.WithStack(err)
		}
		if len(row.Middlewares) > 0 {
			if err := db.Create(row.Middlewares).Error; err != nil {
				return errors.WithStack(err)
			}
		}
		return nil
	})
}

// DeleteTeam implements port.TeamStore.
func (s *Store) DeleteTeam(ctx context.Context, id model.TeamID) error {
	return s.recorded(ctx, trackingTree("team", string(id)), func(ctx context.Context, db *gorm.DB) error {
		if err := lockTeamMiddlewares(db, string(id)); err != nil {
			return err
		}
		var count int64
		if err := db.Model(&Team{}).Where("id = ?", string(id)).Count(&count).Error; err != nil {
			return errors.WithStack(err)
		}
		if count == 0 {
			return errors.WithStack(port.ErrNotFound)
		}
		if err := db.Model(&Application{}).Where("team_id = ?", string(id)).Count(&count).Error; err != nil {
			return errors.WithStack(err)
		}
		if count > 0 {
			return errors.Wrapf(port.ErrNotAllowed, "%d application(s) are bound to the team", count)
		}
		if err := refuseSoleHeldMiddlewares(db, string(id), nil); err != nil {
			return err
		}
		return deleteTeamsWithin(db, db.Model(&Team{}).Select("id").Where("id = ?", string(id)))
	})
}

// refuseSoleHeldMiddlewares returns port.ErrTeamHasMiddlewares when the team
// is the only one holding one of the middlewares ids, all of its middlewares
// when ids is nil: detached from every team, a middleware runs for the whole
// organization. A middleware deleted since keeps its attachment rows and runs
// nowhere, so it does not count.
func refuseSoleHeldMiddlewares(db *gorm.DB, teamID string, ids []string) error {
	others := db.Model(&TeamMiddleware{}).Select("middleware_id").Where("team_id <> ?", teamID)
	existing := db.Model(&Middleware{}).Select("id")
	q := db.Model(&TeamMiddleware{}).
		Where("team_id = ? AND middleware_id NOT IN (?) AND middleware_id IN (?)", teamID, others, existing)
	if ids != nil {
		q = q.Where("middleware_id IN ?", ids)
	}
	var count int64
	if err := q.Count(&count).Error; err != nil {
		return errors.WithStack(err)
	}
	if count > 0 {
		return errors.Wrapf(port.ErrTeamHasMiddlewares, "%d middleware(s)", count)
	}
	return nil
}

// lockTeamMiddlewares locks the middlewares attached to the team, in id order,
// until the end of the transaction. Every write that detaches a middleware
// from a team takes it first, so that refuseSoleHeldMiddlewares sees the
// attachments a concurrent detach removed: without it, two teams sharing a
// middleware detaching it at once would both pass, and leave it running for the
// whole organization.
//
// SQLite needs no lock: the server holds a single connection, so its
// transactions run one after the other, and a transaction of another process
// that read before a concurrent write fails with BUSY_SNAPSHOT when it writes,
// then replays from the start (isRetryableError), check included.
func lockTeamMiddlewares(db *gorm.DB, teamID string) error {
	if !isPostgres(db) {
		return nil
	}
	var ids []string
	err := db.Model(&Middleware{}).
		Where("id IN (?)", db.Model(&TeamMiddleware{}).Select("middleware_id").Where("team_id = ?", teamID)).
		Order("id").Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).
		Pluck("id", &ids).Error
	return errors.WithStack(err)
}

// deleteTeamsWithin removes the teams selected by teamIDs, with their members,
// grants, middlewares and budgets.
func deleteTeamsWithin(db *gorm.DB, teamIDs *gorm.DB) error {
	var ids []string
	if err := teamIDs.Pluck("id", &ids).Error; err != nil {
		return errors.WithStack(err)
	}
	if len(ids) == 0 {
		return nil
	}
	for _, m := range []any{&TeamMemberRole{}, &TeamMember{}, &TeamModelGrant{}, &TeamMiddleware{}} {
		if err := db.Where("team_id IN ?", ids).Delete(m).Error; err != nil {
			return errors.WithStack(err)
		}
	}
	for _, id := range ids {
		if err := db.Where("(scope = ? AND scope_id = ?) OR (scope = ? AND scope_id LIKE ?)",
			string(model.QuotaScopeTeam), id, string(model.QuotaScopeTeamMember), id+":%").Delete(&Quota{}).Error; err != nil {
			return errors.WithStack(err)
		}
	}
	return errors.WithStack(db.Where("id IN ?", ids).Delete(&Team{}).Error)
}

// GetTeamByID implements port.TeamStore.
func (s *Store) GetTeamByID(ctx context.Context, id model.TeamID) (model.Team, error) {
	return s.getTeam(ctx, func(db *gorm.DB) *gorm.DB { return db.Where("id = ?", string(id)) })
}

// GetTeamBySlug implements port.TeamStore.
func (s *Store) GetTeamBySlug(ctx context.Context, orgID model.OrgID, slug string) (model.Team, error) {
	return s.getTeam(ctx, func(db *gorm.DB) *gorm.DB {
		return db.Where("org_id = ? AND slug = ?", string(orgID), slug)
	})
}

func (s *Store) getTeam(ctx context.Context, scope func(*gorm.DB) *gorm.DB) (model.Team, error) {
	var team Team
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		if err := scope(db.Preload("ModelGrants").Preload("Middlewares")).First(&team).Error; err != nil {
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
	return &wrappedTeam{&team}, nil
}

// ListOrgTeams implements port.TeamStore.
func (s *Store) ListOrgTeams(ctx context.Context, orgID model.OrgID) ([]model.Team, error) {
	var teams []*Team
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Preload("ModelGrants").Preload("Middlewares").
			Where("org_id = ?", string(orgID)).Order("name ASC, slug ASC").Find(&teams).Error)
	})
	if err != nil {
		return nil, err
	}
	result := make([]model.Team, 0, len(teams))
	for _, t := range teams {
		result = append(result, &wrappedTeam{t})
	}
	return result, nil
}

// ListTeamMembers implements port.TeamStore.
func (s *Store) ListTeamMembers(ctx context.Context, id model.TeamID) ([]model.TeamMember, error) {
	return s.queryTeamMembers(ctx, true, func(db *gorm.DB) *gorm.DB {
		return db.Where("team_members.team_id = ?", string(id))
	})
}

// ListMembershipTeams implements port.TeamStore.
func (s *Store) ListMembershipTeams(ctx context.Context, membershipID model.MembershipID) ([]model.TeamMember, error) {
	return s.queryTeamMembers(ctx, false, func(db *gorm.DB) *gorm.DB {
		return db.Where("team_members.membership_id = ?", string(membershipID))
	})
}

func (s *Store) queryTeamMembers(ctx context.Context, withUsers bool, scope func(*gorm.DB) *gorm.DB) ([]model.TeamMember, error) {
	var rows []*teamMemberRow
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		if err := scope(db.Table("team_members").
			Select("team_members.team_id, team_members.membership_id, memberships.user_id, team_members.created_at").
			Joins("JOIN memberships ON memberships.id = team_members.membership_id")).
			Order("team_members.team_id, team_members.created_at, team_members.membership_id").
			Scan(&rows).Error; err != nil {
			return errors.WithStack(err)
		}
		if len(rows) == 0 {
			return nil
		}
		teamIDs := make([]string, 0, len(rows))
		membershipIDs := make([]string, 0, len(rows))
		for _, r := range rows {
			teamIDs = append(teamIDs, r.TeamID)
			membershipIDs = append(membershipIDs, r.MembershipID)
		}
		// Both filters bound the read to the rows found; the pairs are matched
		// below.
		var roles []TeamMemberRole
		if err := db.Where("team_id IN ? AND membership_id IN ?",
			slices.Compact(slices.Sorted(slices.Values(teamIDs))), slices.Compact(slices.Sorted(slices.Values(membershipIDs)))).
			Order("role_id").Find(&roles).Error; err != nil {
			return errors.WithStack(err)
		}
		for _, r := range rows {
			r.roleIDs = []model.RoleID{}
			for _, role := range roles {
				if role.TeamID == r.TeamID && role.MembershipID == r.MembershipID {
					r.roleIDs = append(r.roleIDs, model.RoleID(role.RoleID))
				}
			}
		}
		if !withUsers {
			return nil
		}
		userIDs := make([]string, 0, len(rows))
		for _, r := range rows {
			userIDs = append(userIDs, r.UserID)
		}
		var users []*User
		if err := db.Where("id IN ?", userIDs).Find(&users).Error; err != nil {
			return errors.WithStack(err)
		}
		for _, r := range rows {
			for _, u := range users {
				if u.ID == r.UserID {
					r.user = u
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	result := make([]model.TeamMember, 0, len(rows))
	for _, r := range rows {
		result = append(result, &wrappedTeamMember{r})
	}
	return result, nil
}

// SetTeamMember implements port.TeamStore.
func (s *Store) SetTeamMember(ctx context.Context, id model.TeamID, membershipID model.MembershipID, roleIDs []model.RoleID) error {
	return s.recorded(ctx, tracking("team", string(id)), func(ctx context.Context, db *gorm.DB) error {
		var team Team
		if err := db.Select("id, org_id").First(&team, "id = ?", string(id)).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.WithStack(port.ErrNotFound)
			}
			return errors.WithStack(err)
		}
		var count int64
		if err := db.Model(&Membership{}).Where("id = ? AND org_id = ?", string(membershipID), team.OrgID).Count(&count).Error; err != nil {
			return errors.WithStack(err)
		}
		if count == 0 {
			return errors.Wrap(port.ErrParentNotFound, "membership not found in the organization of the team")
		}
		if len(roleIDs) > 0 {
			ids := make([]string, 0, len(roleIDs))
			for _, roleID := range roleIDs {
				ids = append(ids, string(roleID))
			}
			ids = slices.Compact(slices.Sorted(slices.Values(ids)))
			if err := db.Model(&Role{}).Where("id IN ? AND org_id = ?", ids, team.OrgID).Count(&count).Error; err != nil {
				return errors.WithStack(err)
			}
			if int(count) != len(ids) {
				return errors.Wrap(port.ErrParentNotFound, "role not found in the organization of the team")
			}
			// Its codes would be cut to the team-scopable ones without a word.
			if err := db.Model(&Role{}).Where("id IN ? AND builtin = ?", ids, true).Count(&count).Error; err != nil {
				return errors.WithStack(err)
			}
			if count > 0 {
				return errors.Wrap(port.ErrInvalid, "a builtin role cannot be held on a team")
			}
		}
		if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&TeamMember{TeamID: string(id), MembershipID: string(membershipID)}).Error; err != nil {
			return errors.WithStack(err)
		}
		if err := db.Where("team_id = ? AND membership_id = ?", string(id), string(membershipID)).Delete(&TeamMemberRole{}).Error; err != nil {
			return errors.WithStack(err)
		}
		for _, roleID := range roleIDs {
			if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&TeamMemberRole{
				TeamID: string(id), MembershipID: string(membershipID), RoleID: string(roleID),
			}).Error; err != nil {
				return errors.WithStack(err)
			}
		}
		return nil
	})
}

// RemoveTeamMember implements port.TeamStore. The member's budget inside the
// team goes with it.
func (s *Store) RemoveTeamMember(ctx context.Context, id model.TeamID, membershipID model.MembershipID) error {
	track := func(r *mutationRecorder) error {
		if err := r.track("team", string(id)); err != nil {
			return err
		}
		return r.trackTeamMemberQuotas(string(id), string(membershipID))
	}
	return s.recorded(ctx, track, func(ctx context.Context, db *gorm.DB) error {
		var userID string
		if err := db.Model(&Membership{}).Where("id = ?", string(membershipID)).Pluck("user_id", &userID).Error; err != nil {
			return errors.WithStack(err)
		}
		if err := db.Where("team_id = ? AND membership_id = ?", string(id), string(membershipID)).Delete(&TeamMemberRole{}).Error; err != nil {
			return errors.WithStack(err)
		}
		result := db.Where("team_id = ? AND membership_id = ?", string(id), string(membershipID)).Delete(&TeamMember{})
		if result.Error != nil {
			return errors.WithStack(result.Error)
		}
		if result.RowsAffected == 0 {
			return errors.WithStack(port.ErrNotFound)
		}
		if userID != "" {
			if err := db.Where("scope = ? AND scope_id = ?", string(model.QuotaScopeTeamMember),
				model.TeamMemberScopeID(id, model.UserID(userID))).Delete(&Quota{}).Error; err != nil {
				return errors.WithStack(err)
			}
		}
		return nil
	})
}

// deleteTeamMembershipsWithin removes the team memberships of the selected
// organization memberships, with their team roles and budgets.
func deleteTeamMembershipsWithin(db *gorm.DB, membershipIDs *gorm.DB) error {
	var rows []struct {
		TeamID string
		UserID string
	}
	if err := db.Table("team_members").Select("team_members.team_id, memberships.user_id").
		Joins("JOIN memberships ON memberships.id = team_members.membership_id").
		Where("team_members.membership_id IN (?)", membershipIDs).Scan(&rows).Error; err != nil {
		return errors.WithStack(err)
	}
	for _, row := range rows {
		if err := db.Where("scope = ? AND scope_id = ?", string(model.QuotaScopeTeamMember),
			model.TeamMemberScopeID(model.TeamID(row.TeamID), model.UserID(row.UserID))).Delete(&Quota{}).Error; err != nil {
			return errors.WithStack(err)
		}
	}
	if err := db.Where("membership_id IN (?)", membershipIDs).Delete(&TeamMemberRole{}).Error; err != nil {
		return errors.WithStack(err)
	}
	return errors.WithStack(db.Where("membership_id IN (?)", membershipIDs).Delete(&TeamMember{}).Error)
}

// ModelNamePrefixTaken implements port.TeamStore.
func (s *Store) ModelNamePrefixTaken(ctx context.Context, orgID model.OrgID, slug string) (bool, error) {
	var taken bool
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		var err error
		taken, err = modelNamePrefixTaken(db, string(orgID), slug)
		return err
	})
	return taken, err
}

var _ port.TeamStore = &Store{}
