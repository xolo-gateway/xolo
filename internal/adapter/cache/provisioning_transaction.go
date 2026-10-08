package cache

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

type ProvisioningTransaction struct {
	backend   port.ProvisioningTransaction
	users     *UserStore
	providers *ProviderStore
}

// NewProvisioningTransaction invalidates the entries of users and providers
// a committed provisioning transaction changed. Either cache may be nil.
func NewProvisioningTransaction(backend port.ProvisioningTransaction, users *UserStore, providers *ProviderStore) *ProvisioningTransaction {
	return &ProvisioningTransaction{backend: backend, users: users, providers: providers}
}

func (s *ProvisioningTransaction) WithProvisioningTransaction(ctx context.Context, fn func(port.ProvisioningTx) error) error {
	var committed *provisioningInvalidations
	err := s.backend.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
		committed = nil
		pending := &provisioningInvalidations{users: map[model.UserID]bool{}, tenants: map[model.TenantID]bool{}, orgs: map[model.OrgID]bool{}, apps: map[model.ApplicationID]bool{}, providers: map[model.ProviderID]bool{}}
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
	if s.providers != nil {
		for id := range committed.providers {
			s.providers.providerCache.Remove(string(id))
		}
	}
	if s.users == nil || (len(committed.users) == 0 && len(committed.tenants) == 0 && len(committed.orgs) == 0 && len(committed.apps) == 0) {
		return nil
	}
	s.users.userCache.RemoveMatching(func(u *CacheableUser) bool { return committed.users[u.ID()] || committed.tenants[u.TenantID()] })
	s.users.authTokenCache.RemoveMatching(func(t *CacheableAuthToken) bool {
		if committed.orgs[t.OrgID()] {
			return true
		}
		if app := t.Application(); app != nil && (committed.orgs[app.OrgID()] || committed.apps[app.ID()]) {
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
	users     map[model.UserID]bool
	tenants   map[model.TenantID]bool
	orgs      map[model.OrgID]bool
	apps      map[model.ApplicationID]bool
	providers map[model.ProviderID]bool
}

// UpdateApplication invalidates the cached tokens of the application: they
// carry its active flag.
func (tx *provisioningTx) UpdateApplication(ctx context.Context, app model.Application) error {
	tx.apps[app.ID()] = true
	return tx.ProvisioningTx.UpdateApplication(ctx, app)
}

func (tx *provisioningTx) CreateProvider(ctx context.Context, p model.Provider) error {
	tx.providers[p.ID()] = true
	return tx.ProvisioningTx.CreateProvider(ctx, p)
}

func (tx *provisioningTx) SaveProvider(ctx context.Context, p model.Provider) error {
	tx.providers[p.ID()] = true
	return tx.ProvisioningTx.SaveProvider(ctx, p)
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
