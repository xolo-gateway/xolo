package setup

import (
	"strings"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	"github.com/xolo-gateway/xolo/internal/http"
	"github.com/xolo-gateway/xolo/internal/http/handler/passthrough"

	gohttp "net/http"
)

// passthroughOptions builds the mounts for the credential-relay surface.
//
// The surface is split in two on purpose. The probe is anonymous — the client
// sends it with no credentials and refuses to go further until it succeeds — so
// it is mounted outside the auth chain. The relay itself goes through the exact
// same chain as every other API route, reached only after CredentialSwap has
// put the Xolo token where the authenticators look for it.
func passthroughOptions(
	conf *config.Config,
	apiAuthChain func(gohttp.Handler) gohttp.Handler,
	usageStore port.UsageStore,
	providerStore port.ProviderStore,
	orgStore port.OrgStore,
	exchangeRateService *service.ExchangeRateService,
) ([]http.OptionFunc, error) {
	pt := conf.Passthrough

	recorder := passthrough.NewRecorder(
		usageStore,
		providerStore,
		orgStore,
		exchangeRateService,
		model.ProviderID(pt.ProviderID),
	)

	handler, err := passthrough.NewHandler(pt.UpstreamBaseURL, pt.CredentialHeader, pt.AllowedPaths, conf.Proxy.UpstreamTimeout, recorder)
	if err != nil {
		return nil, errors.Wrap(err, "could not create passthrough handler")
	}

	prefix := strings.TrimSuffix(pt.MountPrefix, "/")
	probe := passthrough.ProbeHandler()

	return []http.OptionFunc{
		// Exact routes take precedence over the prefix mount below, which is
		// what keeps the probe outside the auth chain.
		http.WithRoute("GET "+prefix+"/api/hello", probe),
		http.WithRoute("HEAD "+prefix+"/api/hello", probe),
		http.WithMount(pt.MountPrefix, passthrough.CredentialSwap(pt.CredentialHeader)(apiAuthChain(handler))),
	}, nil
}
