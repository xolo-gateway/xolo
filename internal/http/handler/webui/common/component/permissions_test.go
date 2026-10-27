package component

import (
	"context"
	"strings"
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/rbac"
)

// Every section and permission of the catalog has a translation: a missing
// key would render as the key itself.
func TestPermissionLabelsCoverTheCatalog(t *testing.T) {
	ctx := context.Background()
	for _, group := range rbac.Catalog() {
		if got := PermissionGroupLabel(ctx, group); got != group.Label {
			t.Errorf("section %s=%q, want %q", group.Section, got, group.Label)
		}
		for _, def := range group.Perms {
			if got := PermissionLabel(ctx, def); got != def.Label || strings.HasPrefix(got, "rbac.") {
				t.Errorf("permission %s=%q, want %q", def.Code, got, def.Label)
			}
		}
	}
}
