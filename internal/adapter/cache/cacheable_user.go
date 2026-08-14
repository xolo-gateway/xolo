package cache

import (
	"fmt"
	"strings"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

type CacheableUser struct {
	model.User
}

// CacheKeys implements [Cacheable].
func (u *CacheableUser) CacheKeys() []string {
	return []string{
		getUserProviderSubjectCacheKey(u.TenantID(), u.Provider(), u.Subject()),
		string(u.ID()),
	}
}

func NewCacheableUser(user model.User) *CacheableUser {
	return &CacheableUser{user}
}

var (
	_ model.User = &CacheableUser{}
	_ Cacheable  = &CacheableUser{}
)

// getUserProviderSubjectCacheKey keys the identity cache. The tenant is part of
// the key because (provider, subject) is only unique within a tenant: without
// it, the first tenant to resolve an identity would serve it to every other
// one.
func getUserProviderSubjectCacheKey(tenantID model.TenantID, provider string, subject string) string {
	return getCompositeCacheKey(string(tenantID), provider, subject)
}

func getCompositeCacheKey(parts ...any) string {
	var sb strings.Builder
	for i, p := range parts {
		if i > 0 {
			sb.WriteString("|")
		}
		sb.WriteString(fmt.Sprintf("%s", p))
	}
	return sb.String()
}
