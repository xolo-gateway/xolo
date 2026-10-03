package v1

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"

	"github.com/xolo-gateway/xolo/internal/core/service"
)

// WithWebhooks registers the separately discoverable Xolo extension. All routes
// remain behind the dedicated listener's instance-wide certificate authority.
func (h *Handler) WithWebhooks(s *service.WebhookService) *Handler {
	const path = "/v1/xolo/tenants/{tenantID}/webhooks"
	h.webhooksEnabled = true
	h.mux.HandleFunc("GET /v1/xolo/webhooks/status", func(w http.ResponseWriter, r *http.Request) {
		stats, err := s.Stats(r.Context())
		if err != nil {
			writeServiceError(r.Context(), w, err, "could not read webhook status")
			return
		}
		writeJSON(w, 200, stats)
	})
	h.mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
		items, err := s.List(r.Context(), r.PathValue("tenantID"))
		if err != nil {
			writeServiceError(r.Context(), w, err, "could not list subscriptions")
			return
		}
		writeJSON(w, 200, map[string]any{"items": items})
	})
	h.mux.HandleFunc("GET "+path+"/{subscriptionID}", func(w http.ResponseWriter, r *http.Request) {
		item, err := s.Get(r.Context(), r.PathValue("tenantID"), r.PathValue("subscriptionID"))
		if err != nil {
			writeServiceError(r.Context(), w, err, "subscription not found")
			return
		}
		writeJSON(w, 200, item)
	})
	h.mux.HandleFunc("PUT "+path+"/{subscriptionID}", func(w http.ResponseWriter, r *http.Request) {
		var input service.WebhookInput
		if !decodeWebhook(w, r, &input) {
			return
		}
		item, err := s.Put(r.Context(), r.PathValue("tenantID"), r.PathValue("subscriptionID"), input)
		if err != nil {
			writeServiceError(r.Context(), w, err, "could not write subscription")
			return
		}
		writeJSON(w, 200, item)
	})
	h.mux.HandleFunc("DELETE "+path+"/{subscriptionID}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Delete(r.Context(), r.PathValue("tenantID"), r.PathValue("subscriptionID")); err != nil {
			writeServiceError(r.Context(), w, err, "could not remove subscription")
			return
		}
		w.WriteHeader(204)
	})
	h.mux.HandleFunc("POST "+path+"/{subscriptionID}/reset", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			AcknowledgeLoss bool `json:"acknowledge_loss"`
		}
		if !decodeWebhook(w, r, &p) {
			return
		}
		if !p.AcknowledgeLoss {
			writeError(w, 400, "invalid_representation", "explicit acknowledgment required")
			return
		}
		if err := s.Reset(r.Context(), r.PathValue("tenantID"), r.PathValue("subscriptionID")); err != nil {
			writeServiceError(r.Context(), w, err, "could not reset subscription")
			return
		}
		w.WriteHeader(204)
	})
	h.mux.HandleFunc("GET "+path+"/{subscriptionID}/deliveries", func(w http.ResponseWriter, r *http.Request) {
		items, err := s.Deliveries(r.Context(), r.PathValue("tenantID"), r.PathValue("subscriptionID"))
		if err != nil {
			writeServiceError(r.Context(), w, err, "could not read deliveries")
			return
		}
		writeJSON(w, 200, map[string]any{"items": items})
	})
	return h
}
func decodeWebhook(w http.ResponseWriter, r *http.Request, out any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(w, 415, "unsupported_media_type", "application/json required")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBodySize))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		writeError(w, 400, "invalid_json", "invalid webhook representation")
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(w, 400, "invalid_json", "expected one JSON object")
		return false
	}
	return true
}
