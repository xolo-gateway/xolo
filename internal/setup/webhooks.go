package setup

import (
	"context"
	"crypto/x509"
	"os"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/adapter/webhook"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

// WebhookWorker delivers the webhooks. It runs whether or not the
// Provisionning API listener is enabled: subscriptions keep receiving the
// local changes.
type WebhookWorker struct {
	*service.WebhookService
	sender *webhook.Sender
}

func (w *WebhookWorker) Run(ctx context.Context) error {
	defer w.sender.Close()
	return w.WebhookService.Run(ctx)
}

// NewWebhookWorkerFromConfig returns nil when webhooks are disabled.
var NewWebhookWorkerFromConfig = createFromConfigOnce(func(ctx context.Context, conf *config.Config) (*WebhookWorker, error) {
	if !conf.Webhooks.Enabled {
		return nil, nil
	}
	if err := conf.Webhooks.Validate(); err != nil {
		return nil, errors.WithStack(err)
	}
	store, err := getGormStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	var roots *x509.CertPool
	if conf.Webhooks.TLSCAFile != "" {
		if roots, err = x509.SystemCertPool(); err != nil {
			return nil, errors.Wrap(err, "could not load the system trust roots for webhooks")
		}
		raw, err := os.ReadFile(conf.Webhooks.TLSCAFile)
		if err != nil {
			return nil, errors.Wrap(err, "could not read XOLO_WEBHOOKS_TLS_CA_FILE")
		}
		if !roots.AppendCertsFromPEM(raw) {
			return nil, errors.New("XOLO_WEBHOOKS_TLS_CA_FILE holds no PEM certificate")
		}
	}
	sender, err := webhook.NewSender(conf.Webhooks.AllowedOrigins, conf.Webhooks.AllowPrivateNetworks, roots)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	capacity := model.WebhookCapacity{Queue: conf.Webhooks.QueueCapacity, Subscription: conf.Webhooks.SubscriptionCapacity}
	return &WebhookWorker{
		WebhookService: service.NewWebhookService(store, sender, conf.SecretKey, conf.Webhooks.Workers, capacity, conf.Webhooks.PollInterval),
		sender:         sender,
	}, nil
})
