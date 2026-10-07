package v1

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

const (
	defaultCommonLimit = 100
	maxCommonLimit     = 1000
)

// commonTarget reads the scope and, for a unit route, the key of a common
// resource from the path. A route without the key segment designates the
// collection: its key is empty.
func commonTarget(w http.ResponseWriter, r *http.Request, family string) (model.CommonScope, string, bool) {
	scope := model.CommonScope{Family: family}
	parse := func(name string, parser func(string) (string, error)) (string, bool) {
		raw := r.PathValue(name)
		if raw == "" {
			return "", true
		}
		value, err := parser(raw)
		if err != nil {
			writeInvalidID(w)
			return "", false
		}
		return value, true
	}
	tenantUUID := func(s string) (string, error) { id, err := model.ParseTenantID(s); return string(id), err }
	orgUUID := func(s string) (string, error) { id, err := model.ParseOrgID(s); return string(id), err }
	userUUID := func(s string) (string, error) { id, err := model.ParseUserID(s); return string(id), err }
	tenantID, ok := parse("tenantID", tenantUUID)
	if !ok {
		return scope, "", false
	}
	if family == model.FamilyTenant {
		return scope, tenantID, true
	}
	scope.TenantID = tenantID
	if family == model.FamilyOrganizationMembership {
		if scope.OrganizationID, ok = parse("orgID", orgUUID); !ok {
			return scope, "", false
		}
	}
	var key string
	switch family {
	case model.FamilyTenantDomain:
		// The store refuses a hostname that is not already normalized.
		key = r.PathValue("hostname")
	case model.FamilyOrganization:
		key, ok = parse("orgID", orgUUID)
	default:
		key, ok = parse("memberID", userUUID)
	}
	return scope, key, ok
}

// noQuery refuses any query parameter on a route that defines none.
func noQuery(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, codeInvalidParameter, "this route defines no query parameter")
		return false
	}
	return true
}

// commonPagination reads the only parameters of lists and of the event feed:
// cursor and limit, each at most once.
func commonPagination(w http.ResponseWriter, r *http.Request, requireCursor bool) (string, int, bool) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidParameter, "malformed query")
		return "", 0, false
	}
	for name, value := range values {
		if (name != "cursor" && name != "limit") || len(value) != 1 {
			writeError(w, http.StatusBadRequest, codeInvalidParameter, "only cursor and limit are accepted, once each")
			return "", 0, false
		}
	}
	limit := defaultCommonLimit
	if raw, ok := values["limit"]; ok {
		n, err := strconv.Atoi(raw[0])
		if err != nil || n < 1 || n > maxCommonLimit || strconv.Itoa(n) != raw[0] {
			writeError(w, http.StatusBadRequest, codeInvalidParameter, "limit must be an integer between 1 and 1000")
			return "", 0, false
		}
		limit = n
	}
	raw, present := values["cursor"]
	switch {
	case !present && requireCursor:
		writeError(w, http.StatusBadRequest, codeInvalidParameter, "cursor is required; capture one with GET /v1/events/cursor")
		return "", 0, false
	case present && raw[0] == "":
		writeError(w, http.StatusBadRequest, codeInvalidCursor, "empty cursor")
		return "", 0, false
	case present:
		return raw[0], limit, true
	}
	return "", limit, true
}

func (h *Handler) handleCommonGet(family string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scope, key, ok := commonTarget(w, r, family)
		if !ok || !noQuery(w, r) {
			return
		}
		item, err := h.provisioning.GetCommon(r.Context(), scope, key)
		if err != nil {
			writeServiceError(r.Context(), w, err, "could not read resource")
			return
		}
		w.Header().Set("ETag", item.ETag)
		writeJSON(w, http.StatusOK, item.Representation)
	}
}

func (h *Handler) handleCommonList(family string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scope, _, ok := commonTarget(w, r, family)
		if !ok {
			return
		}
		cursor, limit, ok := commonPagination(w, r, false)
		if !ok {
			return
		}
		page, err := h.provisioning.ListCommon(r.Context(), scope, cursor, limit)
		if err != nil {
			writeServiceError(r.Context(), w, err, "could not list resources")
			return
		}
		writeJSON(w, http.StatusOK, page)
	}
}

func (h *Handler) handleEventCursor(w http.ResponseWriter, r *http.Request) {
	if !noQuery(w, r) {
		return
	}
	cursor, err := h.provisioning.CaptureEventCursor(r.Context())
	if err != nil {
		writeServiceError(r.Context(), w, err, "could not capture the event cursor")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"cursor": cursor})
}

func (h *Handler) handleEvents(w http.ResponseWriter, r *http.Request) {
	cursor, limit, ok := commonPagination(w, r, true)
	if !ok {
		return
	}
	page, err := h.provisioning.ReadEvents(r.Context(), cursor, limit)
	if err != nil {
		writeServiceError(r.Context(), w, err, "could not read events")
		return
	}
	writeJSON(w, http.StatusOK, page)
}
