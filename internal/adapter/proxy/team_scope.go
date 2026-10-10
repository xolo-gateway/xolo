package proxy

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	genaiProxy "github.com/bornholm/genai/proxy"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	authn "github.com/xolo-gateway/xolo/internal/http/middleware/authn"
)

// Metadata keys set by TeamScopeHook.
const (
	// MetaTeamID is the team the request is attributed to, absent or empty
	// when it is charged to the organization alone.
	MetaTeamID   = "teamID"
	MetaTeamSlug = "teamSlug"
	// MetaRequestedModel is the model name as the client sent it, team
	// segment included.
	MetaRequestedModel = "requestedModel"
	metaTeamScoped     = "teamScoped"
)

// TeamIDFromMeta reads the team a request is attributed to.
func TeamIDFromMeta(meta map[string]any) model.TeamID {
	v, _ := meta[MetaTeamID].(string)
	return model.TeamID(v)
}

// TeamSlugFromMeta reads the slug of the team a request is attributed to.
func TeamSlugFromMeta(meta map[string]any) string {
	v, _ := meta[MetaTeamSlug].(string)
	return v
}

// RequestedModelFromMeta reads the model name as the client sent it.
func RequestedModelFromMeta(meta map[string]any) string {
	v, _ := meta[MetaRequestedModel].(string)
	return v
}

// BoundTeamIDFromContext returns the team the API token or the application of
// the request is bound to, empty when none.
func BoundTeamIDFromContext(ctx context.Context) model.TeamID {
	if u := authn.OptionalContextUser(ctx); u != nil {
		return model.TeamID(u.TeamID)
	}
	return ""
}

// TeamLookup resolves the teams of an organization.
type TeamLookup interface {
	GetTeamBySlug(ctx context.Context, orgID model.OrgID, slug string) (model.Team, error)
}

// TeamScopeHook attributes every proxied request to exactly one team, or to
// the organization alone, before any other hook reads the model name.
//
// The team comes from the model name, "<org>/<team>/<model>". A token or an
// application bound to a team is always attributed to it. The short form
// "<org>/<model>" is accepted when a single path exists: the token is bound,
// the member has the organization-wide model access (only the organization is
// then charged), or exactly one of the member's teams grants the model.
// Otherwise the request is refused with the qualified names it could use.
//
// Once attributed, the request carries its team in its metadata and its model
// name loses the team segment, so that every later hook resolves the name the
// way it always did.
type TeamScopeHook struct {
	orgStore          port.OrgStore
	teamStore         TeamLookup
	providerStore     port.ProviderStore
	virtualModelStore port.VirtualModelStore
	personalVMStore   port.PersonalVirtualModelStore
}

func NewTeamScopeHook(orgStore port.OrgStore, teamStore TeamLookup, providerStore port.ProviderStore, virtualModelStore port.VirtualModelStore, personalVMStore port.PersonalVirtualModelStore) *TeamScopeHook {
	return &TeamScopeHook{
		orgStore:          orgStore,
		teamStore:         teamStore,
		providerStore:     providerStore,
		virtualModelStore: virtualModelStore,
		personalVMStore:   personalVMStore,
	}
}

func (h *TeamScopeHook) Name() string { return "xolo.team-scope" }

// Priority runs the hook before the pipeline (3), which resolves middlewares
// and virtual models from the model name.
func (h *TeamScopeHook) Priority() int { return 2 }

// PreRequest implements proxy.PreRequestHook.
func (h *TeamScopeHook) PreRequest(ctx context.Context, req *genaiProxy.ProxyRequest) (*genaiProxy.HookResult, error) {
	if scoped, _ := req.Metadata[metaTeamScoped].(bool); scoped {
		return nil, nil
	}
	PopulateMetaFromContext(ctx, req.Metadata)
	req.Metadata[metaTeamScoped] = true
	req.Metadata[MetaRequestedModel] = req.Model

	orgID := OrgIDFromMeta(req.Metadata)
	if orgID == "" || req.UserID == "" {
		// Unauthenticated or unscoped: the router answers.
		return nil, nil
	}
	org, err := h.orgStore.GetOrgByID(ctx, orgID)
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			return nil, nil
		}
		return nil, errors.WithStack(err)
	}

	var named model.Team
	name, ok := model.ParseModelName(req.Model, func(orgSlug, segment string) bool {
		if orgSlug != org.Slug() {
			return false
		}
		team, err := h.teamStore.GetTeamBySlug(ctx, org.ID(), segment)
		if err != nil {
			if !errors.Is(err, port.ErrNotFound) {
				slog.ErrorContext(ctx, "team scope: could not look up team", slog.Any("error", err))
			}
			return false
		}
		named = team
		return true
	})
	if !ok || (!name.Personal() && name.Org != org.Slug()) {
		// Not a name of the token's organization: the router answers.
		return nil, nil
	}

	perms, err := httpCtx.ResolvePermissions(ctx, org.ID())
	if err != nil {
		return nil, errors.WithStack(err)
	}

	bound := BoundTeamIDFromContext(ctx)
	if bound != "" && !perms.InTeam(string(bound)) {
		return teamForbidden(req.Model, "the token is bound to a team it can no longer use"), nil
	}

	var team rbac.TeamRef
	if name.Personal() {
		team, err = h.personalTeam(ctx, perms, bound, name)
		if err != nil {
			var answer *teamScopeAnswer
			if errors.As(err, &answer) {
				return answer.result, nil
			}
			return nil, err
		}
	} else {
		kind, modelID, found := lookupLocalModel(ctx, h.providerStore, h.virtualModelStore, org.ID(), name.Local)
		if !found {
			// Unknown model: let the router answer, with the team kept for
			// the error path only.
			h.setScope(req, name, refOf(named))
			return nil, nil
		}
		switch {
		case named != nil:
			if bound != "" && string(bound) != string(named.ID()) {
				return teamForbidden(req.Model, "the token is bound to another team"), nil
			}
			if !perms.InTeam(string(named.ID())) {
				return teamForbidden(req.Model, "not a member of team '"+named.Slug()+"'"), nil
			}
			if !perms.CanUseModel(string(named.ID()), modelID, kind) {
				return virtualModelForbidden(req.Model), nil
			}
			team = refOf(named)
		case bound != "":
			team = teamRefOf(perms, string(bound))
			if !perms.CanUseModel(team.ID, modelID, kind) {
				return virtualModelForbidden(req.Model), nil
			}
		case perms.HasOrgWideModelAccess(kind):
			// Charged to the organization alone.
		default:
			teams := perms.TeamsGranting(modelID, kind)
			switch len(teams) {
			case 0:
				return virtualModelForbidden(req.Model), nil
			case 1:
				team = teams[0]
			default:
				return ambiguousModel(req.Model, org.Slug(), name.Local, teams), nil
			}
		}
	}

	h.setScope(req, name, team)
	return nil, nil
}

// setScope records the team of the request and strips the team segment from
// its model name.
func (h *TeamScopeHook) setScope(req *genaiProxy.ProxyRequest, name model.ModelName, team rbac.TeamRef) {
	if team.ID != "" {
		req.Metadata[MetaTeamID] = team.ID
		req.Metadata[MetaTeamSlug] = team.Slug
	}
	req.Model = name.OrgLevel().String()
}

// personalTeam attributes a personal virtual model: to the bound team, else
// the team its owner picked, else the single path left.
func (h *TeamScopeHook) personalTeam(ctx context.Context, perms rbac.PermissionSet, bound model.TeamID, name model.ModelName) (rbac.TeamRef, error) {
	if bound != "" {
		return teamRefOf(perms, string(bound)), nil
	}
	if h.personalVMStore != nil {
		if u := httpCtx.User(ctx); u != nil {
			pvm, err := h.personalVMStore.GetPersonalVirtualModelByName(ctx, u.ID(), name.Local)
			if err == nil && pvm.TeamID() != "" && perms.InTeam(string(pvm.TeamID())) {
				return teamRefOf(perms, string(pvm.TeamID())), nil
			}
		}
	}
	if perms.HasOrgWideModelAccess(rbac.ModelKindLLM) {
		return rbac.TeamRef{}, nil
	}
	teams := perms.Teams()
	switch len(teams) {
	case 0:
		return rbac.TeamRef{}, nil
	case 1:
		return teams[0], nil
	}
	return rbac.TeamRef{}, &teamScopeAnswer{result: &genaiProxy.HookResult{
		Response: &genaiProxy.ProxyResponse{
			StatusCode: http.StatusBadRequest,
			Body: map[string]any{"error": map[string]any{
				"type":    "invalid_request_error",
				"message": "personal virtual model '" + name.String() + "' has no team in this organization: pick the team it charges in its settings, or use a token bound to a team",
				"code":    "model_team_required",
			}},
		},
	}}
}

var _ genaiProxy.PreRequestHook = &TeamScopeHook{}

// teamScopeAnswer carries a refusal out of a helper.
type teamScopeAnswer struct {
	result *genaiProxy.HookResult
}

func (a *teamScopeAnswer) Error() string { return "request refused by the team scope" }

func refOf(team model.Team) rbac.TeamRef {
	if team == nil {
		return rbac.TeamRef{}
	}
	return rbac.TeamRef{ID: string(team.ID()), Slug: team.Slug()}
}

func teamRefOf(perms rbac.PermissionSet, teamID string) rbac.TeamRef {
	for _, t := range perms.Teams() {
		if t.ID == teamID {
			return t
		}
	}
	return rbac.TeamRef{ID: teamID}
}

func teamForbidden(modelName, reason string) *genaiProxy.HookResult {
	return &genaiProxy.HookResult{
		Response: &genaiProxy.ProxyResponse{
			StatusCode: http.StatusForbidden,
			Body: map[string]any{"error": map[string]any{
				"type":    "permission_error",
				"message": "model '" + modelName + "' not available: " + reason,
				"code":    "model_forbidden",
			}},
		},
	}
}

// ambiguousModel refuses a short name reachable through several teams, and
// lists the qualified names the client can use instead.
func ambiguousModel(modelName, orgSlug, local string, teams []rbac.TeamRef) *genaiProxy.HookResult {
	candidates := make([]string, 0, len(teams))
	for _, t := range teams {
		candidates = append(candidates, model.ModelName{Org: orgSlug, Team: t.Slug, Local: local}.String())
	}
	return &genaiProxy.HookResult{
		Response: &genaiProxy.ProxyResponse{
			StatusCode: http.StatusBadRequest,
			Body: map[string]any{"error": map[string]any{
				"type":       "invalid_request_error",
				"message":    "model '" + modelName + "' is available through several teams, name the one to charge: " + strings.Join(candidates, ", "),
				"code":       "model_ambiguous",
				"candidates": candidates,
			}},
		},
	}
}
