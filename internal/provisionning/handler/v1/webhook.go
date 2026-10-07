package v1

import (
	"net/http"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

// webhookDTO never carries the secrets, only how many there are.
type webhookDTO struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenantId"`
	Destination string    `json:"destination"`
	Events      []string  `json:"events"`
	Enabled     bool      `json:"enabled"`
	State       string    `json:"state"`
	Position    int64     `json:"position"`
	SecretCount int       `json:"secretCount"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func toWebhookDTO(w model.WebhookSubscription) webhookDTO {
	return webhookDTO{ID: string(w.ID), TenantID: string(w.TenantID), Destination: w.Destination, Events: w.Events, Enabled: w.Enabled, State: w.State, Position: w.Position, SecretCount: w.SecretCount, UpdatedAt: w.UpdatedAt}
}

// webhookInputDTO is a subscription write. Omitted secrets keep the stored
// ones.
type webhookInputDTO struct {
	Destination string   `json:"destination"`
	Events      []string `json:"events"`
	Enabled     *bool    `json:"enabled"`
	Secrets     []string `json:"secrets"`
}

// webhookDeliveryDTO carries neither the event nor the response.
type webhookDeliveryDTO struct {
	ID          string     `json:"id"`
	EventID     string     `json:"eventId"`
	Sequence    int64      `json:"sequence"`
	State       string     `json:"state"`
	Attempts    int        `json:"attempts"`
	NextAttempt time.Time  `json:"nextAttempt"`
	CreatedAt   time.Time  `json:"createdAt"`
	FinishedAt  *time.Time `json:"finishedAt"`
	Diagnostic  string     `json:"diagnostic"`
	StatusCode  int        `json:"statusCode"`
}

// WithWebhooks serves the webhook subscriptions of each tenant, and announces
// the capability in the manifest.
func WithWebhooks(webhooks *service.WebhookService) HandlerOption {
	return func(h *Handler) { h.webhooks = webhooks }
}

func (h *Handler) mountWebhooks() {
	if h.webhooks == nil {
		return
	}
	h.capabilities = append(h.capabilities, "webhooks")
	const (
		webhooks     = "/v1/xolo/tenants/{tenantID}/webhooks"
		subscription = webhooks + "/{subscriptionID}"
	)
	h.mux.HandleFunc("GET "+webhooks, h.handleListWebhooks)
	h.mux.HandleFunc("GET "+subscription, h.handleGetWebhook)
	h.mux.HandleFunc("PUT "+subscription, h.handlePutWebhook)
	h.mux.HandleFunc("DELETE "+subscription, h.handleDeleteWebhook)
	h.mux.HandleFunc("POST "+subscription+"/reset", h.handleResetWebhook)
	h.mux.HandleFunc("GET "+subscription+"/deliveries", h.handleWebhookDeliveries)
}

// webhookTarget reads the tenant and subscription IDs of the path.
func webhookTarget(w http.ResponseWriter, r *http.Request) (model.TenantID, model.WebhookID, bool) {
	if !noQuery(w, r) {
		return "", "", false
	}
	tenant, ok := pathTenantID(w, r)
	if !ok {
		return "", "", false
	}
	id, err := model.ParseWebhookID(r.PathValue("subscriptionID"))
	if err != nil {
		writeInvalidID(w)
		return "", "", false
	}
	return tenant, id, true
}

func (h *Handler) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	if !noQuery(w, r) {
		return
	}
	tenant, ok := pathTenantID(w, r)
	if !ok {
		return
	}
	items, err := h.webhooks.List(r.Context(), tenant)
	if err != nil {
		writeServiceError(r.Context(), w, err, "could not list webhooks")
		return
	}
	dtos := make([]webhookDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, toWebhookDTO(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": dtos})
}

func (h *Handler) handleGetWebhook(w http.ResponseWriter, r *http.Request) {
	tenant, id, ok := webhookTarget(w, r)
	if !ok {
		return
	}
	item, err := h.webhooks.Get(r.Context(), tenant, id)
	if err != nil {
		writeServiceError(r.Context(), w, err, "could not read webhook")
		return
	}
	writeJSON(w, http.StatusOK, toWebhookDTO(item))
}

func (h *Handler) handlePutWebhook(w http.ResponseWriter, r *http.Request) {
	tenant, id, ok := webhookTarget(w, r)
	if !ok {
		return
	}
	var input webhookInputDTO
	if !decodeJSON(w, r, &input) {
		return
	}
	item, err := h.webhooks.Put(r.Context(), tenant, id, service.WebhookInput{Destination: input.Destination, Events: input.Events, Enabled: input.Enabled, Secrets: input.Secrets})
	if err != nil {
		writeServiceError(r.Context(), w, err, "could not write webhook")
		return
	}
	writeJSON(w, http.StatusOK, toWebhookDTO(item))
}

func (h *Handler) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	tenant, id, ok := webhookTarget(w, r)
	if !ok {
		return
	}
	if err := h.webhooks.Delete(r.Context(), tenant, id); err != nil {
		writeServiceError(r.Context(), w, err, "could not delete webhook")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleResetWebhook drops the pending deliveries and resumes at the end of
// the feed. The client acknowledges explicitly that events are skipped.
func (h *Handler) handleResetWebhook(w http.ResponseWriter, r *http.Request) {
	tenant, id, ok := webhookTarget(w, r)
	if !ok {
		return
	}
	var input struct {
		AcknowledgeLoss bool `json:"acknowledgeLoss"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !input.AcknowledgeLoss {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "a reset skips events: acknowledgeLoss must be true")
		return
	}
	if err := h.webhooks.Reset(r.Context(), tenant, id); err != nil {
		writeServiceError(r.Context(), w, err, "could not reset webhook")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	tenant, id, ok := webhookTarget(w, r)
	if !ok {
		return
	}
	items, err := h.webhooks.Deliveries(r.Context(), tenant, id)
	if err != nil {
		writeServiceError(r.Context(), w, err, "could not list webhook deliveries")
		return
	}
	dtos := make([]webhookDeliveryDTO, 0, len(items))
	for _, d := range items {
		dtos = append(dtos, webhookDeliveryDTO{ID: d.ID, EventID: d.EventID, Sequence: d.Sequence, State: d.State, Attempts: d.Attempts, NextAttempt: d.NextAttempt, CreatedAt: d.CreatedAt, FinishedAt: d.FinishedAt, Diagnostic: d.Diagnostic, StatusCode: d.StatusCode})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": dtos})
}
