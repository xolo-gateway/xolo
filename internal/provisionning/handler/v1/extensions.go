package v1

import "net/http"

func (h *Handler) handleExtensions(w http.ResponseWriter, r *http.Request) {
	extensions := []any{
		map[string]any{"name": "identity", "version": "1", "scope": "tenant", "path": "/v1/tenants/{tenantID}/members/{memberID}", "issuer": "exact HTTPS URL", "subject": "exact UTF-8, 1..255 bytes"},
		map[string]any{"name": "ownership", "version": "1", "policy": h.provisioning.OwnershipPolicy()},
		map[string]any{"name": "adoption", "version": "1", "path": "/v1/xolo/export", "format": "xolo-adoption/1"},
	}
	if h.businessEnabled {
		extensions = append(extensions, map[string]any{"name": "business_resources", "version": "1", "families": []string{"custom_role", "application", "quota", "alert", "provider"}, "organization_path": "/v1/xolo/tenants/{tenantID}/organizations/{orgID}", "quota_path": "/v1/xolo/tenants/{tenantID}/quotas", "credential_policy": "write_only", "write_authority": "family_ownership", "events": []string{"created", "updated", "deleted"}})
	}
	if h.lifecycle != nil {
		extensions = append(extensions, map[string]any{"name": "lifecycle", "version": "1", "resources": []string{"tenant", "organization", "member"}, "read_status": "deleted", "delete": "scheduled", "confirmation": "/purge-confirmation", "export": "/deletion/export", "format": "xolo-deletion/1", "purge": "confirmed_after_retention", "retired_uuid_reuse": false, "events": []string{"deleted", "export_confirmed", "purged"}, "immediate_delete": []string{"tenant_domain", "organization_membership"}})
	}
	if h.webhooksEnabled {
		extensions = append(extensions, map[string]any{"name": "webhooks", "version": "1", "scope": "tenant", "owner": h.provisioning.OwnershipPolicy()["subscription"], "path": "/v1/xolo/tenants/{tenantID}/webhooks"})
	}
	writeJSON(w, 200, map[string]any{"extensions": extensions})
}
func (h *Handler) handleExport(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_parameter", "unsupported query parameter")
		return
	}
	raw, err := h.provisioning.ExportAdoption(r.Context())
	if err != nil {
		writeServiceError(r.Context(), w, err, "could not export inventory")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", `attachment; filename="xolo-adoption.json"`)
	w.WriteHeader(200)
	_, _ = w.Write(raw)
}
