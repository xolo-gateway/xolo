package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// businessRoute describes the representation of a business family: the
// fields a PUT must carry, and those that may be null.
type businessRoute struct {
	family             string
	required, nullable []string
	optional           []string
}

var businessRoutes = []businessRoute{
	{family: model.FamilyCustomRole, required: []string{"name", "description", "permissions", "model_grants"}},
	{family: model.FamilyApplication, required: []string{"name", "description", "active", "role_ids"}},
	{family: model.FamilyQuota,
		required: []string{"scope", "scope_id", "currency", "daily_budget", "monthly_budget", "yearly_budget"},
		nullable: []string{"daily_budget", "monthly_budget", "yearly_budget"}},
	{family: model.FamilyAlert, required: []string{"name", "description", "scope", "owner_id", "query", "aggregation", "window_seconds", "comparator", "threshold", "for_seconds", "enabled"}},
	{family: model.FamilyProvider,
		required: []string{"name", "type", "base_url", "active", "currency", "cloud_tier", "billing_mode", "subscription_plan", "retry_config", "rate_limit_config"},
		nullable: []string{"subscription_plan", "retry_config", "rate_limit_config"},
		optional: []string{"api_key"}},
}

// businessCollections names the collection of each business family.
var businessCollections = map[string]string{
	model.FamilyCustomRole:  "roles",
	model.FamilyApplication: "applications",
	model.FamilyQuota:       "quotas",
	model.FamilyAlert:       "alerts",
	model.FamilyProvider:    "providers",
}

// mountBusiness serves the business resources with the reads, conditions and
// events of the common contract. Quotas hang from the tenant, the others
// from an organization. They are not deleted through the contract, except
// custom roles, which keep their former DELETE route.
func (h *Handler) mountBusiness() {
	const (
		tenant = "/v1/xolo/tenants/{tenantID}"
		org    = tenant + "/organizations/{orgID}"
	)
	for _, route := range businessRoutes {
		collection := org + "/" + businessCollections[route.family]
		if route.family == model.FamilyQuota {
			collection = tenant + "/" + businessCollections[route.family]
		}
		h.mux.HandleFunc("GET "+collection, h.handleBusinessList(route))
		h.mux.HandleFunc("GET "+collection+"/{resourceID}", h.handleBusinessGet(route))
		h.mux.HandleFunc("PUT "+collection+"/{resourceID}", h.handleBusinessPut(route))
	}
	h.mux.HandleFunc("POST "+org+"/roles", h.handleCreateRole)
	h.mux.HandleFunc("DELETE "+org+"/roles/{resourceID}", h.handleDeleteRole)
	h.mux.HandleFunc("GET "+org+"/roles/builtin", h.handleBuiltinRoles)
	h.capabilities = append(h.capabilities, "business_resources")
}

// businessScope reads the parents of a business collection.
func businessScope(w http.ResponseWriter, r *http.Request, family string) (model.CommonScope, bool) {
	tenantID, ok := pathTenantID(w, r)
	if !ok {
		return model.CommonScope{}, false
	}
	scope := model.CommonScope{Family: family, TenantID: string(tenantID)}
	if family != model.FamilyQuota {
		orgID, err := model.ParseOrgID(r.PathValue("orgID"))
		if err != nil {
			writeInvalidID(w)
			return model.CommonScope{}, false
		}
		scope.OrganizationID = string(orgID)
	}
	return scope, true
}

// businessTarget reads the parents and the key of a business resource: a
// UUID, or the xid of a resource created locally.
func businessTarget(w http.ResponseWriter, r *http.Request, family string) (model.CommonScope, string, bool) {
	scope, ok := businessScope(w, r, family)
	if !ok {
		return scope, "", false
	}
	key, _, err := model.ParseBusinessKey(r.PathValue("resourceID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidParameter, "resource keys are canonical lowercase UUIDs or local identifiers")
		return scope, "", false
	}
	return scope, key, true
}

func (h *Handler) handleBusinessList(route businessRoute) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scope, ok := businessScope(w, r, route.family)
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

func (h *Handler) handleBusinessGet(route businessRoute) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scope, key, ok := businessTarget(w, r, route.family)
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

func (h *Handler) handleBusinessPut(route businessRoute) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scope, key, ok := businessTarget(w, r, route.family)
		if !ok || !noQuery(w, r) {
			return
		}
		condition, ok := commonCondition(w, r)
		if !ok {
			return
		}
		raw, ok := decodeBusiness(w, r, route)
		if !ok {
			return
		}
		item, err := h.putBusiness(r.Context(), scope, key, condition, raw)
		if err != nil {
			if errors.Is(err, errInvalidRepresentation) {
				writeError(w, http.StatusBadRequest, codeInvalidJSON, "invalid representation")
				return
			}
			writeServiceError(r.Context(), w, err, "could not write resource")
			return
		}
		w.Header().Set("ETag", item.ETag)
		writeJSON(w, http.StatusOK, item.Representation)
	}
}

// errInvalidRepresentation reports a field of the wrong type, refused before
// reaching the service.
var errInvalidRepresentation = errors.New("invalid representation")

// putBusiness decodes the representation of the family and writes it.
func (h *Handler) putBusiness(ctx context.Context, scope model.CommonScope, key string, condition model.MatchCondition, raw []byte) (model.CommonItem, error) {
	tenantID, orgID := model.TenantID(scope.TenantID), model.OrgID(scope.OrganizationID)
	decode := func(v any) error {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(v) != nil {
			return errInvalidRepresentation
		}
		return nil
	}
	switch scope.Family {
	case model.FamilyCustomRole:
		var v model.CustomRoleSettings
		if err := decode(&v); err != nil {
			return model.CommonItem{}, err
		}
		return h.provisioning.PutCustomRole(ctx, tenantID, orgID, key, condition, v)
	case model.FamilyApplication:
		var v model.ApplicationSettings
		if err := decode(&v); err != nil {
			return model.CommonItem{}, err
		}
		return h.provisioning.PutApplication(ctx, tenantID, orgID, key, condition, v)
	case model.FamilyQuota:
		var v model.QuotaSettings
		if err := decode(&v); err != nil {
			return model.CommonItem{}, err
		}
		return h.provisioning.PutQuota(ctx, tenantID, key, condition, v)
	case model.FamilyAlert:
		var v model.AlertSettings
		if err := decode(&v); err != nil {
			return model.CommonItem{}, err
		}
		return h.provisioning.PutAlert(ctx, tenantID, orgID, key, condition, v)
	default:
		var v model.ProviderSettings
		if err := decode(&v); err != nil {
			return model.CommonItem{}, err
		}
		return h.provisioning.PutProvider(ctx, tenantID, orgID, key, condition, v)
	}
}

// decodeBusiness reads a complete business representation: one JSON object
// carrying every required field, null only where the family allows it, and
// no unknown field. Field types are checked by the typed decoding.
func decodeBusiness(w http.ResponseWriter, r *http.Request, route businessRoute) ([]byte, bool) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, codeUnsupportedMediaType, "application/json is required")
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodySize+1))
	if err != nil || len(raw) > maxRequestBodySize || !utf8.Valid(raw) {
		writeError(w, http.StatusBadRequest, codeInvalidJSON, "invalid JSON body")
		return nil, false
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidJSON, "expected one JSON object")
		return nil, false
	}
	if object == nil {
		writeError(w, http.StatusBadRequest, codeInvalidRepresentation, "null is not a representation")
		return nil, false
	}
	known := map[string]bool{}
	for _, field := range append(append([]string{}, route.required...), route.optional...) {
		known[field] = true
	}
	nullable := map[string]bool{}
	for _, field := range route.nullable {
		nullable[field] = true
	}
	for field, value := range object {
		if !known[field] {
			writeError(w, http.StatusBadRequest, codeInvalidJSON, "unknown field "+field)
			return nil, false
		}
		if !nullable[field] && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			writeError(w, http.StatusBadRequest, codeInvalidRepresentation, "field "+field+" can not be null")
			return nil, false
		}
	}
	for _, field := range route.required {
		if _, ok := object[field]; !ok {
			writeError(w, http.StatusBadRequest, codeInvalidRepresentation, "field "+field+" is required")
			return nil, false
		}
	}
	return raw, true
}

// handleCreateRole creates a custom role under a key chosen by the server.
func (h *Handler) handleCreateRole(w http.ResponseWriter, r *http.Request) {
	scope, ok := businessScope(w, r, model.FamilyCustomRole)
	if !ok || !noQuery(w, r) {
		return
	}
	raw, ok := decodeBusiness(w, r, businessRoutes[0])
	if !ok {
		return
	}
	item, err := h.putBusiness(r.Context(), scope, uuid.NewString(), model.MatchCondition{}, raw)
	if err != nil {
		if errors.Is(err, errInvalidRepresentation) {
			writeError(w, http.StatusBadRequest, codeInvalidJSON, "invalid representation")
			return
		}
		writeServiceError(r.Context(), w, err, "could not create role")
		return
	}
	w.Header().Set("ETag", item.ETag)
	writeJSON(w, http.StatusCreated, item)
}

func (h *Handler) handleDeleteRole(w http.ResponseWriter, r *http.Request) {
	scope, key, ok := businessTarget(w, r, model.FamilyCustomRole)
	if !ok || !noQuery(w, r) {
		return
	}
	err := h.provisioning.DeleteRole(r.Context(), model.TenantID(scope.TenantID), model.OrgID(scope.OrganizationID), model.RoleID(key))
	if err != nil {
		writeServiceError(r.Context(), w, err, "role not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type builtinRoleDTO struct {
	ID          string `json:"id"`
	BuiltinKind string `json:"builtin_kind"`
	Name        string `json:"name"`
}

// handleBuiltinRoles lists the builtin roles of the organization: they are
// not part of the custom role collection, but member role assignments
// designate them.
func (h *Handler) handleBuiltinRoles(w http.ResponseWriter, r *http.Request) {
	scope, ok := businessScope(w, r, model.FamilyCustomRole)
	if !ok || !noQuery(w, r) {
		return
	}
	roles, err := h.provisioning.ListRoles(r.Context(), model.TenantID(scope.TenantID), model.OrgID(scope.OrganizationID))
	if err != nil {
		writeServiceError(r.Context(), w, err, "organization not found")
		return
	}
	items := []builtinRoleDTO{}
	for _, role := range roles {
		if role.Builtin() {
			items = append(items, builtinRoleDTO{ID: string(role.ID()), BuiltinKind: role.BuiltinKind(), Name: role.Name()})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
