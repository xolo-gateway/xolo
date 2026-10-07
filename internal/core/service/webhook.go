package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/crypto"
	"github.com/xolo-gateway/xolo/internal/metrics"
)

const (
	// webhookBudget bounds the events read per subscription and per round.
	webhookBudget = 100
	// webhookStatsInterval paces the gauges, webhookSweepInterval the
	// expiry and retention of deliveries.
	webhookStatsInterval = 15 * time.Second
	webhookSweepInterval = time.Minute
	// webhookFinishTimeout bounds the recording of a result after shutdown
	// began: an unrecorded result is recovered when its lease expires.
	webhookFinishTimeout = 5 * time.Second
)

// WebhookService manages the subscriptions and runs their delivery workers.
type WebhookService struct {
	store    port.WebhookStore
	sender   port.WebhookSender
	key      string
	workers  int
	capacity model.WebhookCapacity
	poll     time.Duration
}

// NewWebhookService encrypts the secrets with key, the instance AES key.
func NewWebhookService(store port.WebhookStore, sender port.WebhookSender, key string, workers int, capacity model.WebhookCapacity, poll time.Duration) *WebhookService {
	return &WebhookService{store: store, sender: sender, key: key, workers: workers, capacity: capacity, poll: poll}
}

// WebhookInput is a subscription write. Omitted secrets keep the stored ones;
// they are required to create a subscription.
type WebhookInput struct {
	Destination string
	Events      []string
	Enabled     *bool
	Secrets     []string
}

// webhookCredentials binds the secrets to their subscription: a ciphertext
// copied to another row is refused.
type webhookCredentials struct {
	TenantID       model.TenantID  `json:"tenant_id"`
	SubscriptionID model.WebhookID `json:"subscription_id"`
	Secrets        []string        `json:"secrets"`
}

func (s *WebhookService) Put(ctx context.Context, tenant model.TenantID, id model.WebhookID, input WebhookInput) (model.WebhookSubscription, error) {
	var out model.WebhookSubscription
	// The tenant is resolved before the body is examined.
	if _, err := s.store.ListWebhooks(ctx, tenant); err != nil {
		return out, err
	}
	if input.Enabled == nil || len(input.Events) == 0 || len(input.Events) > 20 || len(input.Destination) > 2048 {
		return out, errors.WithStack(port.ErrInvalid)
	}
	if err := s.sender.ValidateDestination(input.Destination); err != nil {
		return out, errors.WithStack(port.ErrInvalid)
	}
	seen := map[string]bool{}
	for _, typ := range input.Events {
		if !model.ValidWebhookEvent(typ) || seen[typ] {
			return out, errors.WithStack(port.ErrInvalid)
		}
		seen[typ] = true
	}
	if seen["*"] && len(input.Events) != 1 {
		return out, errors.WithStack(port.ErrInvalid)
	}
	settings := model.WebhookSettings{Destination: input.Destination, Events: slices.Sorted(slices.Values(input.Events)), Enabled: *input.Enabled}
	if input.Secrets != nil {
		if err := validateWebhookSecrets(input.Secrets); err != nil {
			return out, err
		}
		raw, err := json.Marshal(webhookCredentials{TenantID: tenant, SubscriptionID: id, Secrets: input.Secrets})
		if err != nil {
			return out, errors.WithStack(err)
		}
		cipher, err := crypto.Encrypt(s.key, string(raw))
		if err != nil {
			return out, errors.New("webhook credentials encryption failed")
		}
		settings.EncryptedSecrets, settings.SecretCount = cipher, len(input.Secrets)
	}
	return s.store.PutWebhook(ctx, tenant, id, settings)
}

// validateWebhookSecrets accepts one secret, or two distinct ones during a
// rotation, in the Standard Webhooks encoding.
func validateWebhookSecrets(secrets []string) error {
	if len(secrets) < 1 || len(secrets) > 2 {
		return errors.WithStack(port.ErrInvalid)
	}
	keys := map[string]bool{}
	for _, secret := range secrets {
		if len(secret) > 94 || strings.ContainsAny(secret, "\r\n") {
			return errors.WithStack(port.ErrInvalid)
		}
		raw, err := base64.StdEncoding.Strict().DecodeString(strings.TrimPrefix(secret, "whsec_"))
		if err != nil || len(raw) < 32 || len(raw) > 64 || keys[string(raw)] {
			return errors.WithStack(port.ErrInvalid)
		}
		keys[string(raw)] = true
	}
	return nil
}

func (s *WebhookService) Get(ctx context.Context, tenant model.TenantID, id model.WebhookID) (model.WebhookSubscription, error) {
	return s.store.GetWebhook(ctx, tenant, id)
}

func (s *WebhookService) List(ctx context.Context, tenant model.TenantID) ([]model.WebhookSubscription, error) {
	return s.store.ListWebhooks(ctx, tenant)
}

func (s *WebhookService) Delete(ctx context.Context, tenant model.TenantID, id model.WebhookID) error {
	return s.store.DeleteWebhook(ctx, tenant, id)
}

func (s *WebhookService) Reset(ctx context.Context, tenant model.TenantID, id model.WebhookID) error {
	return s.store.ResetWebhook(ctx, tenant, id)
}

func (s *WebhookService) Deliveries(ctx context.Context, tenant model.TenantID, id model.WebhookID) ([]model.WebhookDeliveryStatus, error) {
	return s.store.ListWebhookDeliveries(ctx, tenant, id)
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

// Run prepares deliveries and runs the workers until ctx is done. Storage
// failures are logged and retried: webhooks never stop the server.
// Cancellation interrupts the attempts in flight; their results get a short
// grace period, and an unrecorded one is recovered by lease expiry.
func (s *WebhookService) Run(ctx context.Context) error {
	if s.workers < 1 || s.workers > 16 || s.capacity.Queue < 1 || s.capacity.Subscription < 1 || s.poll <= 0 {
		return errors.New("invalid webhook worker settings")
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	for range s.workers {
		wg.Go(func() { s.runWorker(ctx) })
	}
	wg.Go(func() { s.runMaintenance(ctx) })
	ticker := time.NewTicker(s.poll)
	defer ticker.Stop()
	for {
		if err := s.store.PrepareWebhooks(ctx, s.capacity, webhookBudget); err != nil && ctx.Err() == nil {
			metrics.WebhookFailures.WithLabelValues("storage").Inc()
			slog.ErrorContext(ctx, "webhook preparation failed", slog.Any("error", err))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (s *WebhookService) runMaintenance(ctx context.Context) {
	stats := time.NewTicker(webhookStatsInterval)
	defer stats.Stop()
	sweep := time.NewTicker(webhookSweepInterval)
	defer sweep.Stop()
	s.sweep(ctx)
	s.recordStats(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-sweep.C:
			s.sweep(ctx)
		case <-stats.C:
			s.recordStats(ctx)
		}
	}
}

func (s *WebhookService) sweep(ctx context.Context) {
	if err := s.store.SweepWebhooks(ctx, time.Now().Add(-model.WebhookRetention)); err != nil && ctx.Err() == nil {
		metrics.WebhookFailures.WithLabelValues("storage").Inc()
		slog.ErrorContext(ctx, "webhook sweep failed", slog.Any("error", err))
	}
}

func (s *WebhookService) recordStats(ctx context.Context) {
	stats, err := s.store.WebhookStats(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.WarnContext(ctx, "webhook statistics failed", slog.Any("error", err))
		}
		return
	}
	metrics.WebhookQueue.WithLabelValues(model.WebhookPending).Set(float64(stats.Pending))
	metrics.WebhookQueue.WithLabelValues(model.WebhookLeased).Set(float64(stats.InFlight))
	metrics.WebhookQueue.WithLabelValues(model.WebhookFailed).Set(float64(stats.Failed))
	metrics.WebhookQueue.WithLabelValues(model.WebhookDelivered).Set(float64(stats.Delivered))
	metrics.WebhookLag.WithLabelValues("materialization").Set(stats.MaterializationLag.Seconds())
	metrics.WebhookLag.WithLabelValues("delivery").Set(stats.DeliveryLag.Seconds())
	metrics.WebhookHistoryLost.Set(float64(stats.HistoryLost))
	metrics.WebhookBackpressure.Set(float64(stats.Backpressure))
}

func (s *WebhookService) runWorker(ctx context.Context) {
	for ctx.Err() == nil {
		job, err := s.store.ClaimWebhook(ctx)
		if err != nil || job == nil {
			if err != nil && ctx.Err() == nil {
				metrics.WebhookFailures.WithLabelValues("storage").Inc()
				slog.ErrorContext(ctx, "webhook claim failed", slog.Any("error", err))
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
			metrics.WebhookFailures.WithLabelValues(result.Diagnostic).Inc()
		}
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), webhookFinishTimeout)
		err = s.store.FinishWebhook(finishCtx, job, result)
		cancel()
		if err != nil && !errors.Is(err, port.ErrWebhookLeaseLost) {
			metrics.WebhookFailures.WithLabelValues("storage").Inc()
			slog.ErrorContext(ctx, "webhook result recording failed", slog.Any("error", err))
		}
	}
}
