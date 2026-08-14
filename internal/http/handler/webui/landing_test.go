package webui

import (
	"net/url"
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authz"
)

// A platform administrator with no organisation is the state of a fresh install:
// sending them to /no-org leaves the only account able to create an organisation
// with a logout button for sole exit.
func TestLandingWithoutOrg(t *testing.T) {
	base, err := url.Parse("http://xolo.test")
	if err != nil {
		t.Fatal(err)
	}

	admin := model.NewUser(testTenantID, "test", "s1", "admin@xolo.test", "Admin", true, authz.RoleAdmin)
	plain := model.NewUser(testTenantID, "test", "s2", "user@xolo.test", "User", true, authz.RoleUser)

	if got, want := landingWithoutOrg(admin, base), "http://xolo.test/admin/"; got != want {
		t.Errorf("administrator: got %q, want %q", got, want)
	}
	if got, want := landingWithoutOrg(plain, base), "http://xolo.test/no-org"; got != want {
		t.Errorf("plain account: got %q, want %q", got, want)
	}
	if got, want := landingWithoutOrg(nil, base), "http://xolo.test/no-org"; got != want {
		t.Errorf("anonymous: got %q, want %q", got, want)
	}
}

// testTenantID is the tenant every fixture of this package belongs to.
// Tenancy is not what these tests exercise: they only need a stable, shared
// owner so the tenant-scoped unique keys behave like the pre-tenant ones.
const testTenantID = model.TenantID("test-tenant")
