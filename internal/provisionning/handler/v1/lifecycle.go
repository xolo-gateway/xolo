package v1

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

func (h *Handler) WithLifecycle(s *service.LifecycleService) *Handler {
	h.lifecycle = s
	for _, path := range []string{"/v1/tenants/{tenantID}", "/v1/tenants/{tenantID}/organizations/{orgID}", "/v1/tenants/{tenantID}/members/{memberID}"} {
		h.mux.HandleFunc("DELETE "+path, h.handleLifecycleDelete)
		h.mux.HandleFunc("GET "+path+"/deletion", h.handleDeletion)
		h.mux.HandleFunc("GET "+path+"/deletion/export", h.handleDeletionExport)
		h.mux.HandleFunc("POST "+path+"/purge-confirmation", h.handleDeletionConfirm)
	}
	for _, path := range []string{"/v1/tenants/{tenantID}/domains/{hostname}", "/v1/tenants/{tenantID}/organizations/{orgID}/members/{memberID}"} {
		h.mux.HandleFunc("DELETE "+path, h.handleLifecycleDelete)
	}
	return h
}
func lifecycleRequest(w http.ResponseWriter, r *http.Request, body bool) bool {
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_parameter", "unsupported query parameter")
		return false
	}
	if !body {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1))
		if err != nil || len(raw) > 0 {
			writeError(w, 400, "invalid_representation", "empty body required")
			return false
		}
	}
	return true
}
func lifecycleCondition(w http.ResponseWriter, r *http.Request) (model.MatchCondition, bool) {
	c, err := model.ParseMatchCondition(r.Header.Values("If-Match"))
	if err != nil {
		writeError(w, 400, "invalid_precondition", "malformed If-Match")
		return c, false
	}
	return c, true
}
func (h *Handler) handleLifecycleDelete(w http.ResponseWriter, r *http.Request) {
	if !lifecycleRequest(w, r, false) {
		return
	}
	c, ok := lifecycleCondition(w, r)
	if !ok {
		return
	}
	scope, key := commonPath(r)
	d, err := h.lifecycle.Delete(r.Context(), scope, key, c)
	if err != nil {
		writeServiceError(r.Context(), w, err, "could not delete resource")
		return
	}
	if d == nil {
		w.WriteHeader(204)
		return
	}
	w.Header().Set("ETag", d.ETag)
	writeJSON(w, 202, d)
}
func (h *Handler) handleDeletion(w http.ResponseWriter, r *http.Request) {
	if !lifecycleRequest(w, r, false) {
		return
	}
	scope, key := commonPath(r)
	d, err := h.lifecycle.Read(r.Context(), scope, key)
	if err != nil {
		writeServiceError(r.Context(), w, err, "deletion not found")
		return
	}
	w.Header().Set("ETag", d.ETag)
	writeJSON(w, 200, d)
}
func (h *Handler) handleDeletionExport(w http.ResponseWriter, r *http.Request) {
	if !lifecycleRequest(w, r, false) {
		return
	}
	scope, key := commonPath(r)
	raw, err := h.lifecycle.Export(r.Context(), scope, key)
	if err != nil {
		writeServiceError(r.Context(), w, err, "could not export frozen scope")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", `attachment; filename="xolo-deletion.json"`)
	w.WriteHeader(200)
	_, _ = w.Write(raw)
}
func (h *Handler) handleDeletionConfirm(w http.ResponseWriter, r *http.Request) {
	if !lifecycleRequest(w, r, true) {
		return
	}
	c, ok := lifecycleCondition(w, r)
	if !ok {
		return
	}
	typ, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || typ != "application/json" {
		writeError(w, 415, "unsupported_media_type", "application/json required")
		return
	}
	var p *struct {
		SHA256 string `json:"export_sha256"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil || p == nil {
		writeError(w, 400, "invalid_representation", "invalid receipt")
		return
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		writeError(w, 400, "invalid_representation", "one receipt required")
		return
	}
	scope, key := commonPath(r)
	d, err := h.lifecycle.Confirm(r.Context(), scope, key, c, p.SHA256)
	if err != nil {
		writeServiceError(r.Context(), w, err, "receipt refused")
		return
	}
	w.Header().Set("ETag", d.ETag)
	writeJSON(w, 200, d)
}
