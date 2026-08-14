package org

import (
	"context"
	"slices"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authz"
	"github.com/pkg/errors"
)

// hasPermission returns an authz.AssertFunc that checks whether the current
// user holds the given permission within the org identified by orgSlug. A
// global admin and a member with the builtin owner role bypass the check.
//
// Resolution goes through the context resolver so that every principal kind —
// members and applications alike — is handled the same way.
func (h *Handler) hasPermission(orgSlug string, perm rbac.Permission) authz.AssertFunc {
	return func(ctx context.Context, user model.User) (bool, error) {
		if user == nil {
			return false, nil
		}
		// Global admin has access to everything.
		if isGlobalAdmin(user) {
			return true, nil
		}

		org, err := h.orgStore.GetOrgBySlug(ctx, httpCtx.TenantID(ctx), orgSlug)
		if err != nil {
			return false, errors.WithStack(err)
		}

		set, err := httpCtx.ResolvePermissions(ctx, org.ID())
		if err != nil {
			return false, errors.WithStack(err)
		}

		return set.IsOwner() || set.Has(perm), nil
	}
}

// hasAnyPermission returns an authz.AssertFunc that passes when the user holds
// at least one of the given permissions within the org.
func (h *Handler) hasAnyPermission(orgSlug string, perms ...rbac.Permission) authz.AssertFunc {
	return func(ctx context.Context, user model.User) (bool, error) {
		if user == nil {
			return false, nil
		}
		if isGlobalAdmin(user) {
			return true, nil
		}

		org, err := h.orgStore.GetOrgBySlug(ctx, httpCtx.TenantID(ctx), orgSlug)
		if err != nil {
			return false, errors.WithStack(err)
		}

		set, err := httpCtx.ResolvePermissions(ctx, org.ID())
		if err != nil {
			return false, errors.WithStack(err)
		}
		if set.IsOwner() {
			return true, nil
		}
		for _, perm := range perms {
			if set.Has(perm) {
				return true, nil
			}
		}
		return false, nil
	}
}

// isGlobalAdmin reports whether the principal holds the platform admin role.
// The shadow user backing an application is excluded: its platform-level roles
// are an artefact of token authentication, never an operator grant.
func isGlobalAdmin(user model.User) bool {
	if user.Provider() == model.ApplicationProvider {
		return false
	}
	return slices.Contains(user.Roles(), authz.RoleAdmin)
}

// hasOrgMembership returns an authz.AssertFunc checking whether the principal
// belongs to the org — as a member, or as an application holding at least one
// of the org's roles.
func (h *Handler) hasOrgMembership(orgSlug string) authz.AssertFunc {
	return func(ctx context.Context, user model.User) (bool, error) {
		if user == nil {
			return false, nil
		}
		if isGlobalAdmin(user) {
			return true, nil
		}

		org, err := h.orgStore.GetOrgBySlug(ctx, httpCtx.TenantID(ctx), orgSlug)
		if err != nil {
			return false, errors.WithStack(err)
		}

		if user.Provider() == model.ApplicationProvider {
			roles, err := h.roleStore.ListApplicationRoles(ctx, model.ApplicationID(user.Subject()))
			if err != nil {
				return false, errors.WithStack(err)
			}
			for _, role := range roles {
				if role.OrgID() == org.ID() {
					return true, nil
				}
			}
			return false, nil
		}

		return h.orgStore.IsMember(ctx, user.ID(), org.ID())
	}
}

// orgFromSlug resolves the org from the request path slug.
func (h *Handler) orgFromSlug(ctx context.Context, orgSlug string) (model.Organization, error) {
	return h.orgStore.GetOrgBySlug(ctx, httpCtx.TenantID(ctx), orgSlug)
}

// currentUser retrieves the current user from context.
func currentUser(ctx context.Context) model.User {
	return httpCtx.User(ctx)
}
