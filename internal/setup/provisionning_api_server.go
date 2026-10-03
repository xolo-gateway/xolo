package setup

import (
	"context"
	"github.com/xolo-gateway/xolo/internal/build"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/provisionning"
	v1 "github.com/xolo-gateway/xolo/internal/provisionning/handler/v1"
)

// NewProvisionningAPIServerFromConfig assembles the Provisionning API server.
// It returns a nil server when the API is disabled, which the caller treats as
// "nothing to run".
//
// The TLS material is loaded here, before any listener is opened, so a
// misconfiguration is fatal at startup rather than on the first request.
func NewProvisionningAPIServerFromConfig(ctx context.Context, conf *config.Config) (*provisionning.Server, error) {
	if !conf.ProvisionningAPI.Enabled {
		return nil, nil
	}

	if err := conf.ProvisionningAPI.Validate(); err != nil {
		return nil, err
	}

	provisioning, err := getProvisioningServiceFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	tlsConfig, err := provisionning.LoadTLSConfig(
		conf.ProvisionningAPI.TLSCertFile,
		conf.ProvisionningAPI.TLSKeyFile,
		conf.ProvisionningAPI.TLSClientCAFile,
		conf.ProvisionningAPI.AuthorizedURIs...,
	)
	if err != nil {
		return nil, errors.Wrap(err, "could not load provisionning api tls configuration")
	}

	handler := v1.NewHandler(provisioning, build.ShortVersion)
	if conf.BusinessResources {
		handler.WithBusiness()
	}
	lifecycle, err := NewLifecycleServiceFromConfig(ctx, conf)
	if err != nil {
		return nil, err
	}
	if lifecycle != nil {
		handler.WithLifecycle(lifecycle)
	}
	worker, err := NewWebhookWorkerFromConfig(ctx, conf)
	if err != nil {
		return nil, err
	}
	if worker != nil {
		handler.WithWebhooks(worker.WebhookService)
	}
	return provisionning.NewServer(
		provisionning.WithAddress(conf.ProvisionningAPI.Address),
		provisionning.WithClientPolicy(conf.ProvisionningAPI.AuthorizedURIs, conf.ProvisionningAPI.RateLimit, conf.ProvisionningAPI.RateBurst),
		provisionning.WithTLSConfig(tlsConfig),
		provisionning.WithHandler(handler),
		provisionning.WithShutdownTimeout(conf.ProvisionningAPI.ShutdownTimeout),
	), nil
}
