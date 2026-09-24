package config

import "testing"

func validPassthrough() Passthrough {
	return Passthrough{
		Enabled:          true,
		MountPrefix:      "/passthrough/",
		UpstreamBaseURL:  "https://api.anthropic.com",
		CredentialHeader: "X-Xolo-Key",
		ProviderID:       "provider-1",
		AllowedPaths:     []string{"/v1/messages"},
	}
}

func TestPassthroughValidateAcceptsValidConfig(t *testing.T) {
	if err := validPassthrough().Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// A disabled passthrough is never consulted, so an incomplete config must not
// prevent the instance from starting.
func TestPassthroughValidateSkipsWhenDisabled(t *testing.T) {
	if err := (Passthrough{Enabled: false}).Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPassthroughValidateRejectsInvalidConfigs(t *testing.T) {
	tests := map[string]func(*Passthrough){
		"missing provider":                    func(p *Passthrough) { p.ProviderID = "" },
		"empty credential header":             func(p *Passthrough) { p.CredentialHeader = "" },
		"mount prefix without leading slash":  func(p *Passthrough) { p.MountPrefix = "passthrough/" },
		"mount prefix without trailing slash": func(p *Passthrough) { p.MountPrefix = "/passthrough" },
		"upstream without host":               func(p *Passthrough) { p.UpstreamBaseURL = "not-a-url" },
		"empty allowed paths":                 func(p *Passthrough) { p.AllowedPaths = nil },
		"allowed path without leading slash":  func(p *Passthrough) { p.AllowedPaths = []string{"v1/messages"} },
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			conf := validPassthrough()
			mutate(&conf)
			if err := conf.Validate(); err == nil {
				t.Error("expected an error, got none")
			}
		})
	}
}

// A relayed credential must never leave the machine in clear text.
func TestPassthroughValidateRejectsPlaintextUpstream(t *testing.T) {
	conf := validPassthrough()
	conf.UpstreamBaseURL = "http://api.anthropic.com"

	if err := conf.Validate(); err == nil {
		t.Error("expected http:// upstream to be rejected")
	}
}

// Loopback stays reachable over http so the surface can be exercised against a
// local stub without terminating TLS.
func TestPassthroughValidateAllowsLoopbackOverHTTP(t *testing.T) {
	for _, host := range []string{"http://localhost:9998", "http://127.0.0.1:9998"} {
		conf := validPassthrough()
		conf.UpstreamBaseURL = host

		if err := conf.Validate(); err != nil {
			t.Errorf("%s: unexpected error: %v", host, err)
		}
	}
}
