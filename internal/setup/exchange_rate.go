package setup

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	exchangerateAdapter "github.com/xolo-gateway/xolo/internal/adapter/exchangerate"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

var getExchangeRateStoreFromConfig = createFromConfigOnce(func(ctx context.Context, conf *config.Config) (port.ExchangeRateStore, error) {
	store, err := getGormStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return store, nil
})

var getExchangeRateServiceFromConfig = createFromConfigOnce(func(ctx context.Context, conf *config.Config) (*service.ExchangeRateService, error) {
	store, err := getExchangeRateStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	var provider port.ExchangeRateProvider
	switch conf.ExchangeRate.Provider {
	case "file":
		if conf.ExchangeRate.FilePath == "" {
			return nil, fmt.Errorf("exchange rate file provider requires XOLO_EXCHANGE_RATE_FILE_PATH to be set")
		}
		provider = exchangerateAdapter.NewFileProvider(conf.ExchangeRate.FilePath)
	case "ecb":
		provider = exchangerateAdapter.NewECBProvider()
	default: // "frankfurter"
		provider = exchangerateAdapter.NewFrankfurterProvider()
	}

	svc := service.NewExchangeRateService(provider, store, conf.ExchangeRate.TTL)
	return svc, nil
})
