package setup

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn/oauth2token"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn/oidctoken"
)

const chainTestIssuer = "https://id.chain.example.test/"

// stubAuthenticator resolves the bearer tokens of its map, and fails with err
// when it is set.
type stubAuthenticator struct {
	tokens map[string]*authn.User
	err    error
}

func (s stubAuthenticator) Authenticate(_ http.ResponseWriter, r *http.Request) (*authn.User, error) {
	if s.err != nil {
		return nil, s.err
	}
	token := r.Header.Get("Authorization")
	if len(token) > len("Bearer ") {
		return s.tokens[token[len("Bearer "):]], nil
	}
	return nil, nil
}

// chainTestProvider is an OIDC provider serving its key set and an
// introspection endpoint, counting the calls to each.
type chainTestProvider struct {
	key           *rsa.PrivateKey
	down          atomic.Bool
	jwksCalls     atomic.Int32
	introspection atomic.Int32
	jwksURL       string
	introspectURL string
}

func newChainTestProvider(t *testing.T) *chainTestProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &chainTestProvider{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		p.jwksCalls.Add(1)
		if p.down.Load() {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(oidctoken.JWKS{Keys: []oidctoken.JWK{{
			Kty: "RSA", Kid: "k1", Use: "sig", Alg: "RS256",
			N: base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/introspect", func(w http.ResponseWriter, r *http.Request) {
		p.introspection.Add(1)
		http.Error(w, "not found", http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p.jwksURL, p.introspectURL = srv.URL+"/jwks", srv.URL+"/introspect"
	return p
}

func (p *chainTestProvider) idToken(t *testing.T) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"iss": chainTestIssuer, "sub": "alice", "exp": time.Now().Add(time.Hour).Unix()})
	token.Header["kid"] = "k1"
	signed, err := token.SignedString(p.key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func (p *chainTestProvider) serve(t *testing.T, apiKey authn.Authenticator, token string) (int, *authn.User) {
	t.Helper()
	chain := apiAuthenticatorChain(
		apiKey,
		oidctoken.NewHandler([]oidctoken.Provider{{ID: "idp", Issuer: chainTestIssuer, JWKSURL: p.jwksURL}}),
		oauth2token.NewHandler([]oauth2token.Provider{{ID: "idp", IntrospectionURL: p.introspectURL}}),
		stubAuthenticator{},
	)
	var user *authn.User
	h := authn.Middleware(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}, chain...)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user = authn.ContextUser(r.Context())
	}))
	r := httptest.NewRequest(http.MethodGet, "/api/v1/models", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, user
}

// An API key is resolved without calling the OIDC provider, even while it is
// down.
func TestAPIAuthenticatorChain_APIKeyNeverCallsTheProvider(t *testing.T) {
	p := newChainTestProvider(t)
	p.down.Store(true)
	apiKey := stubAuthenticator{tokens: map[string]*authn.User{"xolo-api-key": {Provider: "token", Subject: "bob"}}}

	code, user := p.serve(t, apiKey, "xolo-api-key")
	if code != http.StatusOK || user == nil || user.Subject != "bob" {
		t.Fatalf("expected the API key accepted, got %d %v", code, user)
	}
	if got := p.jwksCalls.Load() + p.introspection.Load(); got != 0 {
		t.Fatalf("provider called %d times, want 0", got)
	}
}

// An ID token is verified before the API key lookup, so a failing database
// does not turn it into a 500.
func TestAPIAuthenticatorChain_IDTokenDoesNotNeedTheDatabase(t *testing.T) {
	p := newChainTestProvider(t)
	apiKey := stubAuthenticator{err: errors.New("database is down")}

	code, user := p.serve(t, apiKey, p.idToken(t))
	if code != http.StatusOK || user == nil || user.Subject != "alice" {
		t.Fatalf("expected the ID token accepted, got %d %v", code, user)
	}
}
