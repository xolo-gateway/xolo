package main

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"testing"

	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// anonOptsHost est un faux host client minimal exposant uniquement GetSecret.
type anonOptsHost struct {
	secretValue string
	secretFound bool
}

func (h *anonOptsHost) GetSecret(_ context.Context, _, _, _, _ string) (string, bool, error) {
	return h.secretValue, h.secretFound, nil
}

func TestBuildAnonymizeOptions_VerificationOnly(t *testing.T) {
	cfg := defaultConfig()
	cfg.Verification = true
	cfg.VerificationStrict = false
	cfg.Strategy = "tag"

	opts, _ := buildAnonymizeOptions(context.Background(), cfg, &proto.RequestContext{}, &anonOptsHost{})

	if len(opts) != 1 {
		t.Fatalf("expected 1 option (WithVerification), got %d", len(opts))
	}
}

func TestBuildAnonymizeOptions_Strict(t *testing.T) {
	cfg := defaultConfig()
	cfg.Verification = true
	cfg.VerificationStrict = true
	cfg.Strategy = "tag"

	opts, _ := buildAnonymizeOptions(context.Background(), cfg, &proto.RequestContext{}, &anonOptsHost{})

	if len(opts) != 1 {
		t.Fatalf("expected 1 option (WithStrictVerification), got %d", len(opts))
	}
}

func TestBuildAnonymizeOptions_HashKey_Loaded(t *testing.T) {
	cfg := defaultConfig()
	cfg.Verification = false
	cfg.Strategy = "hash"

	const hexKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	host := &anonOptsHost{secretValue: hexKey, secretFound: true}

	opts, _ := buildAnonymizeOptions(context.Background(), cfg, &proto.RequestContext{}, host)

	if len(opts) != 1 {
		t.Fatalf("expected 1 option (WithHashKey), got %d", len(opts))
	}
}

func TestBuildAnonymizeOptions_HashKey_Missing(t *testing.T) {
	cfg := defaultConfig()
	cfg.Verification = false
	cfg.Strategy = "hash"

	host := &anonOptsHost{secretFound: false}

	_, err := buildAnonymizeOptions(context.Background(), cfg, &proto.RequestContext{}, host)

	if !errors.Is(err, errHashKeyMissing) {
		t.Fatalf("expected errHashKeyMissing when no HMAC key is configured, got %v", err)
	}
}

func TestBuildAnonymizeOptions_HashKey_Invalid(t *testing.T) {
	cfg := defaultConfig()
	cfg.Verification = false
	cfg.Strategy = "hash"

	host := &anonOptsHost{secretValue: "not-a-valid-key-too-short", secretFound: true}

	_, err := buildAnonymizeOptions(context.Background(), cfg, &proto.RequestContext{}, host)

	if !errors.Is(err, errHashKeyMissing) {
		t.Fatalf("expected errHashKeyMissing when HMAC key is malformed, got %v", err)
	}
}

func TestBuildAnonymizeOptions_HashScope(t *testing.T) {
	cfg := defaultConfig()
	cfg.Verification = false
	cfg.Strategy = "hash"
	cfg.HashScope = "tenant-42"

	const hexKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	host := &anonOptsHost{secretValue: hexKey, secretFound: true}

	opts, _ := buildAnonymizeOptions(context.Background(), cfg, &proto.RequestContext{}, host)

	if len(opts) != 2 {
		t.Fatalf("expected 2 options (WithHashKey + WithHashScope), got %d", len(opts))
	}
}

func TestBuildAnonymizeOptions_SecretScopeIsolation(t *testing.T) {
	host := newFakeUIHost()
	cfg := defaultConfig()
	cfg.Strategy = "hash"
	require.NoError(t, host.SetSecret(t.Context(), "~:user", "pseudonymizer", "node", secretKeyHashHMAC, uiHexKey))
	for _, org := range []string{"org-a", "org-b"} {
		_, err := buildAnonymizeOptions(t.Context(), cfg, &proto.RequestContext{OrgId: org, SecretScopeId: "~:user", NodeId: "node"}, host)
		require.NoError(t, err)
	}
	for _, scope := range []string{"org-a", "~:other", ""} {
		_, err := buildAnonymizeOptions(t.Context(), cfg, &proto.RequestContext{OrgId: "~:user", SecretScopeId: scope, NodeId: "node"}, host)
		require.ErrorIs(t, err, errHashKeyMissing)
	}
}

// The strict verification only serves to refuse the request: with allow, it
// used to forward the whole request in clear, so it now observes.
func TestVerificationMode(t *testing.T) {
	for _, tt := range []struct {
		verification, strict bool
		onLeak               string
		want                 verificationLevel
	}{
		{false, false, "allow", verifyNone},
		{true, false, "allow", verifyObserve},
		{true, false, "block", verifyObserve},
		{false, true, "allow", verifyObserve},
		{true, true, "allow", verifyObserve},
		{false, true, "block", verifyBlock},
		{true, true, "block", verifyBlock},
	} {
		cfg := defaultConfig()
		cfg.Verification, cfg.VerificationStrict, cfg.VerificationOnLeak = tt.verification, tt.strict, tt.onLeak
		if got := verificationMode(cfg); got != tt.want {
			t.Errorf("verification=%v strict=%v on_leak=%s: %v, want %v", tt.verification, tt.strict, tt.onLeak, got, tt.want)
		}
	}
}
