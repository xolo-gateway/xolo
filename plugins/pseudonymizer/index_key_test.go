package main

import (
	"context"
	"testing"

	"github.com/bornholm/go-anon/pkg/anonymizer"
	"github.com/bornholm/go-anon/pkg/ner"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// indexKeyHost is a secret store counting its writes.
type indexKeyHost struct {
	secrets map[string]string
	sets    int
}

func (h *indexKeyHost) GetSecret(_ context.Context, scopeID, pluginName, nodeID, key string) (string, bool, error) {
	v, ok := h.secrets[scopeID+"/"+pluginName+"/"+nodeID+"/"+key]
	return v, ok, nil
}

func (h *indexKeyHost) SetSecret(_ context.Context, scopeID, pluginName, nodeID, key, value string) error {
	if h.secrets == nil {
		h.secrets = map[string]string{}
	}
	h.secrets[scopeID+"/"+pluginName+"/"+nodeID+"/"+key] = value
	h.sets++
	return nil
}

// placeholderWith anonymizes "Jean Dupont" with the index key option the
// request context gets, and returns his placeholder.
func placeholderWith(t *testing.T, keys *indexKeys, host indexKeyStore, reqCtx *proto.RequestContext) string {
	t.Helper()
	cfg := defaultConfig()
	cfg.Strategy = "tag"
	opt, err := keys.indexKeyOption(context.Background(), cfg, reqCtx, host)
	if err != nil || opt == nil {
		t.Fatalf("indexKeyOption = %v, %v", opt, err)
	}
	res, err := anonymizer.New(jeanDupontRecognizer{}, anonymizer.Config{Strategy: anonymizer.TagReplace}).Anonymize("Jean Dupont", anonymizer.WithSession(newSession(context.Background(), reqCtx)), opt)
	if err != nil {
		t.Fatal(err)
	}
	return res.OriginalToPlaceholder["Jean Dupont"]
}

// jeanDupontRecognizer finds Jean Dupont, the whole text.
type jeanDupontRecognizer struct{}

func (jeanDupontRecognizer) Recognize(text string) ([]ner.Entity, error) {
	return []ner.Entity{{Text: text, Type: ner.TypePER, Start: 0, End: len(text), Confidence: 1}}, nil
}

func TestIndexKey_DrawnOnceAndKeptForTheNode(t *testing.T) {
	host := &indexKeyHost{}
	reqCtx := &proto.RequestContext{OrgId: "org", UserId: "alice", SecretScopeId: "org", NodeId: "node"}

	first := placeholderWith(t, &indexKeys{}, host, reqCtx)
	// Another process, the same store: a restart or another replica.
	second := placeholderWith(t, &indexKeys{}, host, reqCtx)

	if first != second {
		t.Errorf("placeholders %q and %q, want the same from a process to the next", first, second)
	}
	if host.sets != 1 {
		t.Errorf("index key stored %d times, want once", host.sets)
	}
}

func TestIndexKey_EachSenderGetsTheirOwnIndexes(t *testing.T) {
	host := &indexKeyHost{}
	keys := &indexKeys{}

	alice := placeholderWith(t, keys, host, &proto.RequestContext{OrgId: "org", UserId: "alice", SecretScopeId: "org", NodeId: "node"})
	bob := placeholderWith(t, keys, host, &proto.RequestContext{OrgId: "org", UserId: "bob", SecretScopeId: "org", NodeId: "node"})

	if alice == bob || trimNonce(alice) == trimNonce(bob) {
		t.Errorf("alice %q, bob %q: want different indexes for the same person", alice, bob)
	}
}

// trimNonce drops the nonce of a placeholder, leaving its label and index.
func trimNonce(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '_' {
			return p[:i]
		}
	}
	return p
}

func TestIndexKey_OnlyForTheIndexedStrategies(t *testing.T) {
	for _, strategy := range []string{"redact", "hash"} {
		cfg := defaultConfig()
		cfg.Strategy = strategy
		opt, err := (&indexKeys{}).indexKeyOption(context.Background(), cfg, &proto.RequestContext{NodeId: "node"}, &indexKeyHost{})
		if opt != nil || err != nil {
			t.Errorf("%s: option %v, err %v, want neither", strategy, opt, err)
		}
	}
}

func TestIndexKey_WithoutSecretStore(t *testing.T) {
	cfg := defaultConfig()
	cfg.Strategy = "tag"
	if _, err := (&indexKeys{}).indexKeyOption(context.Background(), cfg, &proto.RequestContext{NodeId: "node"}, nil); err == nil {
		t.Error("want an error without a secret store")
	}
}
