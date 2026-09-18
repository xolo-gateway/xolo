package component

import (
	"context"
	"log/slog"
	"net/url"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authz"
	httpURL "github.com/xolo-gateway/xolo/internal/http/url"
)

// HasPermission reports whether the current user holds perm within orgID.
// Returns true for org owners (IsOwner bypass) and false on resolver errors.
func HasPermission(ctx context.Context, orgID model.OrgID, perm rbac.Permission) bool {
	perms, err := httpCtx.ResolvePermissions(ctx, orgID)
	if err != nil {
		return false
	}
	return perms.IsOwner() || perms.Has(perm)
}

var (
	WithPath        = httpURL.WithPath
	WithoutValues   = httpURL.WithoutValues
	WithValuesReset = httpURL.WithValuesReset
	WithValues      = httpURL.WithValues
)

func IsDesktopApp(ctx context.Context) bool {
	return false
}

func WithUser(username string, password string) httpURL.MutationFunc {
	return func(u *url.URL) {
		u.User = url.UserPassword(username, password)
	}
}

func BaseURL(ctx context.Context, funcs ...httpURL.MutationFunc) templ.SafeURL {
	baseURL := httpCtx.BaseURL(ctx)
	mutated := httpURL.Mutate(baseURL, funcs...)
	return templ.SafeURL(mutated.String())
}

func BaseURLString(ctx context.Context, funcs ...httpURL.MutationFunc) string {
	return string(BaseURL(ctx, funcs...))
}

func CurrentURL(ctx context.Context, funcs ...httpURL.MutationFunc) templ.SafeURL {
	return templ.SafeURL(httpURL.Mutate(httpCtx.CurrentURL(ctx), funcs...).String())
}

// CurrentURLString returns the current request URL as a plain string. It is
// the stringly-typed counterpart of CurrentURL, useful from .templ files
// that need to apply mutations across multiple branches (templ does not
// allow reassigning a `templ.SafeURL` value from a single declaration).
func CurrentURLString(ctx context.Context) string {
	return httpURL.Mutate(httpCtx.CurrentURL(ctx)).String()
}

// MutateCurrentURL returns the URL of the current request with funcs
// applied as a single in-place mutation, avoiding the
// serialize-parse round-trip of MutateURLString(CurrentURLString(ctx), ...).
// Prefer this over the stringly-typed composition when both helpers
// are available: a render of /models invokes the sort helper twice
// per segment and the show-all link, so the round-trip adds up.
func MutateCurrentURL(ctx context.Context, funcs ...httpURL.MutationFunc) string {
	return httpURL.Mutate(httpCtx.CurrentURL(ctx), funcs...).String()
}

// MutateURLString parses rawURL, applies funcs and returns the resulting URL
// as a string. It is intended for use from .templ when a SafeURL needs to
// be reconstructed with extra mutations; templ.SafeURL wrapping happens at
// the call site. Parse errors are logged at debug level and the raw URL
// is returned unchanged as a defensive fallback — the only caller passes
// CurrentURLString(ctx), which is guaranteed to be well-formed, so the
// fallback is unreachable in practice.
func MutateURLString(rawURL string, funcs ...httpURL.MutationFunc) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		slog.Debug("MutateURLString: failed to parse URL; returning raw value", slog.String("raw_url", rawURL), slogx.Error(err))
		return rawURL
	}
	return httpURL.Mutate(parsed, funcs...).String()
}

func MatchPath(ctx context.Context, path string) bool {
	currentURL := httpCtx.CurrentURL(ctx)
	return currentURL.Path == path
}

func AssertUser(ctx context.Context, funcs ...authz.AssertFunc) bool {
	user := httpCtx.User(ctx)
	if user == nil {
		return false
	}

	allowed, err := authz.Assert(ctx, user, funcs...)
	if err != nil {
		panic(errors.WithStack(err))
	}

	return allowed
}

var User = httpCtx.User

// HasPermissionInAnyOrg reports whether the current user holds perm in at least
// one of their org memberships. Useful for cross-org features like personal VMs.
func HasPermissionInAnyOrg(ctx context.Context, perm rbac.Permission) bool {
	for _, m := range httpCtx.Memberships(ctx) {
		if HasPermission(ctx, m.OrgID(), perm) {
			return true
		}
	}
	return false
}

// CanAccessModelsPage reports whether the current user can access the /models
// page, i.e. has PermModelUseOrg, PermModelUseVirtual, or at least one
// model-specific grant in any of their org memberships.
func CanAccessModelsPage(ctx context.Context) bool {
	for _, m := range httpCtx.Memberships(ctx) {
		perms, err := httpCtx.ResolvePermissions(ctx, m.OrgID())
		if err != nil {
			continue
		}
		if perms.IsOwner() ||
			perms.Has(rbac.PermModelUseOrg) ||
			perms.Has(rbac.PermModelUseVirtual) ||
			perms.HasAnyGrant(rbac.ModelKindLLM) ||
			perms.HasAnyGrant(rbac.ModelKindVirtual) {
			return true
		}
	}
	return false
}

// OrgAdminMemberships returns the user's memberships granting access to at
// least one organization administration section.
func OrgAdminMemberships(ctx context.Context) []model.Membership {
	all := httpCtx.Memberships(ctx)
	var result []model.Membership
	for _, m := range all {
		if membershipHasAdminAccess(m) {
			result = append(result, m)
		}
	}
	return result
}

func membershipHasAdminAccess(m model.Membership) bool {
	for _, r := range m.Roles() {
		if r.BuiltinKind() == model.BuiltinKindOwner || r.BuiltinKind() == model.BuiltinKindAdmin {
			return true
		}
		for _, code := range r.Permissions() {
			if rbac.IsAdminPermission(rbac.Permission(code)) {
				return true
			}
		}
	}
	return false
}
