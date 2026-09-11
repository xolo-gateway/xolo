package component

import (
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
)

// explorerHref builds an events-explorer URL, preserving and properly
// URL-encoding the scope, query, page and browsing context (view).
func explorerHref(base, scope, query string, page int, view string) string {
	v := url.Values{}
	if scope != "" {
		v.Set("scope", scope)
	}
	if query != "" {
		v.Set("query", query)
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	if view != "" {
		v.Set("view", view)
	}
	if len(v) == 0 {
		return base
	}
	return base + "?" + v.Encode()
}

// viewedPath appends the browsing context marker to a simple path.
func viewedPath(path, view string) string {
	if view == "" {
		return path
	}
	return path + "?view=" + view
}

// severityTone maps an event severity onto the semantic colours of the design
// system, which the pills of the event tables are drawn with.
func severityTone(sev model.EventSeverity) common.Tone {
	switch sev {
	case model.SeverityError:
		return common.ToneNegative
	case model.SeverityWarning:
		return common.ToneWarning
	default:
		return common.ToneNeutral
	}
}

// alertStateTone maps an alert state onto the same scale: a firing alert is
// read as an incident, a pending one as a warning.
func alertStateTone(state model.AlertState) common.Tone {
	switch state {
	case model.AlertStateFiring:
		return common.ToneNegative
	case model.AlertStatePending:
		return common.ToneWarning
	default:
		return common.ToneNeutral
	}
}

// alertsSubtitle explains what an alert does, and which scope the current user
// is allowed to work in — the two things that decide what the screen can show.
func alertsSubtitle(vmodel AlertsPageVModel) string {
	subtitle := "Une alerte se déclenche quand le nombre d'événements correspondant à sa requête franchit son seuil sur sa fenêtre."
	switch {
	case vmodel.CanWriteOrg:
		return subtitle + " Vous gérez les alertes de l'organisation (portée « org »)."
	case vmodel.CanOwnAlerts:
		return subtitle + " Vos alertes portent sur vos propres événements (portée « perso »)."
	default:
		return subtitle
	}
}

// eventActor returns the displayable identity behind an event's user id,
// falling back to the raw id when the user could not be resolved (deleted
// account, or a lookup error already logged server-side).
func eventActor(e model.Event, actors map[model.UserID]EventActor) EventActor {
	if actor, ok := actors[e.UserID()]; ok {
		return actor
	}
	return EventActor{Label: string(e.UserID())}
}

type kv struct {
	Key   string
	Value string
}

// sortedAttrs returns the attributes of an event as a key-sorted slice.
func sortedAttrs(attrs map[string]string) []kv {
	result := make([]kv, 0, len(attrs))
	for k, v := range attrs {
		result = append(result, kv{Key: k, Value: v})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result
}

// formatEventTime renders a timestamp for the event tables.
func formatEventTime(t time.Time) string {
	return t.Local().Format("2006-01-02 15:04:05")
}

// formatIncidentTime renders an optional resolved-at timestamp.
func formatOptionalTime(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}
