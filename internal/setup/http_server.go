package setup

import (
	"context"
	"github.com/bornholm/genai/proxy"
	proxyAdapter "github.com/xolo-gateway/xolo/internal/adapter/proxy"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/http"
	"github.com/xolo-gateway/xolo/internal/http/handler/api"
	"github.com/xolo-gateway/xolo/internal/http/handler/metrics"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/common"
	pipelineAssets "github.com/xolo-gateway/xolo/internal/http/handler/webui/pipeline"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authz"
	membershipsMiddleware "github.com/xolo-gateway/xolo/internal/http/middleware/memberships"
	"github.com/xolo-gateway/xolo/internal/http/middleware/tenant"
	"github.com/xolo-gateway/xolo/internal/http/middleware/ratelimit"
	"github.com/pkg/errors"

	gohttp "net/http"
)

func NewHTTPServerFromConfig(ctx context.Context, conf *config.Config) (*http.Server, error) {
	oidcAuthn, err := getOIDCAuthnHandlerFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.Wrap(err, "could not configure authn oidc handler from config")
	}

	tokenAuthn, err := getTokenAuthnHandlerFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.Wrap(err, "could not configure authn token handler from config")
	}

	oidcTokenAuthn, err := getOIDCTokenAuthnHandlerFromConfig(ctx, conf, oidcAuthn)
	if err != nil {
		return nil, errors.Wrap(err, "could not configure authn oidc token handler from config")
	}

	oauth2TokenAuthn, err := getOAuth2TokenAuthnHandlerFromConfig(ctx, conf, oidcAuthn)
	if err != nil {
		return nil, errors.Wrap(err, "could not configure authn oauth2 token handler from config")
	}

	authenticators := []authn.Authenticator{tokenAuthn, oidcAuthn}
	if oidcTokenAuthn != nil {
		authenticators = append([]authn.Authenticator{oidcTokenAuthn}, authenticators...)
	}

	// API auth chain ordering (cheapest / most specific first):
	//   oidctoken (local JWT ID token) → oauth2token (remote introspection) →
	//   tokenAuthn (DB API token) → oidcAuthn (session).
	apiAuthenticators := []authn.Authenticator{tokenAuthn}
	if oauth2TokenAuthn != nil {
		apiAuthenticators = append([]authn.Authenticator{oauth2TokenAuthn}, apiAuthenticators...)
	}
	if oidcTokenAuthn != nil {
		apiAuthenticators = append([]authn.Authenticator{oidcTokenAuthn}, apiAuthenticators...)
	}
	apiAuthenticators = append(apiAuthenticators, oidcAuthn)

	authnMiddleware := authn.Middleware(
		func(w gohttp.ResponseWriter, r *gohttp.Request) {
			gohttp.Redirect(w, r, "/auth/oidc/login", gohttp.StatusSeeOther)
		},
		authenticators...,
	)

	apiAuthnMiddleware := authn.Middleware(
		func(w gohttp.ResponseWriter, r *gohttp.Request) {
			gohttp.Error(w, gohttp.StatusText(gohttp.StatusUnauthorized), gohttp.StatusUnauthorized)
		},
		apiAuthenticators...,
	)

	bridgeMiddleware, err := getBridgeMiddlewareFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.Wrap(err, "could not configure bridge middleware from config")
	}

	assets := common.NewHandler()

	rateLimiter := ratelimit.Middleware(
		conf.HTTP.RateLimit.TrustHeaders,
		conf.HTTP.RateLimit.RequestInterval,
		conf.HTTP.RateLimit.RequestMaxBurst,
		conf.HTTP.RateLimit.CacheSize,
		conf.HTTP.RateLimit.CacheTTL,
	)

	authChain := func(h gohttp.Handler) gohttp.Handler {
		return authnMiddleware(bridgeMiddleware(h))
	}

	taskRunner, err := getTaskRunner(ctx, conf)
	if err != nil {
		return nil, errors.Wrap(err, "could not create task runner from config")
	}

	userStore, err := getUserStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.Wrap(err, "could not create user store from config")
	}

	orgStore, err := getOrgStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.Wrap(err, "could not create org store from config")
	}

	roleStore, err := getRoleStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.Wrap(err, "could not create role store from config")
	}

	providerStore, err := getProviderStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.Wrap(err, "could not create provider store from config")
	}

	quotaStore, err := getQuotaStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.Wrap(err, "could not create quota store from config")
	}

	quotaService, err := getQuotaServiceFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.Wrap(err, "could not create quota service from config")
	}

	usageStore, err := getUsageStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.Wrap(err, "could not create usage store from config")
	}

	inviteStore, err := getInviteStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.Wrap(err, "could not create invite store from config")
	}

	exchangeRateService, err := getExchangeRateServiceFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.Wrap(err, "could not create exchange rate service from config")
	}
	exchangeRateService.StartRefresher(ctx, model.SupportedCurrencies, conf.ExchangeRate.RefreshInterval)

	pluginManager, err := getPluginManagerFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.Wrap(err, "could not create plugin manager from config")
	}

	virtualModelStore, err := getVirtualModelStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	personalVMStore, err := getPersonalVirtualModelStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.Wrap(err, "could not create personal virtual model store from config")
	}

	middlewareStore, err := getMiddlewareStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.Wrap(err, "could not create middleware store from config")
	}

	subscriptionState := proxyAdapter.NewSubscriptionState()

	orgModelRouter := proxyAdapter.NewOrgModelRouter(providerStore, orgStore, conf.SecretKey)

	pipelineHookAdapter := proxyAdapter.NewPipelineHookAdapter(
		pluginManager,
		virtualModelStore,
		personalVMStore,
		providerStore,
		orgStore,
		middlewareStore,
		orgModelRouter,
	)

	withMemberships := membershipsMiddleware.Middleware(orgStore, roleStore)

	applicationStore, err := getApplicationStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.Wrap(err, "could not create application store from config")
	}

	secretStore, err := getSecretStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.Wrap(err, "could not create secret store from config")
	}

	eventEmitter, err := getEventEmitterFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.Wrap(err, "could not create event emitter from config")
	}

	eventStore, err := getEventStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.Wrap(err, "could not create event store from config")
	}

	alertStore, err := getAlertStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.Wrap(err, "could not create alert store from config")
	}

	alertIncidentStore, err := getAlertIncidentStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.Wrap(err, "could not create alert incident store from config")
	}

	eventSettingsStore, err := getEventSettingsStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.Wrap(err, "could not create event settings store from config")
	}

	if _, err := getAlertEvaluatorFromConfig(ctx, conf); err != nil {
		return nil, errors.Wrap(err, "could not start alert evaluator from config")
	}

	if _, err := getEventPurgerFromConfig(ctx, conf); err != nil {
		return nil, errors.Wrap(err, "could not start event purger from config")
	}

	webuiHandler := webui.NewHandler(taskRunner, userStore, orgStore, roleStore, providerStore, virtualModelStore, middlewareStore, personalVMStore, usageStore, inviteStore, applicationStore, quotaStore, quotaService, exchangeRateService, secretStore, conf.SecretKey, pluginManager, subscriptionState, eventStore, alertStore, alertIncidentStore, eventSettingsStore, conf.Events.MaxPerOrg, conf.Events.DefaultPerOrg)

	apiHandler := api.NewHandler(providerStore, orgStore, virtualModelStore, personalVMStore, middlewareStore, secretStore, exchangeRateService, pluginManager)

	proxyServer := proxy.NewServer(
		proxy.WithAuthExtractor(proxyAdapter.XoloAuthExtractor()),
		proxy.WithHook(proxyAdapter.NewXoloMetricsHook()),
		proxy.WithHook(&proxyAdapter.SessionIDHook{}),
		proxy.WithHook(pipelineHookAdapter),
		proxy.WithHook(orgModelRouter),
		proxy.WithHook(proxyAdapter.NewXoloQuotaEnforcer(quotaService, quotaStore, usageStore, providerStore)),
		proxy.WithHook(proxyAdapter.NewXoloSubscriptionEnforcer(providerStore, usageStore, subscriptionState, orgStore)),
		proxy.WithHook(proxyAdapter.NewXoloUsageTracker(usageStore, providerStore, orgStore, exchangeRateService)),
		proxy.WithHook(proxyAdapter.NewXoloEventEmitterHook(eventEmitter)),
	)

	// apiActiveCheck rejects authenticated-but-deactivated users on every API
	// surface. The webui enforces this per-route via authz.Active(); the LLM proxy
	// path did not, letting a deactivated account keep spending through a still-valid
	// API token or an existing browser session.
	apiActiveCheck := authz.Middleware(
		gohttp.HandlerFunc(func(w gohttp.ResponseWriter, r *gohttp.Request) {
			gohttp.Error(w, gohttp.StatusText(gohttp.StatusForbidden), gohttp.StatusForbidden)
		}),
		authz.Active(),
	)

	apiAuthChain := func(h gohttp.Handler) gohttp.Handler {
		return apiAuthnMiddleware(bridgeMiddleware(apiActiveCheck(withMemberships(h))))
	}

	tenantStore, err := getTenantStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	// Tenant resolution wraps the whole server: authentication resolves a user
	// within a tenant, so no route may run before the tenant is known. A host
	// matching none answers 404 — an unknown subdomain must not reveal whether
	// the instance exists.
	tenantMiddleware := tenant.Middleware(
		tenant.NewResolver(tenantStore, conf.Multitenancy),
		gohttp.HandlerFunc(func(w gohttp.ResponseWriter, r *gohttp.Request) {
			common.HandleError(w, r, common.NewHTTPError(gohttp.StatusNotFound))
		}),
	)

	options := []http.OptionFunc{
		http.WithAddress(conf.HTTP.Address),
		http.WithBaseURL(conf.HTTP.BaseURL),
		http.WithMiddleware(tenantMiddleware),
		http.WithMount("/assets/", assets),
		http.WithMount("/assets/pipeline/", pipelineAssets.NewAssetsHandler()),
		http.WithMount("/auth/oidc/", rateLimiter(oidcAuthn)),
		http.WithMount("/auth/token/", rateLimiter(tokenAuthn)),
		http.WithMount("/metrics/", rateLimiter(authChain(metrics.NewHandler()))),
		// LLM proxy traffic is intentionally NOT behind the per-IP rate limiter:
		// requests are authenticated and already regulated by quotas, budgets and
		// per-provider rate limits. The per-IP limiter (1 req/s by default) would
		// otherwise reject concurrent embeddings/chat traffic with 429s.
		http.WithMount("/api/v1/", apiAuthChain(proxyServer)),
		http.WithRoute("GET /api/v1/models", apiAuthChain(apiHandler)),
		http.WithRoute("GET /api/models-dev/lookup", rateLimiter(apiAuthChain(apiHandler))),
		http.WithRoute("GET /api/exchange-rate", rateLimiter(apiAuthChain(apiHandler))),
		// Plugin UI config sync
		http.WithRoute("PUT /api/orgs/{orgSlug}/plugin-ui-config", rateLimiter(apiAuthChain(apiHandler))),
		http.WithRoute("GET /api/orgs/{orgSlug}/plugin-ui-config", rateLimiter(apiAuthChain(apiHandler))),
		http.WithRoute("PUT /api/personal-plugin-ui-config", rateLimiter(apiAuthChain(apiHandler))),
		http.WithRoute("GET /api/personal-plugin-ui-config", rateLimiter(apiAuthChain(apiHandler))),
		// Virtual model pipeline API
		http.WithRoute("GET /api/orgs/{orgSlug}/virtual-models", rateLimiter(apiAuthChain(apiHandler))),
		http.WithRoute("POST /api/orgs/{orgSlug}/virtual-models", rateLimiter(apiAuthChain(apiHandler))),
		http.WithRoute("POST /api/orgs/{orgSlug}/virtual-models/import", rateLimiter(apiAuthChain(apiHandler))),
		http.WithRoute("GET /api/orgs/{orgSlug}/virtual-models/{vmID}", rateLimiter(apiAuthChain(apiHandler))),
		http.WithRoute("GET /api/orgs/{orgSlug}/virtual-models/{vmID}/export", rateLimiter(apiAuthChain(apiHandler))),
		http.WithRoute("PUT /api/orgs/{orgSlug}/virtual-models/{vmID}", rateLimiter(apiAuthChain(apiHandler))),
		http.WithRoute("DELETE /api/orgs/{orgSlug}/virtual-models/{vmID}", rateLimiter(apiAuthChain(apiHandler))),
		http.WithRoute("GET /api/orgs/{orgSlug}/pipeline-node-types", rateLimiter(apiAuthChain(apiHandler))),

		http.WithRoute("GET /api/orgs/{orgSlug}/middlewares", rateLimiter(apiAuthChain(apiHandler))),
		http.WithRoute("POST /api/orgs/{orgSlug}/middlewares", rateLimiter(apiAuthChain(apiHandler))),
		http.WithRoute("GET /api/orgs/{orgSlug}/middlewares/{mwID}", rateLimiter(apiAuthChain(apiHandler))),
		http.WithRoute("PUT /api/orgs/{orgSlug}/middlewares/{mwID}", rateLimiter(apiAuthChain(apiHandler))),
		http.WithRoute("PUT /api/orgs/{orgSlug}/middlewares/{mwID}/settings", rateLimiter(apiAuthChain(apiHandler))),
		http.WithRoute("DELETE /api/orgs/{orgSlug}/middlewares/{mwID}", rateLimiter(apiAuthChain(apiHandler))),
		// Personal virtual model pipeline API
		http.WithRoute("GET /api/personal-models", rateLimiter(apiAuthChain(apiHandler))),
		http.WithRoute("POST /api/personal-models", rateLimiter(apiAuthChain(apiHandler))),
		http.WithRoute("POST /api/personal-models/import", rateLimiter(apiAuthChain(apiHandler))),
		http.WithRoute("GET /api/personal-models/{vmID}", rateLimiter(apiAuthChain(apiHandler))),
		http.WithRoute("GET /api/personal-models/{vmID}/export", rateLimiter(apiAuthChain(apiHandler))),
		http.WithRoute("PUT /api/personal-models/{vmID}", rateLimiter(apiAuthChain(apiHandler))),
		http.WithRoute("DELETE /api/personal-models/{vmID}", rateLimiter(apiAuthChain(apiHandler))),
		http.WithRoute("GET /api/personal-models/pipeline-node-types", rateLimiter(apiAuthChain(apiHandler))),
		http.WithMount("/", authChain(withMemberships(webuiHandler))),
	}

	server := http.NewServer(options...)

	return server, nil
}
