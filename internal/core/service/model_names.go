package service

import (
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
)

// ListedModelNames returns the names under which a principal sees a model of
// its organization in a model list, one per way to reach it: the short form
// "<org>/<model>" for the organization-wide access, and "<org>/<team>/<model>"
// for each of its teams granting the model. A token bound to a team sees the
// team form only, the only one it may use. Names stay stable when a member
// joins another team.
func ListedModelNames(perms rbac.PermissionSet, bound model.TeamID, orgSlug, local, modelID, kind string) []string {
	if bound != "" {
		if !perms.InTeam(string(bound)) || !perms.CanUseModel(string(bound), modelID, kind) {
			return nil
		}
		return []string{model.ModelName{Org: orgSlug, Team: teamSlugOf(perms, string(bound)), Local: local}.String()}
	}
	var names []string
	if perms.HasOrgWideModelAccess(kind) {
		names = append(names, model.ModelName{Org: orgSlug, Local: local}.String())
	}
	for _, t := range perms.TeamsGranting(modelID, kind) {
		names = append(names, model.ModelName{Org: orgSlug, Team: t.Slug, Local: local}.String())
	}
	return names
}

func teamSlugOf(perms rbac.PermissionSet, teamID string) string {
	for _, t := range perms.Teams() {
		if t.ID == teamID {
			return t.Slug
		}
	}
	return ""
}
