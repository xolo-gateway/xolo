package model

import "strings"

// PersonalModelOrg is the organization segment of a personal virtual model
// name, "~/<name>".
const PersonalModelOrg = "~"

// ModelName is a model name as a client or a pipeline node gives it, split
// into its segments: "<org>/<team>/<model>", "<org>/<model>", "~/<model>" or a
// bare "<model>".
type ModelName struct {
	// Org is the organization slug, PersonalModelOrg for a personal virtual
	// model, or empty when the name is not qualified.
	Org string
	// Team is the team slug, empty when the name carries none.
	Team string
	// Local is the proxy name of a real model or the name of a virtual model,
	// inside the organization. It may contain slashes itself, as a vLLM model
	// declared under its Hugging Face ID does.
	Local string
}

// Personal reports whether the name designates a personal virtual model.
func (n ModelName) Personal() bool { return n.Org == PersonalModelOrg }

// Qualified reports whether the name carries an organization segment.
func (n ModelName) Qualified() bool { return n.Org != "" }

// String rebuilds the name, team segment included.
func (n ModelName) String() string {
	parts := make([]string, 0, 3)
	if n.Org != "" {
		parts = append(parts, n.Org)
	}
	if n.Team != "" {
		parts = append(parts, n.Team)
	}
	return strings.Join(append(parts, n.Local), "/")
}

// OrgLevel returns the name without its team segment.
func (n ModelName) OrgLevel() ModelName {
	n.Team = ""
	return n
}

// ParseModelName splits name into its segments. It is the only parser of
// model names: the proxy, the pipeline engine and the models API all go
// through it, so that a name resolves the same way on every path.
//
// The first segment is always the organization. The second one is a team only
// when isTeam says so: a proxy name can contain slashes, and team slugs are
// kept from clashing with proxy names when either is created (see
// ModelNameConflicts). A nil isTeam reads no team segment. isTeam is called
// at most once, synchronously, before ParseModelName returns: a caller may
// record a lookup error in a variable it captured and read it afterwards.
//
// A name with an empty segment where one is required is invalid.
func ParseModelName(name string, isTeam func(org, segment string) bool) (ModelName, bool) {
	if name == "" {
		return ModelName{}, false
	}
	org, rest, qualified := strings.Cut(name, "/")
	if !qualified {
		return ModelName{Local: name}, true
	}
	if org == "" || rest == "" {
		return ModelName{}, false
	}
	if org == PersonalModelOrg {
		return ModelName{Org: org, Local: rest}, true
	}
	if segment, local, found := strings.Cut(rest, "/"); found && segment != "" && local != "" && isTeam != nil && isTeam(org, segment) {
		return ModelName{Org: org, Team: segment, Local: local}, true
	}
	return ModelName{Org: org, Local: rest}, true
}

// ModelNameConflicts reports whether a team slug and a model name of the same
// organization (a proxy name or a virtual model name) would make qualified
// names ambiguous: "<org>/<slug>/<rest>" must never designate both a model of
// the team and a model whose local name is "<slug>/<rest>".
func ModelNameConflicts(teamSlug, localName string) bool {
	return strings.HasPrefix(localName, teamSlug+"/")
}
