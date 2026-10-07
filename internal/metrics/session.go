package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	NameOIDCBackchannelLogouts = "oidc_backchannel_logouts_total"

	LabelOIDCLogoutResult = "result"
)

// OIDC back-channel logout results.
const (
	OIDCLogoutRevoked     = "revoked"
	OIDCLogoutReplayed    = "replayed"
	OIDCLogoutInvalid     = "invalid"
	OIDCLogoutUnavailable = "unavailable"
)

var OIDCBackchannelLogouts = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name:      NameOIDCBackchannelLogouts,
		Help:      "OIDC back-channel logout requests handled by this process, by result",
		Namespace: Namespace,
	},
	[]string{LabelOIDCLogoutResult},
)
