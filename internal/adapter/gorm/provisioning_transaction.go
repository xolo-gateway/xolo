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

// WithProvisioningTransaction replays validation, writes and audit together.
// PostgreSQL SSI protects cross-row invariants without an instance-wide lock.
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
			tx := &provisioningTx{
				Store: &Store{getDatabase: func(context.Context) (*gorm.DB, error) { return db, nil }, transactionBound: true},
				db:    db, before: make(map[mutationKey][]byte),
			}
			if err := fn(tx); err != nil {
				return err
			}
			return tx.flushMutations(ctx)
		}, opts)
	})
}

type provisioningTx struct {
	*Store
	db     *gorm.DB
	before map[mutationKey][]byte
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
	if err := tx.track("user", string(u.ID())); err != nil {
		return nil, err
	}
	// A concurrent insertion of this identity replays the serializable transaction.
	if err := tx.db.Omit(clause.Associations).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "tenant_id"}, {Name: "provider"}, {Name: "subject"}}, DoNothing: true,
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

func (tx *provisioningTx) CreateTenant(ctx context.Context, tenant model.Tenant) error {
	if err := tx.track("tenant", string(tenant.ID())); err != nil {
		return err
	}
	return tx.Store.CreateTenant(ctx, tenant)
}

func (tx *provisioningTx) SaveTenant(ctx context.Context, tenant model.Tenant) error {
	if err := tx.track("tenant", string(tenant.ID())); err != nil {
		return err
	}
	return tx.Store.SaveTenant(ctx, tenant)
}

func (tx *provisioningTx) DeleteTenant(ctx context.Context, id model.TenantID) error {
	if err := tx.track("tenant", string(id)); err != nil {
		return err
	}
	if err := tx.trackDependents("tenant", string(id)); err != nil {
		return err
	}
	return tx.Store.DeleteTenant(ctx, id)
}

func (tx *provisioningTx) CreateOrg(ctx context.Context, org model.Organization) error {
	if err := tx.track("organization", string(org.ID())); err != nil {
		return err
	}
	return tx.Store.CreateOrg(ctx, org)
}

func (tx *provisioningTx) SaveOrg(ctx context.Context, org model.Organization) error {
	if err := tx.track("organization", string(org.ID())); err != nil {
		return err
	}
	return tx.Store.SaveOrg(ctx, org)
}

func (tx *provisioningTx) DeleteOrg(ctx context.Context, id model.OrgID) error {
	if err := tx.track("organization", string(id)); err != nil {
		return err
	}
	if err := tx.trackDependents("organization", string(id)); err != nil {
		return err
	}
	return tx.Store.DeleteOrg(ctx, id)
}

func (tx *provisioningTx) SaveUser(ctx context.Context, user model.User) error {
	if err := tx.track("user", string(user.ID())); err != nil {
		return err
	}
	return tx.Store.SaveUser(ctx, user)
}

func (tx *provisioningTx) AddMember(ctx context.Context, membership model.Membership) error {
	if err := tx.track("membership", string(membership.ID())); err != nil {
		return err
	}
	return tx.Store.AddMember(ctx, membership)
}

func (tx *provisioningTx) RemoveMember(ctx context.Context, id model.MembershipID) error {
	if err := tx.track("membership", string(id)); err != nil {
		return err
	}
	return tx.Store.RemoveMember(ctx, id)
}

func (tx *provisioningTx) SetMembershipRoles(ctx context.Context, id model.MembershipID, roles []model.RoleID) error {
	if err := tx.track("membership", string(id)); err != nil {
		return err
	}
	return tx.Store.SetMembershipRoles(ctx, id, roles)
}

func (tx *provisioningTx) CreateRole(ctx context.Context, role model.Role) error {
	if err := tx.track("role", string(role.ID())); err != nil {
		return err
	}
	return tx.Store.CreateRole(ctx, role)
}

func (tx *provisioningTx) SaveRole(ctx context.Context, role model.Role) error {
	if err := tx.track("role", string(role.ID())); err != nil {
		return err
	}
	return tx.Store.SaveRole(ctx, role)
}

func (tx *provisioningTx) DeleteRole(ctx context.Context, id model.RoleID) error {
	if err := tx.track("role", string(id)); err != nil {
		return err
	}
	if err := tx.trackDependents("role", string(id)); err != nil {
		return err
	}
	return tx.Store.DeleteRole(ctx, id)
}

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
