package v1

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

func commonPath(r *http.Request) (model.CommonScope, string) {
	s := model.CommonScope{Family: "tenant"}
	key := r.PathValue("tenantID")
	if strings.Contains(r.Pattern, "/domains") {
		s.Family = "tenant_domain"
		key = r.PathValue("hostname")
	} else if strings.Contains(r.Pattern, "/members") {
		s.Family = "member"
		key = r.PathValue("memberID")
		if r.PathValue("orgID") != "" {
			s.Family = "organization_membership"
			s.OrganizationID = r.PathValue("orgID")
		}
	} else if strings.Contains(r.Pattern, "/organizations") {
		s.Family = "organization"
		key = r.PathValue("orgID")
	}
	if s.Family != "tenant" {
		s.TenantID = r.PathValue("tenantID")
	}
	if s.Family == "tenant_domain" {
		if host, err := model.NormalizeHostname(key); err == nil {
			key = host
		}
	}
	return s, key
}
func commonPagination(w http.ResponseWriter, r *http.Request, required bool) (string, int, bool) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, 400, "invalid_parameter", "invalid query")
		return "", 0, false
	}
	for k, v := range q {
		if (k != "cursor" && k != "limit") || len(v) != 1 {
			writeError(w, 400, "invalid_parameter", "unsupported or repeated parameter")
			return "", 0, false
		}
	}
	limit := 100
	if values, ok := q["limit"]; ok {
		raw := values[0]
		valid := raw != ""
		for _, c := range raw {
			if c < '0' || c > '9' {
				valid = false
			}
		}
		limit, err = strconv.Atoi(raw)
		if !valid || err != nil || limit < 1 || limit > 1000 {
			writeError(w, 400, "invalid_parameter", "limit must be between 1 and 1000")
			return "", 0, false
		}
	}
	if _, exists := q["cursor"]; required && !exists {
		writeError(w, 400, "invalid_parameter", "cursor query parameter is required")
		return "", 0, false
	}
	cursor := q.Get("cursor")
	if _, ok := q["cursor"]; (ok || required) && cursor == "" {
		writeError(w, 400, "invalid_cursor", "cursor is required and must not be empty")
		return "", 0, false
	}
	return cursor, limit, true
}
func (h *Handler) handleCommonGet(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_parameter", "unsupported query parameter")
		return
	}
	scope, key := commonPath(r)
	item, err := h.provisioning.ReadCommon(r.Context(), scope, key)
	if err != nil {
		writeServiceError(r.Context(), w, err, "resource not found")
		return
	}
	w.Header().Set("ETag", item.ETag)
	writeJSON(w, 200, item.Representation)
}
func (h *Handler) handleCommonList(w http.ResponseWriter, r *http.Request) {
	cursor, limit, ok := commonPagination(w, r, false)
	if !ok {
		return
	}
	scope, _ := commonPath(r)
	page, err := h.provisioning.ListCommon(r.Context(), scope, cursor, limit)
	if err != nil {
		writeServiceError(r.Context(), w, err, "could not list resources")
		return
	}
	writeJSON(w, 200, page)
}
func (h *Handler) handleEventCursor(w http.ResponseWriter, r *http.Request) {
	cursor, err := h.provisioning.CaptureCommonCursor(r.Context())
	if err != nil {
		writeServiceError(r.Context(), w, err, "could not capture cursor")
		return
	}
	writeJSON(w, 200, map[string]string{"cursor": cursor})
}
func (h *Handler) handleEvents(w http.ResponseWriter, r *http.Request) {
	cursor, limit, ok := commonPagination(w, r, true)
	if !ok {
		return
	}
	page, err := h.provisioning.ReadCommonEvents(r.Context(), cursor, limit)
	if err != nil {
		writeServiceError(r.Context(), w, err, "could not read events")
		return
	}
	writeJSON(w, 200, page)
}
func (h *Handler) handleCommonPut(w http.ResponseWriter, r *http.Request) {
	scope, key := commonPath(r)
	required := []string{"slug", "name", "status"}
	var optional []string
	switch scope.Family {
	case "member":
		required = []string{"email", "tenant_role", "status"}
		optional = []string{"display_name", "identity"}
	case "organization_membership":
		required = []string{"role", "status"}
	case "tenant_domain":
		required = []string{"status"}
	}
	p, ok := decodeCommon(w, r, required, optional...)
	if !ok {
		return
	}
	var identity *model.Identity
	if raw, ok := p["identity"]; ok {
		identity = &model.Identity{}
		_ = json.Unmarshal([]byte(raw), identity)
	}
	condition, err := model.ParseMatchCondition(r.Header.Values("If-Match"))
	if err != nil {
		writeError(w, 400, "invalid_precondition", "malformed If-Match")
		return
	}
	item, err := h.provisioning.WriteCommon(r.Context(), scope, key, condition, func(s *service.ProvisioningService) error {
		var err error
		switch scope.Family {
		case "tenant":
			_, err = s.PutCommonTenant(r.Context(), model.TenantID(key), service.CommonResource{Slug: p["slug"], Name: p["name"], Status: model.Status(p["status"])})
		case "organization":
			_, err = s.PutCommonOrganization(r.Context(), model.TenantID(scope.TenantID), model.OrgID(key), service.CommonResource{Slug: p["slug"], Name: p["name"], Status: model.Status(p["status"])})
		case "member":
			_, err = s.PutCommonMember(r.Context(), model.TenantID(scope.TenantID), model.UserID(key), service.CommonMember{Identity: identity, Email: p["email"], DisplayName: p["display_name"], TenantRole: model.TenantRole(p["tenant_role"]), Status: model.Status(p["status"])})
		case "tenant_domain":
			_, err = s.PutCommonDomain(r.Context(), model.TenantID(scope.TenantID), key, model.Status(p["status"]))
		case "organization_membership":
			_, err = s.PutCommonMembership(r.Context(), model.TenantID(scope.TenantID), model.OrgID(scope.OrganizationID), model.UserID(key), service.CommonMembership{Role: model.MembershipRole(p["role"]), Status: model.Status(p["status"])})
		}
		return err
	})
	if err != nil {
		writeServiceError(r.Context(), w, err, "could not write resource")
		return
	}
	w.Header().Set("ETag", item.ETag)
	writeJSON(w, 200, item.Representation)
}
