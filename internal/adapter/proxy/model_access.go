package proxy

import (
	"context"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/pipeline"
)

// requestModelAccess checks the models a pipeline reaches by name against the
// access of the request: the organization-wide model access, or the team the
// request is attributed to.
type requestModelAccess struct {
	org               model.Organization
	teamID            string
	teamSlug          string
	orgStore          port.OrgStore
	providerStore     port.ProviderStore
	virtualModelStore port.VirtualModelStore
}

func newRequestModelAccess(org model.Organization, teamID, teamSlug string, orgStore port.OrgStore, providerStore port.ProviderStore, virtualModelStore port.VirtualModelStore) *requestModelAccess {
	return &requestModelAccess{
		org:               org,
		teamID:            teamID,
		teamSlug:          teamSlug,
		orgStore:          orgStore,
		providerStore:     providerStore,
		virtualModelStore: virtualModelStore,
	}
}

// CanUse implements pipeline.ModelAccess.
//
// A name of another organization is allowed when the caller can use the model
// there, organization-wide or through one of its teams: personal virtual
// models may reference any organization of their owner. A name whose model
// does not exist is allowed, so that its resolution fails with the usual
// "not available" error.
func (a *requestModelAccess) CanUse(ctx context.Context, name string) (bool, error) {
	n, ok := model.ParseModelName(name, func(org, segment string) bool {
		return org == a.org.Slug() && a.teamSlug != "" && segment == a.teamSlug
	})
	if !ok {
		return false, nil
	}

	if n.Personal() {
		// personal-vm:create is also the right to use one's personal virtual
		// models: each model they reach is checked in turn.
		perms, err := httpCtx.ResolvePermissions(ctx, a.org.ID())
		if err != nil {
			return false, errors.WithStack(err)
		}
		return perms.Has(rbac.PermPersonalVMCreate), nil
	}

	org, teamID, local := a.org, a.teamID, n.Local
	if n.Org != "" && n.Org != a.org.Slug() {
		other, err := a.orgStore.GetOrgBySlug(ctx, httpCtx.TenantID(ctx), n.Org)
		switch {
		case errors.Is(err, port.ErrNotFound):
			// Not an organization: a bare local name holding a slash.
			local = n.OrgLevel().String()
		case err != nil:
			return false, errors.WithStack(err)
		case !other.Active():
			return false, nil
		default:
			org, teamID = other, ""
		}
	}

	kind, modelID, found, err := lookupLocalModel(ctx, a.providerStore, a.virtualModelStore, org.ID(), local)
	if err != nil {
		return false, err
	}
	if !found && local != n.Local {
		// The resolution reads the name the same way: a first segment that
		// names no organization nor a local name is dropped.
		local = n.Local
		kind, modelID, found, err = lookupLocalModel(ctx, a.providerStore, a.virtualModelStore, org.ID(), local)
		if err != nil {
			return false, err
		}
	}
	if !found {
		return true, nil
	}

	perms, err := httpCtx.ResolvePermissions(ctx, org.ID())
	if err != nil {
		return false, errors.WithStack(err)
	}
	if org.ID() != a.org.ID() {
		return perms.HasOrgWideModelAccess(kind) || len(perms.TeamsGranting(modelID, kind)) > 0, nil
	}
	return perms.CanUseModel(teamID, modelID, kind), nil
}

var _ pipeline.ModelAccess = &requestModelAccess{}

// lookupLocalModel resolves a local name of the organization to the model it
// designates, a virtual model first, as the pipeline does. A store error is
// returned, never read as an unknown model: an unknown model is let through
// to its resolution, and a model the resolution then finds would skip the
// access check.
func lookupLocalModel(ctx context.Context, providerStore port.ProviderStore, virtualModelStore port.VirtualModelStore, orgID model.OrgID, local string) (kind, id string, found bool, err error) {
	if virtualModelStore != nil {
		vm, err := virtualModelStore.GetVirtualModelByName(ctx, orgID, local)
		switch {
		case err == nil && vm != nil:
			return rbac.ModelKindVirtual, string(vm.ID()), true, nil
		case err != nil && !errors.Is(err, port.ErrNotFound):
			return "", "", false, errors.WithStack(err)
		}
	}
	if providerStore != nil {
		m, err := providerStore.GetLLMModelByProxyName(ctx, orgID, local)
		switch {
		case err == nil && m != nil:
			return rbac.ModelKindLLM, string(m.ID()), true, nil
		case err != nil && !errors.Is(err, port.ErrNotFound):
			return "", "", false, errors.WithStack(err)
		}
	}
	return "", "", false, nil
}
