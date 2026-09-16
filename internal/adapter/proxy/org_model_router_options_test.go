package proxy

import (
	"testing"

	"github.com/bornholm/genai/llm/provider"
	"github.com/bornholm/genai/llm/provider/anthropic"
	"github.com/bornholm/genai/llm/provider/openai"
)

// The anthropic provider needs max_tokens on every request: the model's
// output window must reach its options, and providers without the field
// must be left untouched.
func TestWithDynamicChatCompletion_MaxTokens(t *testing.T) {
	t.Run("anthropic gets the output window", func(t *testing.T) {
		opts, err := provider.NewOptions(withDynamicChatCompletion("anthropic", "https://api.anthropic.com", "k", "claude-sonnet-5", 64000))
		if err != nil {
			t.Fatal(err)
		}
		specific, ok := opts.ChatCompletion.Specific.(*anthropic.Options)
		if !ok {
			t.Fatalf("unexpected options type %T", opts.ChatCompletion.Specific)
		}
		if specific.MaxTokens != 64000 || specific.Model != "claude-sonnet-5" || specific.APIKey != "k" {
			t.Errorf("options not populated: %+v", specific)
		}
	})

	t.Run("unset output window keeps the provider default", func(t *testing.T) {
		opts, err := provider.NewOptions(withDynamicChatCompletion("anthropic", "", "k", "m", 0))
		if err != nil {
			t.Fatal(err)
		}
		if got := opts.ChatCompletion.Specific.(*anthropic.Options).MaxTokens; got != anthropic.DefaultMaxTokens {
			t.Errorf("expected the provider default %d, got %d", anthropic.DefaultMaxTokens, got)
		}
	})

	t.Run("providers without the field are unaffected", func(t *testing.T) {
		opts, err := provider.NewOptions(withDynamicChatCompletion("openai", "https://api.openai.com/v1", "k", "gpt-4o", 64000))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := opts.ChatCompletion.Specific.(*openai.Options); !ok {
			t.Fatalf("unexpected options type %T", opts.ChatCompletion.Specific)
		}
	})
}

func TestEmbeddingsRegistry_AnthropicHasNone(t *testing.T) {
	if provider.NewEmbeddingsProviderOptions("anthropic") != nil {
		t.Fatal("the guard in clientForModel assumes anthropic registers no embeddings client")
	}
	if provider.NewEmbeddingsProviderOptions("openai") == nil {
		t.Fatal("openai must still register an embeddings client")
	}
}
