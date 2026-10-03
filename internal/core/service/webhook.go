package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/crypto"
	"github.com/xolo-gateway/xolo/internal/metrics"
)

type WebhookService struct {
	store             port.WebhookStore
	sender            port.WebhookSender
	key               string
	workers, capacity int
	poll              time.Duration
}

func NewWebhookService(store port.WebhookStore, sender port.WebhookSender, key string, workers, capacity int, poll time.Duration) *WebhookService {
	return &WebhookService{store: store, sender: sender, key: key, workers: workers, capacity: capacity, poll: poll}
}

type WebhookInput struct {
	Destination string   `json:"destination"`
	Events      []string `json:"events"`
	Enabled     *bool    `json:"enabled"`
	Secrets     []string `json:"secrets,omitempty"`
}
type webhookCredentials struct {
	TenantID, SubscriptionID string
	Secrets                  []string
}

func ValidWebhookEvent(typ string) bool {
	for _, family := range []string{"tenant", "organization", "member"} {
		for _, action := range []string{"deleted", "export_confirmed", "purged"} {
			if typ == family+"."+action+".v1" {
				return true
			}
		}
	}
	for _, family := range []string{"tenant_domain", "organization_membership", "custom_role", "application", "quota", "alert", "provider"} {
		if typ == family+".deleted.v1" {
			return true
		}
		if model.IsBusinessFamily(family) && (typ == family+".created.v1" || typ == family+".updated.v1") {
			return true
		}
	}

	if typ == "*" {
		return true
	}
	for _, family := range []string{"tenant", "tenant_domain", "organization", "member", "organization_membership"} {
		for _, action := range []string{"created", "updated", "status_changed"} {
			if typ == family+"."+action+".v1" {
				return true
			}
		}
	}
	return typ == "member.tenant_role_changed.v1" || typ == "organization_membership.granted.v1" || typ == "organization_membership.role_changed.v1"
}
func (s *WebhookService) Put(ctx context.Context, tid, id string, p WebhookInput) (model.WebhookSubscription, error) {
	var out model.WebhookSubscription
	if _, err := model.ParseTenantID(tid); err != nil {
		return out, port.ErrInvalid
	}
	if _, err := model.ParseTenantID(id); err != nil {
		return out, port.ErrInvalid
	}
	// Resolve the tenant before processing or reading existing credentials.
	if _, err := s.store.ListWebhooks(ctx, tid); err != nil {
		return out, err
	}
	if p.Enabled == nil || len(p.Events) == 0 || len(p.Events) > 20 || len(p.Destination) > 2048 {
		return out, port.ErrInvalid
	}
	if err := s.sender.ValidateDestination(p.Destination); err != nil {
		return out, port.ErrInvalid
	}
	seen := map[string]bool{}
	for _, typ := range p.Events {
		if !ValidWebhookEvent(typ) || seen[typ] {
			return out, port.ErrInvalid
		}
		seen[typ] = true
	}
	if seen["*"] && len(p.Events) != 1 {
		return out, port.ErrInvalid
	}
	settings := model.WebhookSettings{Destination: p.Destination, Events: slices.Clone(p.Events), Enabled: *p.Enabled}
	slices.Sort(settings.Events)
	if p.Secrets != nil {
		if len(p.Secrets) < 1 || len(p.Secrets) > 2 {
			return out, port.ErrInvalid
		}
		keys := map[string]bool{}
		for _, secret := range p.Secrets {
			if len(secret) > 94 || strings.ContainsAny(secret, "\r\n") {
				return out, port.ErrInvalid
			}
			raw, err := base64.StdEncoding.Strict().DecodeString(strings.TrimPrefix(secret, "whsec_"))
			if err != nil || len(raw) < 32 || len(raw) > 64 || keys[string(raw)] {
				return out, port.ErrInvalid
			}
			keys[string(raw)] = true
		}
		raw, err := json.Marshal(webhookCredentials{TenantID: tid, SubscriptionID: id, Secrets: p.Secrets})
		if err != nil {
			return out, err
		}
		cipher, err := crypto.Encrypt(s.key, string(raw))
		if err != nil {
			return out, errors.New("webhook credentials encryption failed")
		}
		settings.EncryptedSecrets = cipher
		settings.SecretCount = len(p.Secrets)
	}
	return s.store.PutWebhook(ctx, tid, id, settings)
}
func (s *WebhookService) Get(ctx context.Context, tid, id string) (model.WebhookSubscription, error) {
	return s.store.GetWebhook(ctx, tid, id)
}
func (s *WebhookService) List(ctx context.Context, tid string) ([]model.WebhookSubscription, error) {
	return s.store.ListWebhooks(ctx, tid)
}
func (s *WebhookService) Delete(ctx context.Context, tid, id string) error {
	return s.store.DeleteWebhook(ctx, tid, id)
}
func (s *WebhookService) Reset(ctx context.Context, tid, id string) error {
	return s.store.ResetWebhook(ctx, tid, id)
}
func (s *WebhookService) Stats(ctx context.Context) (model.WebhookStats, error) {
	return s.store.WebhookStats(ctx)
}
func (s *WebhookService) deliver(ctx context.Context, job *model.WebhookJob) model.WebhookResult {
	raw, err := crypto.Decrypt(s.key, job.EncryptedSecrets)
	if err != nil {
		return model.WebhookResult{Diagnostic: "credentials_unavailable"}
	}
	var c webhookCredentials
	if json.Unmarshal([]byte(raw), &c) != nil || c.TenantID != job.TenantID || c.SubscriptionID != job.SubscriptionID {
		return model.WebhookResult{Diagnostic: "credentials_unavailable"}
	}
	return s.sender.SendWebhook(ctx, job, c.Secrets)
}

// Run owns all worker goroutines. Cancellation interrupts in-flight HTTP, then
// result recording gets a bounded grace period. Unrecorded results recover by lease.
func (s *WebhookService) Run(ctx context.Context) error {
	if s.workers < 1 || s.workers > 16 || s.capacity < 1 || s.poll <= 0 {
		return errors.New("invalid webhook worker settings")
	}
	var wg sync.WaitGroup
	for range s.workers {
		wg.Go(func() { s.runWorker(ctx) })
	}
	defer wg.Wait()
	ticker := time.NewTicker(s.poll)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := s.store.PrepareWebhooks(ctx, s.capacity); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			metrics.WebhookErrors.WithLabelValues("storage").Inc()
			slog.WarnContext(ctx, "webhook materialization failed")
		}
		if stats, err := s.store.WebhookStats(ctx); err == nil {
			metrics.WebhookQueue.WithLabelValues("pending").Set(float64(stats.Pending))
			metrics.WebhookQueue.WithLabelValues("leased").Set(float64(stats.InFlight))
			metrics.WebhookQueue.WithLabelValues("failed").Set(float64(stats.Failed))
			metrics.WebhookQueue.WithLabelValues("delivered").Set(float64(stats.Delivered))
			metrics.WebhookLag.WithLabelValues("materialization").Set(stats.MaterializationLag)
			metrics.WebhookLag.WithLabelValues("delivery").Set(stats.DeliveryLag)
			metrics.WebhookHistoryLost.Set(float64(stats.HistoryLost))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
func (s *WebhookService) runWorker(ctx context.Context) {
	for ctx.Err() == nil {
		job, err := s.store.ClaimWebhook(ctx)
		if err != nil || job == nil {
			if err != nil && ctx.Err() == nil {
				metrics.WebhookErrors.WithLabelValues("storage").Inc()
				slog.WarnContext(ctx, "webhook reservation failed")
			}
			timer := time.NewTimer(s.poll)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		metrics.WebhookAttempts.Inc()
		result := s.deliver(ctx, job)
		if !result.Success {
			metrics.WebhookErrors.WithLabelValues(result.Diagnostic).Inc()
		}
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err = s.store.FinishWebhook(finishCtx, job, result)
		cancel()
		if err != nil && !errors.Is(err, port.ErrWebhookLeaseLost) {
			metrics.WebhookErrors.WithLabelValues("storage").Inc()
			slog.WarnContext(ctx, "webhook result recording failed")
		}
	}
}

func (s *WebhookService) Deliveries(ctx context.Context, tid, id string) ([]model.WebhookDeliveryStatus, error) {
	return s.store.ListWebhookDeliveries(ctx, tid, id)
}
