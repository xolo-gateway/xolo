package component

import (
	"context"
	"strings"

	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

// The rbac catalog carries French labels as its own documentation; the pages
// show them through the rbac.section. and rbac.permission. keys, named after
// the section and the permission code (":" and "-" become "." and "_").

// PermissionGroupLabel names a permission group in the request language.
func PermissionGroupLabel(ctx context.Context, group rbac.PermissionGroup) string {
	return i18n.T(ctx, "rbac.section."+rbacKey(group.Section))
}

// PermissionLabel names a permission in the request language.
func PermissionLabel(ctx context.Context, def rbac.PermissionDef) string {
	return i18n.T(ctx, "rbac.permission."+rbacKey(string(def.Code)))
}

func rbacKey(value string) string {
	return strings.NewReplacer(":", ".", "-", "_").Replace(value)
}
