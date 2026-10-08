package gorm

import (
	"context"
	"errors"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// WithInvitationTransaction retries the entire operation, including validation,
// on SQLite snapshot conflicts and PostgreSQL deadlocks. The bound store never
// migrates, retries a statement, or starts a nested/default write transaction.
func (s *Store) WithInvitationTransaction(ctx context.Context, fn func(port.InvitationTx) error) error {
	ctx = model.EnsureActor(ctx)
	db, err := s.getDatabase(ctx)
	if err != nil {
		return err
	}
	return retryTransaction(ctx, func() error {
		return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			tx = tx.Session(&gorm.Session{SkipDefaultTransaction: true})
			recorder := newMutationRecorder(tx, s.ownership)
			bound := &Store{
				getDatabase:      func(context.Context) (*gorm.DB, error) { return tx, nil },
				transactionBound: true,
				recorder:         recorder,
				identityIssuers:  s.identityIssuers,
				ownership:        s.ownership,
			}
			if err := fn(&invitationTx{Store: bound, db: tx}); err != nil {
				return err
			}
			return recorder.flush(ctx)
		})
	})
}

// invitationTx binds a Store to the transaction opened by
// WithInvitationTransaction. Every method reached through port.InvitationTx,
// whether defined here or promoted from the bound Store, must run on that
// transaction and must not open its own: the bound Store's withRetry calls its
// function once, without a transaction of its own and without retrying.
// Retries belong to the outer loop, which replays the whole callback.
type invitationTx struct {
	*Store
	db *gorm.DB
}

func (tx *invitationTx) locked(ctx context.Context, strength string) *gorm.DB {
	db := tx.db.WithContext(ctx)
	if isPostgres(db) {
		db = db.Clauses(clause.Locking{Strength: strength})
	}
	return db
}

func invitationReadError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return port.ErrNotFound
	}
	return err
}

func (tx *invitationTx) GetInviteByID(ctx context.Context, id model.InviteTokenID) (model.InviteToken, error) {
	var invite InviteToken
	if err := tx.locked(ctx, "UPDATE").First(&invite, "id = ?", string(id)).Error; err != nil {
		return nil, invitationReadError(err)
	}
	return &wrappedInviteToken{&invite}, nil
}

func (tx *invitationTx) GetOrgByID(ctx context.Context, id model.OrgID) (model.Organization, error) {
	var org Organization
	if err := tx.locked(ctx, "SHARE").First(&org, "id = ?", string(id)).Error; err != nil {
		return nil, invitationReadError(err)
	}
	return &wrappedOrganization{&org}, nil
}

func (tx *invitationTx) GetUserByID(ctx context.Context, id model.UserID) (model.User, error) {
	var user User
	if err := tx.locked(ctx, "SHARE").First(&user, "id = ?", string(id)).Error; err != nil {
		return nil, invitationReadError(err)
	}
	return &wrappedUser{&user}, nil
}

func (tx *invitationTx) GetRoleByID(ctx context.Context, id model.RoleID) (model.Role, error) {
	var role Role
	if err := tx.locked(ctx, "SHARE").First(&role, "id = ?", string(id)).Error; err != nil {
		return nil, invitationReadError(err)
	}
	return &wrappedRole{&role}, nil
}

func (tx *invitationTx) ListOrgRoles(ctx context.Context, orgID model.OrgID) ([]model.Role, error) {
	var roles []*Role
	if err := tx.locked(ctx, "SHARE").Where("org_id = ?", string(orgID)).Order("id").Find(&roles).Error; err != nil {
		return nil, err
	}
	result := make([]model.Role, 0, len(roles))
	for _, role := range roles {
		result = append(result, &wrappedRole{role})
	}
	return result, nil
}

func (tx *invitationTx) InsertInvitationMember(ctx context.Context, membership model.Membership) (bool, error) {
	if err := tx.recorder.track("membership", string(membership.ID())); err != nil {
		return false, err
	}
	result := tx.db.WithContext(ctx).Omit(clause.Associations).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "org_id"}},
		DoNothing: true,
	}).Create(fromMembership(membership))
	return result.RowsAffected == 1, result.Error
}

var _ port.InvitationTransaction = (*Store)(nil)
var _ port.InvitationTx = (*invitationTx)(nil)

// retryTransaction never waits with a transaction open and only retries whole callbacks.
func retryTransaction(ctx context.Context, operation func() error) error {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := operation()
		if err == nil || !isRetryableError(err) || attempt >= 10 {
			return err
		}
		timer := time.NewTimer(min(10*time.Millisecond<<attempt, 500*time.Millisecond))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
