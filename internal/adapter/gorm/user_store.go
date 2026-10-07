package gorm

import (
	"context"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/crypto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// fromAuthToken converts a model.AuthToken to a GORM AuthToken
func fromAuthToken(t model.AuthToken) *AuthToken {
	var ownerID *string
	if t.Owner() != nil {
		id := string(t.Owner().ID())
		ownerID = &id
	}

	return &AuthToken{
		ID:      string(t.ID()),
		OwnerID: ownerID,
		Label:   t.Label(),
		// Only the hash is persisted: the clear-text key exists once, in the
		// response that follows its creation.
		Value:     crypto.HashToken(t.Value()),
		OrgID:     string(t.OrgID()),
		ExpiresAt: t.ExpiresAt(),
	}
}

// FindOrCreateUser implements port.UserStore.
func (s *Store) FindOrCreateUser(ctx context.Context, tenantID model.TenantID, provider, subject string) (model.User, error) {
	if provider == "" || subject == "" {
		return nil, errors.WithStack(port.ErrInvalid)
	}
	var user model.User
	// The identifier is chosen first so that a creation is recorded; finding
	// an existing user changes nothing.
	id := model.NewUserID()
	err := s.recorded(ctx, tracking("user", string(id)), func(ctx context.Context, db *gorm.DB) error {
		var u User

		err := db.Where("tenant_id = ? AND provider = ? AND subject = ?", string(tenantID), provider, subject).
			Preload("Roles").
			Preload("Preferences").
			First(&u).Error
		if err == nil {
			user = &wrappedUser{&u}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.WithStack(err)
		}

		u = User{
			ID:       string(id),
			TenantID: string(tenantID),
			Provider: provider,
			Subject:  subject,
			Active:   true,
		}
		if err := s.assertIdentityAvailable(db, &u); err != nil {
			return err
		}
		if err := db.Omit(clause.Associations).Create(&u).Error; err != nil {
			return errors.WithStack(err)
		}

		user = &wrappedUser{&u}
		return nil
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return user, nil
}

// GetUserByID implements port.UserStore.
func (s *Store) GetUserByID(ctx context.Context, userID model.UserID) (model.User, error) {
	var user User

	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		if err := db.Preload("Roles").Preload("Preferences").First(&user, "id = ?", string(userID)).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.WithStack(port.ErrNotFound)
			}
			return errors.WithStack(err)
		}
		return nil
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return &wrappedUser{&user}, nil
}

// GetUserByIdentity implements port.UserStore.
func (s *Store) GetUserByIdentity(ctx context.Context, tenantID model.TenantID, provider, subject string) (model.User, error) {
	// An account no sign-in is linked to has neither: it must never answer
	// for an identity.
	if provider == "" || subject == "" {
		return nil, errors.WithStack(port.ErrNotFound)
	}

	var user User

	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		err := db.Preload("Roles").Preload("Preferences").
			Where("tenant_id = ? AND provider = ? AND subject = ?", string(tenantID), provider, subject).
			First(&user).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.WithStack(port.ErrNotFound)
			}
			return errors.WithStack(err)
		}
		return nil
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return &wrappedUser{&user}, nil
}

// GetUserByDeclaredIdentity implements port.UserStore.
func (s *Store) GetUserByDeclaredIdentity(ctx context.Context, tenantID model.TenantID, identity model.Identity) (model.User, error) {
	var user model.User
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		var err error
		user, err = getUserByDeclaredIdentity(db, tenantID, identity)
		return err
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return user, nil
}

func getUserByDeclaredIdentity(db *gorm.DB, tenantID model.TenantID, identity model.Identity) (model.User, error) {
	if identity.Issuer == "" || identity.Subject == "" {
		return nil, errors.WithStack(port.ErrNotFound)
	}
	var user User
	err := db.Preload("Roles").Preload("Preferences").
		Where("tenant_id = ? AND identity_issuer = ? AND identity_subject = ?", string(tenantID), identity.Issuer, identity.Subject).
		First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errors.WithStack(port.ErrNotFound)
	}
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return &wrappedUser{&user}, nil
}

// FindUsersByEmail implements port.UserStore.
func (s *Store) FindUsersByEmail(ctx context.Context, tenantID model.TenantID, email string, limit int) ([]model.User, error) {
	var users []model.User
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		var err error
		users, err = findUsersByEmail(db, tenantID, email, limit)
		return err
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return users, nil
}

// findUsersByEmail compares normalized emails without rewriting the stored
// ones. SQL narrows the candidates, Go decides: printable ASCII values are
// matched by LOWER(TRIM()), every other value is normalized in Go. The
// candidates are read as bare (id, email) pairs, which the other values may
// make numerous; only the accounts that match are loaded, and locked on a
// transaction that locks its reads.
func findUsersByEmail(db *gorm.DB, tenantID model.TenantID, email string, limit int) ([]model.User, error) {
	email = model.NormalizeEmail(email)
	if email == "" || limit <= 0 {
		return nil, nil
	}
	other := outsidePrintableASCII(db, "email")
	query := db.Session(&gorm.Session{NewDB: true}).Model(&User{}).
		Where("tenant_id = ? AND email <> ''", string(tenantID))
	if isPrintableASCII(email) {
		query = query.Where("(LOWER(TRIM(email)) = ? OR "+other+")", email)
	} else {
		query = query.Where(other)
	}
	var candidates []struct{ ID, Email string }
	if err := query.Select("id", "email").Order("id").Find(&candidates).Error; err != nil {
		return nil, errors.WithStack(err)
	}
	var ids []string
	for _, candidate := range candidates {
		if model.NormalizeEmail(candidate.Email) == email {
			ids = append(ids, candidate.ID)
			if len(ids) == limit {
				break
			}
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}

	var rows []User
	if err := db.Preload("Roles").Preload("Preferences").Where("id IN ?", ids).Order("id").Find(&rows).Error; err != nil {
		return nil, errors.WithStack(err)
	}
	users := make([]model.User, 0, len(rows))
	for i := range rows {
		users = append(users, &wrappedUser{&rows[i]})
	}
	return users, nil
}

func isPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// assertIdentityAvailable keeps one identity designating at most one account
// of a tenant, through a declaration or a sign-in link alike: a link is the
// declared identity when its provider proves the declared issuer. A declared
// identity must match the link of its own account. Only a change of link or
// declaration is checked, so accounts written before this rule keep working.
func (s *Store) assertIdentityAvailable(db *gorm.DB, u *User) error {
	linked := u.Provider != "" && u.Subject != ""
	if !linked && u.IdentityIssuer == "" {
		return nil
	}
	issuers := s.issuers()
	linkIssuer, proven := "", false
	if linked {
		linkIssuer, proven = issuers.Issuer(u.Provider)
	}
	if u.IdentityIssuer != "" && linked && (!proven || linkIssuer != u.IdentityIssuer || u.Subject != u.IdentitySubject) {
		return errors.Wrap(port.ErrAlreadyExists, "the declared identity differs from the sign-in linked to the user")
	}
	issuer, subject := u.IdentityIssuer, u.IdentitySubject
	if issuer == "" && proven {
		issuer, subject = linkIssuer, u.Subject
	}
	if issuer == "" {
		return nil
	}

	var stored []User
	if err := db.Select("provider", "subject", "identity_issuer", "identity_subject").
		Where("id = ?", u.ID).Limit(1).Find(&stored).Error; err != nil {
		return errors.WithStack(err)
	}
	if len(stored) == 1 && stored[0].Provider == u.Provider && stored[0].Subject == u.Subject &&
		stored[0].IdentityIssuer == u.IdentityIssuer && stored[0].IdentitySubject == u.IdentitySubject {
		return nil
	}

	where, args := "identity_issuer = ? AND identity_subject = ?", []any{issuer, subject}
	if providers := issuers.Providers(issuer); len(providers) != 0 {
		where = "(" + where + ") OR (provider IN ? AND subject = ?)"
		args = append(args, providers, subject)
	}
	var others []string
	if err := db.Model(&User{}).Where("tenant_id = ? AND id <> ?", u.TenantID, u.ID).
		Where("("+where+")", args...).Limit(1).Pluck("id", &others).Error; err != nil {
		return errors.WithStack(err)
	}
	if len(others) != 0 {
		return errors.Wrap(port.ErrAlreadyExists, "the identity is already bound to another user")
	}
	return nil
}

// SaveUser implements port.UserStore.
func (s *Store) SaveUser(ctx context.Context, user model.User) error {
	if _, err := model.ParseUserID(string(user.ID())); err != nil {
		return port.ErrInvalid
	}
	err := s.recorded(ctx, tracking("user", string(user.ID())), func(ctx context.Context, db *gorm.DB) error {
		gormUser := fromUser(user)

		if err := s.assertIdentityAvailable(db, gormUser); err != nil {
			return err
		}

		// Use Clauses with OnConflict to handle upsert
		if err := db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			UpdateAll: true,
		}).Omit("Roles", "Preferences").Create(gormUser).Error; err != nil {
			// Matched on the index alone (SQLite names its column, PostgreSQL
			// the index): the key values reported may contain any fragment.
			if isUniqueViolation(err, "users.identity_issuer") || isUniqueViolation(err, declaredIdentityIndex) {
				return errors.Wrap(port.ErrAlreadyExists, "the identity is already declared for another user")
			}
			if isUniqueViolation(err, "users.subject") || isUniqueViolation(err, "idx_users_tenant_identity") {
				return errors.Wrap(port.ErrAlreadyExists, "the sign-in is already linked to another user")
			}
			if isUniqueViolation(err, "users", "email") {
				return errors.Wrapf(port.ErrEmailTaken, "email %q is already used by another user", gormUser.Email)
			}

			return errors.WithStack(err)
		}

		// Handle preferences separately with the correct conflict column
		if gormUser.Preferences != nil {
			gormUser.Preferences.UserID = gormUser.ID
			if err := db.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "user_id"}},
				UpdateAll: true,
			}).Create(gormUser.Preferences).Error; err != nil {
				return errors.WithStack(err)
			}
		}

		newRoles := gormUser.Roles[:]

		if err := db.Model(gormUser).Association("Roles").Clear(); err != nil {
			return errors.WithStack(err)
		}

		for _, r := range newRoles {
			err := db.
				Clauses(clause.OnConflict{
					Columns:   []clause.Column{{Name: "user_id"}, {Name: "role"}},
					DoNothing: true,
				}).
				Omit("User").Save(r).Error
			if err != nil {
				return errors.WithStack(err)
			}
		}

		return nil
	})
	if err != nil {
		return errors.WithStack(err)
	}

	return nil
}

// FindAuthToken implements port.UserStore.
func (s *Store) FindAuthToken(ctx context.Context, token string) (model.AuthToken, error) {
	var authToken AuthToken

	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		if err := db.Preload("Owner").Preload("Application").First(&authToken, "value = ?", crypto.HashToken(token)).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.WithStack(port.ErrNotFound)
			}
			return errors.WithStack(err)
		}
		return nil
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	// Enforce expiry: treat expired tokens as not found
	if authToken.ExpiresAt != nil && time.Now().After(*authToken.ExpiresAt) {
		return nil, errors.WithStack(port.ErrNotFound)
	}

	return &wrappedAuthToken{&authToken}, nil
}

// GetUserAuthTokens implements port.UserStore.
func (s *Store) GetUserAuthTokens(ctx context.Context, userID model.UserID) ([]model.AuthToken, error) {
	var authTokens []*AuthToken

	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		if err := db.Where("owner_id = ?", string(userID)).Find(&authTokens).Error; err != nil {
			return errors.WithStack(err)
		}
		return nil
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	wrappedTokens := make([]model.AuthToken, 0, len(authTokens))
	for _, t := range authTokens {
		wrappedTokens = append(wrappedTokens, &wrappedAuthToken{t})
	}

	return wrappedTokens, nil
}

// CreateAuthToken implements port.UserStore.
func (s *Store) CreateAuthToken(ctx context.Context, token model.AuthToken) error {
	err := s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		gormToken := fromAuthToken(token)

		if err := db.Create(gormToken).Error; err != nil {
			return errors.WithStack(err)
		}

		return nil
	})
	if err != nil {
		return errors.WithStack(err)
	}

	return nil
}

// DeleteAuthToken implements port.UserStore.
func (s *Store) DeleteAuthToken(ctx context.Context, tokenID model.AuthTokenID) error {
	err := s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		result := db.Delete(&AuthToken{}, "id = ?", string(tokenID))
		if result.Error != nil {
			return errors.WithStack(result.Error)
		}

		if result.RowsAffected == 0 {
			return errors.WithStack(port.ErrNotFound)
		}

		return nil
	})
	if err != nil {
		return errors.WithStack(err)
	}

	return nil
}

// DeleteUser implements port.UserStore.
func (s *Store) DeleteUser(ctx context.Context, userID model.UserID) error {
	err := s.recorded(ctx, trackingTree("user", string(userID)), func(ctx context.Context, db *gorm.DB) error {
		deleted, err := deleteUsersWithin(db, []string{string(userID)})
		if err != nil {
			return err
		}

		if deleted == 0 {
			return errors.WithStack(port.ErrNotFound)
		}

		return nil
	})
	if err != nil {
		return errors.WithStack(err)
	}

	return nil
}

// deleteUsersWithin removes the given users and every row keyed on them, and
// returns the number of users actually deleted. memberships and
// membership_roles have no database-level cascade, so they must go first or the
// user deletion fails on a foreign key constraint. Personal alerts only make
// sense for their owner and follow them; org alerts, usage records and events
// stay with the organization. Any new user-scoped table must be added here.
func deleteUsersWithin(db *gorm.DB, userIDs []string) (int64, error) {
	if len(userIDs) == 0 {
		return 0, nil
	}

	membershipIDs := db.Model(&Membership{}).Select("id").Where("user_id IN ?", userIDs)
	if err := db.Where("membership_id IN (?)", membershipIDs).Delete(&MembershipRole{}).Error; err != nil {
		return 0, errors.WithStack(err)
	}
	if err := db.Where("user_id IN ?", userIDs).Delete(&Membership{}).Error; err != nil {
		return 0, errors.WithStack(err)
	}

	personalAlertIDs := db.Model(&Alert{}).Select("id").Where("scope = ? AND owner_id IN ?", string(model.AlertScopePersonal), userIDs)
	if err := db.Where("alert_id IN (?)", personalAlertIDs).Delete(&AlertIncident{}).Error; err != nil {
		return 0, errors.WithStack(err)
	}
	if err := db.Where("scope = ? AND owner_id IN ?", string(model.AlertScopePersonal), userIDs).Delete(&Alert{}).Error; err != nil {
		return 0, errors.WithStack(err)
	}

	userScoped := []any{
		&UserRole{},
		&UserPreferences{},
		&PersonalVirtualModel{},
	}
	for _, m := range userScoped {
		if err := db.Where("user_id IN ?", userIDs).Delete(m).Error; err != nil {
			return 0, errors.WithStack(err)
		}
	}

	if err := db.Where("owner_id IN ?", userIDs).Delete(&AuthToken{}).Error; err != nil {
		return 0, errors.WithStack(err)
	}
	if err := db.Where("scope = ? AND scope_id IN ?", string(model.QuotaScopeUser), userIDs).Delete(&Quota{}).Error; err != nil {
		return 0, errors.WithStack(err)
	}

	result := db.Where("id IN ?", userIDs).Delete(&User{})
	if result.Error != nil {
		return 0, errors.WithStack(result.Error)
	}

	return result.RowsAffected, nil
}

// applyUserSearch restricts a user query to the rows whose display name, email
// or subject contains term.
//
// LIKE is case-insensitive in SQLite for ASCII, which covers e-mail addresses
// and subjects; display names are lowered on both sides so accented names match
// too. `%` and `_` are escaped so a search for "jean_dupont" does not turn the
// underscore into a wildcard.
func applyUserSearch(query *gorm.DB, term string) *gorm.DB {
	term = strings.TrimSpace(term)
	if term == "" {
		return query
	}

	escaper := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	pattern := "%" + escaper.Replace(strings.ToLower(term)) + "%"

	return query.Where(
		`(LOWER(display_name) LIKE ? ESCAPE '\' OR LOWER(email) LIKE ? ESCAPE '\' OR LOWER(subject) LIKE ? ESCAPE '\')`,
		pattern, pattern, pattern,
	)
}

// applyUserTenant restricts a user query to a single tenant. The column is
// qualified because the role filter joins user_roles.
func applyUserTenant(query *gorm.DB, tenantID *model.TenantID) *gorm.DB {
	if tenantID == nil {
		return query
	}
	return query.Where("users.tenant_id = ?", string(*tenantID))
}

// CountUsers implements port.UserStore.
func (s *Store) CountUsers(ctx context.Context, opts port.QueryUsersOptions) (int64, error) {
	var count int64

	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		query := db.Model(&User{})

		if len(opts.Roles) > 0 {
			query = query.Joins("JOIN user_roles ON users.id = user_roles.user_id").
				Where("user_roles.role IN ?", opts.Roles).
				Distinct()
		}

		if opts.Active != nil {
			query = query.Where("active = ?", *opts.Active)
		}

		query = applyUserTenant(query, opts.TenantID)
		query = applyUserSearch(query, opts.Search)

		return errors.WithStack(query.Count(&count).Error)
	})
	if err != nil {
		return 0, errors.WithStack(err)
	}

	return count, nil
}

// QueryUsers implements port.UserStore.
func (s *Store) QueryUsers(ctx context.Context, opts port.QueryUsersOptions) ([]model.User, error) {
	var users []*User

	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		query := db.Model(&User{}).Preload("Roles")

		// Apply role filtering if specified
		if len(opts.Roles) > 0 {
			// Join with user_roles table to filter by roles
			query = query.Joins("JOIN user_roles ON users.id = user_roles.user_id").
				Where("user_roles.role IN ?", opts.Roles).
				Distinct()
		}

		// Apply active/inactive filtering if specified
		if opts.Active != nil {
			query = query.Where("active = ?", *opts.Active)
		}

		query = applyUserTenant(query, opts.TenantID)
		query = applyUserSearch(query, opts.Search)

		// Apply pagination
		if opts.Page != nil {
			limit := 10
			if opts.Limit != nil {
				limit = *opts.Limit
			}
			query = query.Offset(*opts.Page * limit)
		}

		if opts.Limit != nil {
			query = query.Limit(*opts.Limit)
		}

		// Order by display name for consistent results
		query = query.Order("display_name ASC")

		if err := query.Find(&users).Error; err != nil {
			return errors.WithStack(err)
		}

		return nil
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	wrappedUsers := make([]model.User, 0, len(users))
	for _, u := range users {
		wrappedUsers = append(wrappedUsers, &wrappedUser{u})
	}

	return wrappedUsers, nil
}
