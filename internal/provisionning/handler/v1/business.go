package v1

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

func (h *Handler) WithBusiness() *Handler {
	h.businessEnabled = true
	base := "/v1/xolo/tenants/{tenantID}"
	for _, collection := range []string{"applications", "alerts", "providers", "quotas"} {
		path := base + "/organizations/{orgID}/" + collection
		if collection == "quotas" {
			path = base + "/quotas"
		}
		h.mux.HandleFunc("GET "+path, h.handleBusinessList)
		h.mux.HandleFunc("GET "+path+"/{resourceID}", h.handleBusinessGet)
		h.mux.HandleFunc("PUT "+path+"/{resourceID}", h.handleBusinessPut)
	}
	return h
}
func businessPath(r *http.Request) (model.CommonScope, string) {
	scope := model.CommonScope{TenantID: r.PathValue("tenantID"), OrganizationID: r.PathValue("orgID")}
	key := r.PathValue("resourceID")
	for collection, family := range map[string]string{"roles": "custom_role", "applications": "application", "quotas": "quota", "alerts": "alert", "providers": "provider"} {
		if strings.Contains(r.Pattern, "/"+collection) {
			scope.Family = family
		}
	}
	if scope.Family == "custom_role" {
		key = r.PathValue("roleID")
	}
	return scope, key
}
func (h *Handler) handleBusinessGet(w http.ResponseWriter, r *http.Request) {
	if !lifecycleRequest(w, r, false) {
		return
	}
	scope, key := businessPath(r)
	item, err := h.provisioning.ReadCommon(r.Context(), scope, key)
	if err != nil {
		writeServiceError(r.Context(), w, err, "resource not found")
		return
	}
	w.Header().Set("ETag", item.ETag)
	writeJSON(w, 200, item.Representation)
}
func (h *Handler) handleBusinessList(w http.ResponseWriter, r *http.Request) {
	cursor, limit, ok := commonPagination(w, r, false)
	if !ok {
		return
	}
	scope, _ := businessPath(r)
	page, err := h.provisioning.ListCommon(r.Context(), scope, cursor, limit)
	if err != nil {
		writeServiceError(r.Context(), w, err, "could not list resources")
		return
	}
	writeJSON(w, 200, page)
}
func (h *Handler) handleBusinessPut(w http.ResponseWriter, r *http.Request) {
	if !lifecycleRequest(w, r, true) {
		return
	}
	c, ok := lifecycleCondition(w, r)
	if !ok {
		return
	}
	scope, key := businessPath(r)
	typ, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || typ != "application/json" {
		writeError(w, 415, "unsupported_media_type", "application/json required")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBodySize))
	if err != nil {
		writeError(w, 400, "invalid_representation", "invalid body")
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		writeError(w, 400, "invalid_representation", "object required")
		return
	}
	var p model.BusinessSettings
	var target any
	var required, nullable []string
	switch scope.Family {
	case "custom_role":
		p.Role = &model.CustomRoleSettings{}
		target = p.Role
		required = []string{"name", "description", "permissions", "model_grants"}
	case "application":
		p.Application = &model.ApplicationSettings{}
		target = p.Application
		required = []string{"name", "description", "active", "role_ids"}
	case "quota":
		p.Quota = &model.QuotaSettings{}
		target = p.Quota
		required = []string{"scope", "scope_id", "currency", "daily_budget", "monthly_budget", "yearly_budget"}
		nullable = []string{"daily_budget", "monthly_budget", "yearly_budget"}
	case "alert":
		p.Alert = &model.AlertSettings{}
		target = p.Alert
		required = []string{"name", "description", "scope", "owner_id", "query", "aggregation", "window_seconds", "comparator", "threshold", "for_seconds", "enabled"}
	case "provider":
		p.Provider = &model.ProviderSettings{}
		target = p.Provider
		required = []string{"name", "type", "base_url", "active", "currency", "cloud_tier", "billing_mode", "subscription_plan", "retry_config", "rate_limit_config"}
		nullable = []string{"subscription_plan", "retry_config", "rate_limit_config"}
	default:
		writeError(w, 404, "not_found", "unknown family")
		return
	}
	allowNull := map[string]bool{}
	for _, f := range nullable {
		allowNull[f] = true
	}
	for _, f := range required {
		if _, ok := fields[f]; !ok {
			writeError(w, 400, "invalid_representation", "complete representation required")
			return
		}
	}
	for f, value := range fields {
		if !allowNull[f] && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			writeError(w, 400, "invalid_representation", "null is not allowed")
			return
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		writeError(w, 400, "invalid_representation", "invalid representation")
		return
	}
	item, err := h.provisioning.PutBusiness(r.Context(), scope, key, c, p)
	if err != nil {
		writeServiceError(r.Context(), w, err, "could not write resource")
		return
	}
	w.Header().Set("ETag", item.ETag)
	writeJSON(w, 200, item.Representation)
}
