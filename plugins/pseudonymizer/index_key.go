package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"

	goanon "github.com/bornholm/go-anon"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// secretKeyIndex holds the node's index key, drawn on first use: the secret
// placeholder indexes are derived from (see indexKeyOption).
const secretKeyIndex = "index_key"

// eventIndexKeyUnavailable reports a request whose placeholders are numbered in
// order of appearance, the node's index key being unavailable.
const eventIndexKeyUnavailable = "index-key.unavailable"

// indexKeyStore is what indexKeyOption needs of the host's secret store.
type indexKeyStore interface {
	GetSecret(ctx context.Context, scopeID, pluginName, nodeID, key string) (string, bool, error)
	SetSecret(ctx context.Context, scopeID, pluginName, nodeID, key, value string) error
}

// indexKeys caches the nodes' index keys, by secret scope and node: a key, not
// personal data, read once per process instead of on every request.
type indexKeys struct {
	mu   sync.Mutex
	keys map[string]goanon.HashKey
}

// indexKeyOption derives the placeholder indexes of the tag and consistent
// strategies from the entities themselves rather than from their order of
// appearance (goanon.WithIndexKey).
//
// With the nonce fixed per org, user and node (see newSession), an index
// numbered in order of appearance names the first person of every request:
// a placeholder the model kept from an earlier turn, in a thinking block the
// plugin forwards untouched, was restored to whoever came first in the current
// request (#56). Derived from the entity, it names the same one from a request
// to the next, and one the current request does not hold is restored to
// nobody: it is neutralized like an invented one. Nothing about the entities
// outlives the request.
//
// The key is drawn at random for the node and kept in the host's secret store,
// so that every replica and every restart derives the same indexes, and the
// upstream prompt cache holds. Each sender gets a key of their own, derived from
// it: the same person mentioned by two users shows two different indexes, as
// two different nonces already did.
//
// Without a secret store, the indexes follow the order of appearance, as
// before: the error says why, for the caller to log.
func (k *indexKeys) indexKeyOption(ctx context.Context, cfg Config, reqCtx *proto.RequestContext, host indexKeyStore) (goanon.AnonymizeOption, error) {
	if cfg.Strategy != "tag" && cfg.Strategy != "consistent" {
		return nil, nil
	}
	if host == nil {
		return nil, fmt.Errorf("no secret store")
	}
	nodeKey, err := k.nodeKey(ctx, reqCtx.GetSecretScopeId(), reqCtx.GetNodeId(), host)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, nodeKey)
	mac.Write([]byte(reqCtx.GetOrgId()))
	mac.Write([]byte{0x00})
	mac.Write([]byte(reqCtx.GetUserId()))
	return goanon.WithIndexKey(mac.Sum(nil)), nil
}

// nodeKey returns the node's index key, drawing and storing one the first
// time. Two replicas drawing one at once both store theirs; each reads back the
// one that stayed, so they agree from then on.
func (k *indexKeys) nodeKey(ctx context.Context, scopeID, nodeID string, host indexKeyStore) (goanon.HashKey, error) {
	cacheKey := scopeID + "\x00" + nodeID
	k.mu.Lock()
	defer k.mu.Unlock()
	if key, ok := k.keys[cacheKey]; ok {
		return key, nil
	}

	raw, found, err := host.GetSecret(ctx, scopeID, "pseudonymizer", nodeID, secretKeyIndex)
	if err != nil {
		return nil, fmt.Errorf("reading the index key: %w", err)
	}
	if !found || raw == "" {
		drawn := make([]byte, 32)
		if _, err := rand.Read(drawn); err != nil {
			return nil, fmt.Errorf("drawing the index key: %w", err)
		}
		if err := host.SetSecret(ctx, scopeID, "pseudonymizer", nodeID, secretKeyIndex, hex.EncodeToString(drawn)); err != nil {
			return nil, fmt.Errorf("storing the index key: %w", err)
		}
		if raw, found, err = host.GetSecret(ctx, scopeID, "pseudonymizer", nodeID, secretKeyIndex); err != nil || !found {
			return nil, fmt.Errorf("reading back the index key: found=%v: %v", found, err)
		}
	}
	key, err := goanon.ParseHashKey(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid index key: %w", err)
	}

	if k.keys == nil {
		k.keys = map[string]goanon.HashKey{}
	}
	k.keys[cacheKey] = key
	return key, nil
}
