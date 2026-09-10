package main

import (
	"context"
	"errors"
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
