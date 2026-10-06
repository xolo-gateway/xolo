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
// The common contract lives at the root of /v1: idempotent PUTs of tenants,
// domains, organizations, members and memberships, identified by
// client-chosen UUIDs. Operations specific to Xolo live under /v1/xolo.
//
// It is pure transport: it decodes and validates the request, delegates to the
// provisioning service, converts the result to an API representation and maps
// domain errors to HTTP statuses. It holds no store and no business rule.
type Handler struct {
	provisioning *service.ProvisioningService
	mux          *http.ServeMux
	version      string
}

func NewHandler(provisioning *service.ProvisioningService, version string) *Handler {
	h := &Handler{
		provisioning: provisioning,
		mux:          http.NewServeMux(),
		version:      version,
	}

	h.mux.HandleFunc("GET /v1/manifest", h.handleManifest)
	h.mux.HandleFunc("PUT /v1/tenants/{tenantID}", h.handlePutTenant)
	h.mux.HandleFunc("PUT /v1/tenants/{tenantID}/domains/{hostname}", h.handlePutDomain)
	h.mux.HandleFunc("PUT /v1/tenants/{tenantID}/organizations/{orgID}", h.handlePutOrganization)
	h.mux.HandleFunc("PUT /v1/tenants/{tenantID}/members/{memberID}", h.handlePutTenantMember)
	h.mux.HandleFunc("PUT /v1/tenants/{tenantID}/organizations/{orgID}/members/{memberID}", h.handlePutOrgMember)

	const ext = "/v1/xolo"

	h.mux.HandleFunc("GET "+ext+"/healthz", h.handleHealthz)
	h.mux.HandleFunc("GET "+ext+"/permissions", h.handlePermissions)

	h.mux.HandleFunc("GET "+ext+"/tenants", h.handleListTenants)
	h.mux.HandleFunc("GET "+ext+"/tenants/{tenantID}", h.handleGetTenant)
	h.mux.HandleFunc("PATCH "+ext+"/tenants/{tenantID}", h.handleUpdateTenant)

	const orgPath = ext + "/tenants/{tenantID}/organizations"

	h.mux.HandleFunc("GET "+orgPath, h.handleListOrganizations)
	h.mux.HandleFunc("GET "+orgPath+"/{orgID}", h.handleGetOrganization)
	h.mux.HandleFunc("PATCH "+orgPath+"/{orgID}", h.handleUpdateOrganization)

	h.mux.HandleFunc("GET "+orgPath+"/{orgID}/members", h.handleListMembers)
	h.mux.HandleFunc("GET "+orgPath+"/{orgID}/members/{membershipID}", h.handleGetMember)
	h.mux.HandleFunc("PUT "+orgPath+"/{orgID}/members/{membershipID}/roles", h.handleSetMemberRoles)

	h.mux.HandleFunc("GET "+orgPath+"/{orgID}/roles", h.handleListRoles)
	h.mux.HandleFunc("POST "+orgPath+"/{orgID}/roles", h.handleCreateRole)
	h.mux.HandleFunc("GET "+orgPath+"/{orgID}/roles/{roleID}", h.handleGetRole)
	h.mux.HandleFunc("PUT "+orgPath+"/{orgID}/roles/{roleID}", h.handleUpdateRole)
	h.mux.HandleFunc("DELETE "+orgPath+"/{orgID}/roles/{roleID}", h.handleDeleteRole)

	// Users hang from the tenant: (provider, subject) is only unique within
	// one, so an instance-wide upsert would have no key to act on.
	h.mux.HandleFunc("GET "+ext+"/tenants/{tenantID}/users", h.handleListUsers)
	h.mux.HandleFunc("PUT "+ext+"/tenants/{tenantID}/users", h.handlePutUser)
	h.mux.HandleFunc("GET "+ext+"/tenants/{tenantID}/users/{userID}", h.handleGetUser)

	// Catch-all so an unknown route answers with the same error envelope as
	// everything else.
	h.mux.HandleFunc("/", h.handleNotFound)

	return h
}

// ServeHTTP refuses query parameters on the common contract, which defines
// none: a client relying on one must be told rather than silently ignored.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" && !strings.HasPrefix(r.URL.Path, "/v1/xolo/") {
		writeError(w, http.StatusBadRequest, codeInvalidParameter, "the common contract defines no query parameter")
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handlePermissions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"groups": newPermissionCatalogDTO()})
}

// routableMethods are the methods this API uses. They are probed to tell an
// unknown resource apart from a known one addressed with the wrong method.
var routableMethods = []string{
	http.MethodGet,
	http.MethodPost,
	http.MethodPut,
	http.MethodPatch,
	http.MethodDelete,
}

// handleNotFound serves every request no route matched. Registering it makes
// the catch-all shadow the mux's own 404 and 405 replies, so it re-derives the
// distinction to keep a single error envelope across the whole API.
func (h *Handler) handleNotFound(w http.ResponseWriter, r *http.Request) {
	allowed := make([]string, 0, len(routableMethods))

	for _, method := range routableMethods {
		if method == r.Method {
			continue
		}

		probe := r.Clone(r.Context())
		probe.Method = method

		if _, pattern := h.mux.Handler(probe); pattern != "" && pattern != "/" {
			allowed = append(allowed, method)
		}
	}

	if len(allowed) > 0 {
		w.Header().Set("Allow", strings.Join(allowed, ", "))
		writeError(w, http.StatusMethodNotAllowed, codeMethodNotAllowed, "method not allowed for this resource")

		return
	}

	writeError(w, http.StatusNotFound, codeNotFound, "unknown resource")
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
