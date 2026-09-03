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
}
