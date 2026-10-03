package setup

import (
	"context"
	"crypto/x509"
	"fmt"
	"os"

	"github.com/xolo-gateway/xolo/internal/adapter/webhook"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

type WebhookWorker struct {
	*service.WebhookService
	sender *webhook.Sender
}

func (w *WebhookWorker) Run(ctx context.Context) error {
	defer w.sender.Close()
	return w.WebhookService.Run(ctx)
}

var NewWebhookWorkerFromConfig = createFromConfigOnce(func(ctx context.Context, conf *config.Config) (*WebhookWorker, error) {
	if !conf.Webhooks.Enabled {
		return nil, nil
	}
	if err := conf.Webhooks.Validate(); err != nil {
		return nil, err
	}
	store, err := getGormStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, err
	}
	var roots *x509.CertPool
	if conf.Webhooks.TLSCAFile != "" {
		roots, err = x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("load system webhook trust roots")
		}
		raw, err := os.ReadFile(conf.Webhooks.TLSCAFile)
		if err != nil {
			return nil, fmt.Errorf("read webhook trust bundle")
		}
		if !roots.AppendCertsFromPEM(raw) {
			return nil, fmt.Errorf("invalid webhook trust bundle")
		}
	}
	sender, err := webhook.NewSender(conf.Webhooks.AllowedOrigins, conf.Webhooks.AllowPrivateNetworks, roots)
	if err != nil {
		return nil, err
	}
	return &WebhookWorker{WebhookService: service.NewWebhookService(store, sender, conf.SecretKey, conf.Webhooks.Workers, conf.Webhooks.QueueCapacity, conf.Webhooks.PollInterval), sender: sender}, nil
})
