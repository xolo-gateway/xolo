package cache

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

type ProvisioningTransaction struct {
	backend port.ProvisioningTransaction
	users   *UserStore
}

func NewProvisioningTransaction(backend port.ProvisioningTransaction, users *UserStore) *ProvisioningTransaction {
	return &ProvisioningTransaction{backend: backend, users: users}
}

func (s *ProvisioningTransaction) WithProvisioningTransaction(ctx context.Context, fn func(port.ProvisioningTx) error) error {
	var committed *provisioningInvalidations
	err := s.backend.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
		committed = nil
		pending := &provisioningInvalidations{users: map[model.UserID]bool{}, tenants: map[model.TenantID]bool{}, orgs: map[model.OrgID]bool{}}
		bound := &provisioningTx{ProvisioningTx: tx, provisioningInvalidations: pending}
		if err := fn(bound); err != nil {
			return err
		}
		committed = pending
		return nil
	})
	if err != nil {
		return err
	}
	if len(committed.users) == 0 && len(committed.tenants) == 0 && len(committed.orgs) == 0 {
		return nil
	}
	s.users.userCache.RemoveMatching(func(u *CacheableUser) bool { return committed.users[u.ID()] || committed.tenants[u.TenantID()] })
	s.users.authTokenCache.RemoveMatching(func(t *CacheableAuthToken) bool {
		if committed.orgs[t.OrgID()] {
			return true
		}
		if app := t.Application(); app != nil && committed.orgs[app.OrgID()] {
			return true
		}
		u := t.Owner()
		return u != nil && (committed.users[u.ID()] || committed.tenants[u.TenantID()])
	})
	return nil
}

// provisioningTx records which cache entries to invalidate after commit. Only
// SaveUser, DeleteOrg and DeleteTenant invalidate: CacheableUser holds nothing
// derived from organizations, so DeleteRole and RemoveMember invalidate nothing
// and DeleteOrg does not sweep the organization's members. If roles,
// permissions or memberships ever enter the user cache, these paths must start
// invalidating the affected users.
type provisioningTx struct {
	port.ProvisioningTx
	*provisioningInvalidations
}

type provisioningInvalidations struct {
	users   map[model.UserID]bool
	tenants map[model.TenantID]bool
	orgs    map[model.OrgID]bool
}

func (tx *provisioningTx) SaveUser(ctx context.Context, user model.User) error {
	tx.users[user.ID()] = true
	return tx.ProvisioningTx.SaveUser(ctx, user)
}

func (tx *provisioningTx) DeleteOrg(ctx context.Context, id model.OrgID) error {
	tx.orgs[id] = true
	return tx.ProvisioningTx.DeleteOrg(ctx, id)
}

func (tx *provisioningTx) DeleteTenant(ctx context.Context, id model.TenantID) error {
	orgs, _, err := tx.ListOrgs(ctx, port.ListOrgsOptions{TenantID: &id})
	if err != nil {
		return err
	}
	for _, org := range orgs {
		tx.orgs[org.ID()] = true
	}
	tx.tenants[id] = true
	return tx.ProvisioningTx.DeleteTenant(ctx, id)
}

var _ port.ProvisioningTransaction = (*ProvisioningTransaction)(nil)
