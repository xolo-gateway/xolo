package gorm

import (
	"context"
	"database/sql"
	"errors"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// WithProvisioningTransaction replays validation, writes, audit and
// publication together. PostgreSQL SSI protects cross-row invariants without
// an instance-wide lock; only a transaction changing a projection takes the
// feed lock, at its very end.
func (s *Store) WithProvisioningTransaction(ctx context.Context, fn func(port.ProvisioningTx) error) error {
	if s.transactionBound {
		return errors.New("cannot start provisioning inside another transaction")
	}
	ctx = model.EnsureActor(ctx)
	db, err := s.getDatabase(ctx)
	if err != nil {
		return err
	}
	opts := &sql.TxOptions{}
	if isPostgres(db) {
		opts.Isolation = sql.LevelSerializable
	}
	return retryTransaction(ctx, func() error {
		return db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
			db = db.Session(&gorm.Session{SkipDefaultTransaction: true})
			recorder := newMutationRecorder(db)
			tx := &provisioningTx{
				Store:    &Store{getDatabase: func(context.Context) (*gorm.DB, error) { return db, nil }, transactionBound: true, recorder: recorder},
				db:       db,
				recorder: recorder,
			}
			if err := fn(tx); err != nil {
				return err
			}
			return tx.flushMutations(ctx)
		}, opts)
	})
}

// provisioningTx binds a Store to the provisioning transaction. The bound
// Store tracks every resource it writes into recorder, which audits and
// publishes them before commit.
type provisioningTx struct {
	*Store
	db       *gorm.DB
	recorder *mutationRecorder
}

func (tx *provisioningTx) FindOrCreateUser(ctx context.Context, tenantID model.TenantID, provider, subject string) (model.User, error) {
	u, err := tx.GetUserByIdentity(ctx, tenantID, provider, subject)
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, port.ErrNotFound) {
		return nil, err
	}
	u = model.NewUser(tenantID, provider, subject, "", "", true)
	if err := tx.recorder.track("user", string(u.ID())); err != nil {
		return nil, err
	}
	// A concurrent insertion of this identity replays the serializable transaction.
	if err := tx.db.Omit(clause.Associations).Clauses(clause.OnConflict{
		Columns:     []clause.Column{{Name: "tenant_id"}, {Name: "provider"}, {Name: "subject"}},
		TargetWhere: identityIndexPredicate, DoNothing: true,
	}).Create(fromUser(u)).Error; err != nil {
		return nil, err
	}
	return tx.GetUserByIdentity(ctx, tenantID, provider, subject)
}

func (tx *provisioningTx) EnsureBuiltinRoles(ctx context.Context, orgID model.OrgID) error {
	roles, err := tx.ListOrgRoles(ctx, orgID)
	if err != nil {
		return err
	}
	for _, spec := range builtinRoleSpecs() {
		exists := false
		for _, r := range roles {
			if r.BuiltinKind() == spec.kind {
				exists = true
				break
			}
		}
		if exists {
			continue
		}
		if err := tx.CreateRole(ctx, model.NewBuiltinRole(orgID, spec.kind, spec.name, spec.description, spec.permissions)); err != nil {
			return err
		}
	}
	return nil
}

var _ port.ProvisioningTransaction = (*Store)(nil)
var _ port.ProvisioningTx = (*provisioningTx)(nil)

// Parent locks also protect against ordinary (read-committed) writers removing
// a validated parent. They are shared row locks, never a lock on the whole tenant
// catalog; independent tenants remain writable.
func (tx *provisioningTx) parentReader(ctx context.Context) *gorm.DB {
	db := tx.db.WithContext(ctx)
	if isPostgres(db) {
		db = db.Clauses(clause.Locking{Strength: "SHARE"})
	}
	return db
}

func (tx *provisioningTx) GetTenantByID(ctx context.Context, id model.TenantID) (model.Tenant, error) {
	var tenant Tenant
	if err := tx.parentReader(ctx).First(&tenant, "id = ?", string(id)).Error; err != nil {
		return nil, invitationReadError(err)
	}
	return &wrappedTenant{&tenant}, nil
}
func (tx *provisioningTx) GetOrgByID(ctx context.Context, id model.OrgID) (model.Organization, error) {
	var org Organization
	if err := tx.parentReader(ctx).First(&org, "id = ?", string(id)).Error; err != nil {
		return nil, invitationReadError(err)
	}
	return &wrappedOrganization{&org}, nil
}
func (tx *provisioningTx) GetUserByID(ctx context.Context, id model.UserID) (model.User, error) {
	var user User
	if err := tx.parentReader(ctx).Preload("Roles").Preload("Preferences").First(&user, "id = ?", string(id)).Error; err != nil {
		return nil, invitationReadError(err)
	}
	return &wrappedUser{&user}, nil
}
func (tx *provisioningTx) GetUserByIdentity(ctx context.Context, tenant model.TenantID, provider, subject string) (model.User, error) {
	var user User
	if err := tx.parentReader(ctx).Preload("Roles").Preload("Preferences").Where("tenant_id = ? AND provider = ? AND subject = ?", string(tenant), provider, subject).First(&user).Error; err != nil {
		return nil, invitationReadError(err)
	}
	return &wrappedUser{&user}, nil
}
func (tx *provisioningTx) GetRoleByID(ctx context.Context, id model.RoleID) (model.Role, error) {
	var role Role
	if err := tx.parentReader(ctx).Preload("Permissions").Preload("ModelGrants").First(&role, "id = ?", string(id)).Error; err != nil {
		return nil, invitationReadError(err)
	}
	return &wrappedRole{&role}, nil
}
func (tx *provisioningTx) ListOrgRoles(ctx context.Context, id model.OrgID) ([]model.Role, error) {
	var roles []Role
	if err := tx.parentReader(ctx).Preload("Permissions").Preload("ModelGrants").Where("org_id = ?", string(id)).Order("id").Find(&roles).Error; err != nil {
		return nil, err
	}
	result := make([]model.Role, 0, len(roles))
	for i := range roles {
		result = append(result, &wrappedRole{&roles[i]})
	}
	return result, nil
}
