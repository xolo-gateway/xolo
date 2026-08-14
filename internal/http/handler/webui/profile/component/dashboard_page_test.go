package component

import (
	"context"
	"strings"
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authz"
)

// TestDashboardPageKeepsEveryBlockWhenEmpty pins the rule the org dashboard
// already follows: a screen whose sections come and go with the data stops
// reading as a dashboard, and a missing figure becomes indistinguishable from a
// failed load. Every frame is rendered on a brand new account too.
func TestDashboardPageKeepsEveryBlockWhenEmpty(t *testing.T) {
	user := model.NewUser(testTenantID, "test", "subject", "user@xolo.test", "Ada Lovelace", true, authz.RoleUser)
	ctx := httpCtx.SetBaseURL(context.Background(), "http://xolo.test")
	ctx = httpCtx.SetUser(ctx, user)
	ctx = httpCtx.SetMemberships(ctx, nil)

	var out strings.Builder
	if err := DashboardPage(DashboardPageVModel{Range: "30d"}).Render(ctx, &out); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := out.String()

	for _, title := range []string{
		"Mon budget par organisation",
		"Requêtes",
		"Tokens",
		"Coût total",
		"Coût par jour",
		"Répartition par modèle",
		"Coût par fournisseur",
		"Requêtes récentes",
	} {
		if !strings.Contains(html, title) {
			t.Errorf("the empty dashboard should still render %q", title)
		}
	}
}

// testTenantID is the tenant every fixture of this package belongs to.
// Tenancy is not what these tests exercise: they only need a stable, shared
// owner so the tenant-scoped unique keys behave like the pre-tenant ones.
const testTenantID = model.TenantID("test-tenant")
