package v1

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

const (
	defaultPageLimit = 50
	maxPageLimit     = 200

	// maxRequestBodySize caps the payloads this API accepts. Its resources are
	// small: anything larger is a mistake.
	maxRequestBodySize = 1 << 20
)

// Handler exposes the provisioning operations as a versioned, resource
// oriented HTTP API.
//
// It is pure transport: it decodes and validates the request, delegates to the
// provisioning service, converts the result to an API representation and maps
// domain errors to HTTP statuses. It holds no store and no business rule.
type Handler struct {
	businessEnabled bool
	lifecycle       *service.LifecycleService
	webhooksEnabled bool
	provisioning    *service.ProvisioningService
	mux             *http.ServeMux
	version         string
}

func NewHandler(provisioning *service.ProvisioningService, version ...string) *Handler {
	h := &Handler{
		provisioning: provisioning,
		mux:          http.NewServeMux(),
	}

	h.version = "development"
	if len(version) > 0 && version[0] != "" {
		h.version = version[0]
	}
	h.mux.HandleFunc("GET /v1/manifest", h.handleManifest)
	h.mux.HandleFunc("GET /v1/xolo/extensions", h.handleExtensions)
	h.mux.HandleFunc("GET /v1/xolo/export", h.handleExport)
	h.mux.HandleFunc("PUT /v1/tenants/{tenantID}", h.handleCommonPut)
	h.mux.HandleFunc("PUT /v1/tenants/{tenantID}/domains/{hostname}", h.handleCommonPut)
	h.mux.HandleFunc("PUT /v1/tenants/{tenantID}/organizations/{orgID}", h.handleCommonPut)
	h.mux.HandleFunc("PUT /v1/tenants/{tenantID}/members/{memberID}", h.handleCommonPut)
	h.mux.HandleFunc("PUT /v1/tenants/{tenantID}/organizations/{orgID}/members/{memberID}", h.handleCommonPut)
	for _, path := range []string{"/v1/tenants/{tenantID}", "/v1/tenants/{tenantID}/domains/{hostname}", "/v1/tenants/{tenantID}/organizations/{orgID}", "/v1/tenants/{tenantID}/members/{memberID}", "/v1/tenants/{tenantID}/organizations/{orgID}/members/{memberID}"} {
		h.mux.HandleFunc("GET "+path, h.handleCommonGet)
	}
	for _, path := range []string{"/v1/tenants", "/v1/tenants/{tenantID}/domains", "/v1/tenants/{tenantID}/organizations", "/v1/tenants/{tenantID}/members", "/v1/tenants/{tenantID}/organizations/{orgID}/members"} {
		h.mux.HandleFunc("GET "+path, h.handleCommonList)
	}
	h.mux.HandleFunc("GET /v1/events/cursor", h.handleEventCursor)
	h.mux.HandleFunc("GET /v1/events", h.handleEvents)
	// Xolo-specific operations have a separate extension namespace.
	const ext = "/v1/xolo"
	h.mux.HandleFunc("GET "+ext+"/healthz", h.handleHealthz)
	h.mux.HandleFunc("GET "+ext+"/permissions", h.handlePermissions)
	h.mux.HandleFunc("GET "+ext+"/tenants/{tenantID}", h.handleGetTenant)
	h.mux.HandleFunc("PATCH "+ext+"/tenants/{tenantID}", h.handleUpdateTenant)
	const orgPath = ext + "/tenants/{tenantID}/organizations/{orgID}"
	h.mux.HandleFunc("GET "+orgPath, h.handleGetOrganization)
	h.mux.HandleFunc("PATCH "+orgPath, h.handleUpdateOrganization)
	h.mux.HandleFunc("GET "+orgPath+"/roles", h.handleListRoles)
	h.mux.HandleFunc("POST "+orgPath+"/roles", h.handleCreateRole)
	h.mux.HandleFunc("GET "+orgPath+"/roles/{roleID}", h.handleGetRole)
	h.mux.HandleFunc("PUT "+orgPath+"/roles/{roleID}", h.handleUpdateRole)
	h.mux.HandleFunc("DELETE "+orgPath+"/roles/{roleID}", h.handleDeleteRole)
	h.mux.HandleFunc("PUT "+orgPath+"/members/{membershipID}/roles", h.handleSetMemberRoles)
	h.mux.HandleFunc("PUT "+ext+"/tenants/{tenantID}/users", h.handlePutUser)
	h.mux.HandleFunc("GET "+ext+"/tenants/{tenantID}/users", h.handleListUsers)

	// Catch-all so an unknown route answers with the same error envelope as
	// everything else.
	h.mux.HandleFunc("/", h.handleNotFound)

	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" && !strings.HasPrefix(r.URL.Path, "/v1/xolo/") && (r.Method != "GET" || r.URL.Path == "/v1/manifest" || r.URL.Path == "/v1/events/cursor") {
		writeError(w, 400, "invalid_parameter", "unsupported query parameter")
		return
	}
	if r.Method == http.MethodHead {
		h.handleNotFound(w, r)
		return
	}
	r = r.WithContext(model.WithWriteAuthority(r.Context(), model.OwnerControlPlane))
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handlePermissions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"groups": newPermissionCatalogDTO()})
}

// Unsupported methods and unknown routes share the contract error envelope.
func (h *Handler) handleNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, codeNotFound, "unknown resource or method")
}

// decodeJSON reads a JSON request body. Unknown fields are rejected: a
// declarative client that misspells a field must be told, not silently ignored.
func decodeJSON(w http.ResponseWriter, r *http.Request, payload any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBodySize))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(payload); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "request body is not a valid JSON document for this endpoint")
		return false
	}

	return true
}

// pagination reads the page/limit query parameters. Pages are 1-based on the
// wire and 0-based in the domain.
func pagination(r *http.Request) (page, limit int, ok bool) {
	page, limit = 1, defaultPageLimit

	if raw := r.URL.Query().Get("page"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			return 0, 0, false
		}
		page = parsed
	}

	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxPageLimit {
			return 0, 0, false
		}
		limit = parsed
	}

	return page, limit, true
}

// resolveTenant reads the {tenantID} path segment and loads the tenant. It
// writes the error response itself and reports whether the caller may proceed,
// so every nested handler starts from a tenant that is known to exist.
func (h *Handler) resolveTenant(w http.ResponseWriter, r *http.Request) (model.Tenant, bool) {
	ctx := r.Context()

	tenant, err := h.provisioning.GetTenant(ctx, model.TenantID(r.PathValue("tenantID")))
	if err != nil {
		writeServiceError(ctx, w, err, "tenant not found")
		return nil, false
	}

	return tenant, true
}

// resolveOrganization loads the {orgID} of the {tenantID}. An organization
// belonging to another tenant is reported as not found.
func (h *Handler) resolveOrganization(w http.ResponseWriter, r *http.Request) (model.Organization, bool) {
	ctx := r.Context()

	tenant, ok := h.resolveTenant(w, r)
	if !ok {
		return nil, false
	}

	org, err := h.provisioning.GetOrganization(ctx, tenant.ID(), model.OrgID(r.PathValue("orgID")))
	if err != nil {
		writeServiceError(ctx, w, err, "organization not found")
		return nil, false
	}

	return org, true
}

func writeInvalidPagination(w http.ResponseWriter) {
	writeError(w, http.StatusBadRequest, codeInvalidRequest,
		"page must be a positive integer and limit must be between 1 and "+strconv.Itoa(maxPageLimit))
}
