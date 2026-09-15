package config

import "time"

// ProxyConfig tunes how the OpenAI-compatible proxy talks to the upstream
// providers.
type ProxyConfig struct {
	// UpstreamTimeout bounds a call to an upstream provider. For a plain
	// completion or an embeddings call it caps the whole call, retries
	// included. For a streamed completion it caps the wait for the first chunk
	// and then the silence between two chunks, so a long but flowing answer is
	// never cut. When it expires the client receives a 504 that names the
	// provider instead of an opaque reverse-proxy timeout. The reverse proxy
	// in front of Xolo must keep a strictly longer timeout. "0" disables it.
	UpstreamTimeout time.Duration `env:"UPSTREAM_TIMEOUT,expand" envDefault:"5m"`
	// ActiveUserCacheTTL is how long the number of users active on a
	// subscription plan is reused before being counted again. That count is a
	// COUNT(DISTINCT user_id) over the plan's whole window, run for every proxy
	// request on a subscription provider; it moves slowly, and a value a few
	// seconds stale changes a user's share by at most one competitor. Raise it
	// where the usage table is large, "0" counts on every request.
	ActiveUserCacheTTL time.Duration `env:"ACTIVE_USER_CACHE_TTL,expand" envDefault:"30s"`
	// ActiveUserCountTimeout bounds one plan-wide active-user count. The count
	// runs detached from the proxy request, so this is what stops a stuck query;
	// a count that always exceeds it is cached as a failure and every share on
	// the plan falls back to the whole membership. Raise it where the usage
	// table is very large or the index is missing, rather than living with that.
	ActiveUserCountTimeout time.Duration `env:"ACTIVE_USER_COUNT_TIMEOUT,expand" envDefault:"10s"`
}
