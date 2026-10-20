package proxy

// No genai provider package is imported here on purpose: the test binary
// shares one registry, and an import in a test file would hide a missing
// blank import in org_model_router.go (see provider_registry_test.go).

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/bornholm/genai/llm/provider"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// maxTokensOf reads the MaxTokens field of provider options by reflection,
// the way withDynamicChatCompletion sets it.
func maxTokensOf(t *testing.T, specific any) (int64, bool) {
	t.Helper()
	field := reflect.ValueOf(specific).Elem().FieldByName("MaxTokens")
	if !field.IsValid() {
		return 0, false
	}
	return field.Int(), true
}

// The anthropic provider needs max_tokens on every request: the model's
// output window must reach its options, and providers without the field
// must be left untouched.
func TestWithDynamicChatCompletion_MaxTokens(t *testing.T) {
	t.Run("anthropic gets the output window", func(t *testing.T) {
		opts, err := provider.NewOptions(withDynamicChatCompletion("anthropic", "https://api.anthropic.com", "k", "claude-sonnet-5", 64000, nil))
		if err != nil {
			t.Fatal(err)
		}
		if typ := fmt.Sprintf("%T", opts.ChatCompletion.Specific); typ != "*anthropic.Options" {
			t.Fatalf("unexpected options type %s", typ)
		}
		if got, ok := maxTokensOf(t, opts.ChatCompletion.Specific); !ok || got != 64000 {
			t.Errorf("MaxTokens not populated: %v", opts.ChatCompletion.Specific)
		}
		common := reflect.ValueOf(opts.ChatCompletion.Specific).Elem().FieldByName("CommonOptions")
		if common.FieldByName("Model").String() != "claude-sonnet-5" || common.FieldByName("APIKey").String() != "k" {
			t.Errorf("common options not populated: %+v", opts.ChatCompletion.Specific)
		}
	})

	t.Run("unset output window keeps the provider default", func(t *testing.T) {
		opts, err := provider.NewOptions(withDynamicChatCompletion("anthropic", "", "k", "m", 0, nil))
		if err != nil {
			t.Fatal(err)
		}
		// 4096 is anthropic.DefaultMaxTokens, not imported here on purpose.
		if got, ok := maxTokensOf(t, opts.ChatCompletion.Specific); !ok || got != 4096 {
			t.Errorf("expected the provider default 4096, got %d", got)
		}
	})

	t.Run("other providers are unaffected", func(t *testing.T) {
		for _, name := range []provider.Name{"openai", "mistral", "openrouter"} {
			opts, err := provider.NewOptions(withDynamicChatCompletion(name, "https://example.invalid/v1", "k", "m", 64000, nil))
			if err != nil {
				t.Fatal(err)
			}
			if typ := fmt.Sprintf("%T", opts.ChatCompletion.Specific); typ != "*"+string(name)+".Options" {
				t.Fatalf("%s: unexpected options type %s", name, typ)
			}
			if got, has := maxTokensOf(t, opts.ChatCompletion.Specific); has && got != 0 {
				t.Errorf("%s: MaxTokens must only be set on allowlisted providers, got %d", name, got)
			}
		}
	})
}

// A gateway such as OpenCode Go refuses requests without the headers its
// provider configures: they must reach the genai options of both clients,
// as a copy the stored provider does not share.
func TestDynamicOptions_CarryTheProviderHeaders(t *testing.T) {
	headers := &model.ProviderRequestHeaders{
		Headers:       map[string]string{"User-Agent": "xolo/test"},
		SessionHeader: "x-opencode-session",
	}
	opts, err := provider.NewOptions(
		withDynamicChatCompletion("openai", "https://example.invalid/v1", "k", "m", 0, headers),
		withDynamicEmbeddings("openai", "https://example.invalid/v1", "k", "m", headers),
	)
	if err != nil {
		t.Fatal(err)
	}
	for name, specific := range map[string]any{"chat": opts.ChatCompletion.Specific, "embeddings": opts.Embeddings.Specific} {
		common := reflect.ValueOf(specific).Elem().FieldByName("CommonOptions").Interface().(provider.CommonOptions)
		if common.Headers["User-Agent"] != "xolo/test" || common.SessionHeader != "x-opencode-session" || common.APIKey != "k" {
			t.Errorf("%s: common options not populated: %+v", name, common)
		}
		common.Headers["User-Agent"] = "changed"
		if headers.Headers["User-Agent"] != "xolo/test" {
			t.Fatalf("%s: the options share the provider's header map", name)
		}
	}
}

func TestEmbeddingsRegistry_AnthropicHasNone(t *testing.T) {
	if provider.NewEmbeddingsProviderOptions("anthropic") != nil {
		t.Fatal("the guard in clientForModel assumes anthropic registers no embeddings client")
	}
	if provider.NewEmbeddingsProviderOptions("openai") == nil {
		t.Fatal("openai must still register an embeddings client")
	}
}

func TestDefaultMaxTokensFor(t *testing.T) {
	cases := map[int64]int64{
		0:      0,
		8_192:  8_192,
		16_384: 16_384,
		64_000: maxDefaultMaxTokens,
	}
	for window, want := range cases {
		got := defaultMaxTokensFor(outputWindowModel{LLMModel: model.NewLLMModel("", "", "", "", "", 0, 0), window: window})
		if got != want {
			t.Errorf("output window %d: got %d, want %d", window, got, want)
		}
	}
}

// outputWindowModel overrides the output window of a base model.
type outputWindowModel struct {
	model.LLMModel
	window int64
}

func (m outputWindowModel) OutputWindow() int64 { return m.window }
