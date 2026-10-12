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
	GetTeamByID(ctx context.Context, id model.TeamID) (model.Team, error)
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
	var lookupErr error
	name, ok := model.ParseModelName(req.Model, func(orgSlug, segment string) bool {
		if orgSlug != org.Slug() {
			return false
		}
		team, err := h.teamStore.GetTeamBySlug(ctx, org.ID(), segment)
		if err != nil {
			if !errors.Is(err, port.ErrNotFound) {
				lookupErr = err
			}
			return false
		}
		named = team
		return true
	})
	if lookupErr != nil {
		// Never read the segment as part of a model name: the request would
		// be answered as naming an unknown model.
		return nil, errors.WithStack(lookupErr)
	}
	if !ok || (!name.Personal() && name.Org != org.Slug()) {
		// Not a name of the token's organization: the router answers.
		slog.DebugContext(ctx, "team scope: model name not of the token's organization, left to the router",
			slog.String("model", req.Model), slog.String("org", name.Org))
		return nil, nil
	}

	perms, err := httpCtx.ResolvePermissions(ctx, org.ID())
	if err != nil {
		return nil, errors.WithStack(err)
	}

	// A token bound to a team is refused here, once, when its principal can no
	// longer charge that team: every path below charges boundTeam as is.
	bound := BoundTeamIDFromContext(ctx)
	var boundTeam rbac.TeamRef
	if bound != "" {
		var ok bool
		if boundTeam, ok = teamRefOf(perms, string(bound)); !ok {
			return teamForbidden(req.Model, "the token is bound to a team it can no longer use"), nil
		}
	}

	var team rbac.TeamRef
	if name.Personal() {
		team, err = h.personalTeam(ctx, org.ID(), perms, boundTeam, name)
		if err != nil {
			var answer *teamScopeAnswer
			if errors.As(err, &answer) {
				return answer.result, nil
			}
			return nil, err
		}
	} else {
		if named != nil && (!perms.InTeam(string(named.ID())) || (bound != "" && bound != named.ID())) {
			// A team the caller can not charge is answered like a team that
			// does not exist: the name reaches the router unchanged, which
			// knows no model of that name. Telling them apart would let any
			// member list the teams of the organization. The server logs why.
			reason := "caller not in the team"
			if perms.InTeam(string(named.ID())) {
				reason = "token bound to another team"
			}
			slog.InfoContext(ctx, "team scope: model name names a team the caller can not charge",
				slog.String("model", req.Model), slog.String("team", string(named.ID())), slog.String("reason", reason))
			return nil, nil
		}
		kind, modelID, found, err := lookupLocalModel(ctx, h.providerStore, h.virtualModelStore, org.ID(), name.Local)
		if err != nil {
			return nil, err
		}
		if !found {
			// Unknown model: let the router answer. The team segment is
			// stripped all the same so that the router reads the name it
			// knows; the request fails there, so no team is recorded.
			h.setScope(req, name, rbac.TeamRef{})
			return nil, nil
		}
		switch {
		case named != nil:
			if !perms.CanUseModel(string(named.ID()), modelID, kind) {
				return virtualModelForbidden(req.Model), nil
			}
			team = refOf(named)
		case boundTeam.ID != "":
			team = boundTeam
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
// its model name. A name carrying a team segment only reaches it with that
// team resolved, or for an unknown model the router refuses: PreRequest
// answers every team the caller can not charge before.
func (h *TeamScopeHook) setScope(req *genaiProxy.ProxyRequest, name model.ModelName, team rbac.TeamRef) {
	if team.ID != "" {
		req.Metadata[MetaTeamID] = team.ID
		req.Metadata[MetaTeamSlug] = team.Slug
	}
	req.Model = name.OrgLevel().String()
}

// personalTeam attributes a personal virtual model: to the bound team, else
// the team its owner picked, else the single path left.
//
// A team picked by the owner that the owner can no longer charge, because it
// left the team or the team was deleted, refuses the request until the owner
// picks another one: the team it designated to pay is never swapped for
// another payer behind its back. A team of another organization is refused
// too: this organization can not charge it, and charging another one instead
// would be that swap.
//
// Only a model whose owner picked no team falls back on the path left: the
// organization alone with the organization-wide access or when the owner is
// in no team, the owner's single team, else model_team_required.
func (h *TeamScopeHook) personalTeam(ctx context.Context, orgID model.OrgID, perms rbac.PermissionSet, bound rbac.TeamRef, name model.ModelName) (rbac.TeamRef, error) {
	if bound.ID != "" {
		return bound, nil
	}
	if h.personalVMStore != nil {
		if u := httpCtx.User(ctx); u != nil {
			pvm, err := h.personalVMStore.GetPersonalVirtualModelByName(ctx, u.ID(), name.Local)
			if err != nil && !errors.Is(err, port.ErrNotFound) {
				// Reading it as "no model" would charge another payer: fail the request.
				return rbac.TeamRef{}, errors.WithStack(err)
			}
			if err == nil && pvm.TeamID() != "" {
				if team, ok := teamRefOf(perms, string(pvm.TeamID())); ok {
					return team, nil
				}
				team, err := h.teamStore.GetTeamByID(ctx, pvm.TeamID())
				switch {
				case err == nil && team.OrgID() != orgID:
					return rbac.TeamRef{}, &teamScopeAnswer{result: teamRequired(
						"personal virtual model '" + name.String() + "' charges a team of another organization: pick a team of this organization in its settings")}
				case err == nil || errors.Is(err, port.ErrNotFound):
					return rbac.TeamRef{}, &teamScopeAnswer{result: teamRequired(
						"the team personal virtual model '" + name.String() + "' charges is no longer available to you: pick another team in its settings")}
				default:
					return rbac.TeamRef{}, errors.WithStack(err)
				}
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
	return rbac.TeamRef{}, &teamScopeAnswer{result: teamRequired(
		"personal virtual model '" + name.String() + "' has no team in this organization: pick the team it charges in its settings, or use a token bound to a team")}
}

// teamRequired refuses a personal virtual model whose charged team must be
// picked again.
func teamRequired(message string) *genaiProxy.HookResult {
	return &genaiProxy.HookResult{
		Response: &genaiProxy.ProxyResponse{
			StatusCode: http.StatusBadRequest,
			Body: map[string]any{"error": map[string]any{
				"type":    "invalid_request_error",
				"message": message,
				"code":    "model_team_required",
			}},
		},
	}
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

// teamRefOf returns the team of the permission set, false when the principal
// is not in it: a caller then refuses rather than attribute a team it can not
// name.
func teamRefOf(perms rbac.PermissionSet, teamID string) (rbac.TeamRef, bool) {
	for _, t := range perms.Teams() {
		if t.ID == teamID {
			return t, true
		}
	}
	return rbac.TeamRef{}, false
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
