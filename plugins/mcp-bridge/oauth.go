package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/pkg/errors"
	"golang.org/x/oauth2"

	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
)

// Authentication modes of a node.
const (
	// AuthModeStatic sends a fixed header, the same for every user.
	AuthModeStatic = "static"
	// AuthModeOAuth makes each user authorize Xolo once on the MCP server's
	// authorization server; calls then carry the user's own token.
	AuthModeOAuth = "oauth"
)

// connectToolName is the tool offered while the user has not authorized
// the MCP server yet: calling it gives the link to do so.
const connectToolName = "mcp_bridge_connect"

// oauthSecretKey is the secret holding the token of a user for a node.
func oauthSecretKey(userID string) string {
	return "oauth:" + userID
}

// oauthToken is the authorization of a user on an MCP server, stored as a
// node secret. It is bound to the endpoint it was obtained for: a token is
// never sent to another server.
type oauthToken struct {
	Endpoint     string           `json:"endpoint"`
	TokenURL     string           `json:"token_url"`
	ClientID     string           `json:"client_id"`
	ClientSecret string           `json:"client_secret,omitempty"`
	AuthStyle    oauth2.AuthStyle `json:"auth_style"`
	AccessToken  string           `json:"access_token"`
	RefreshToken string           `json:"refresh_token,omitempty"`
	Expiry       time.Time        `json:"expiry"`
}

func (t *oauthToken) config() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     t.ClientID,
		ClientSecret: t.ClientSecret,
		Endpoint:     oauth2.Endpoint{TokenURL: t.TokenURL, AuthStyle: t.AuthStyle},
	}
}

// oauthClient holds the authorizations in progress and serializes the
// refreshes: a refresh token rotates, and replaying the former one makes
// the authorization server revoke the whole authorization.
type oauthClient struct {
	http *http.Client
	// host returns the host client and plugin name of a UI request.
	host func(ctx context.Context) (pluginsdk.HostClient, string)

	mu      sync.Mutex
	pending map[string]*pendingAuthorization
	locks   map[string]*sync.Mutex
}

func newOAuthClient() *oauthClient {
	return &oauthClient{
		http: &http.Client{Timeout: 30 * time.Second},
		host: func(ctx context.Context) (pluginsdk.HostClient, string) {
			return pluginsdk.HostClientFromContext(ctx), pluginsdk.PluginNameFromContext(ctx)
		},
		pending: map[string]*pendingAuthorization{},
		locks:   map[string]*sync.Mutex{},
	}
}

// pendingAuthorization is an authorization request waiting for the user to
// come back from the authorization server.
type pendingAuthorization struct {
	userID   string
	orgID    string
	nodeID   string
	verifier string
	token    oauthToken
	config   *oauth2.Config
	resource string
	created  time.Time
}

// pendingTTL bounds how long the user may take to authorize.
const pendingTTL = 15 * time.Minute

func (c *oauthClient) lock(key string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	l, ok := c.locks[key]
	if !ok {
		l = &sync.Mutex{}
		c.locks[key] = l
	}
	return l
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", errors.WithStack(err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// discover finds the authorization server of an MCP endpoint (RFC 9728 then
// RFC 8414).
func (c *oauthClient) discover(ctx context.Context, endpoint string) (*oauthex.AuthServerMeta, string, error) {
	metadataURL := ""

	// An unauthenticated request tells where the resource metadata lives.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(`{}`))
	if err != nil {
		return nil, "", errors.WithStack(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if res, err := c.http.Do(req); err == nil {
		io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
		res.Body.Close()
		if challenges, err := oauthex.ParseWWWAuthenticate(res.Header.Values("WWW-Authenticate")); err == nil {
			for _, ch := range challenges {
				if v := ch.Params["resource_metadata"]; v != "" {
					metadataURL = v
				}
			}
		}
	}
	if metadataURL == "" {
		u, err := url.Parse(endpoint)
		if err != nil {
			return nil, "", errors.WithStack(err)
		}
		metadataURL = u.Scheme + "://" + u.Host + "/.well-known/oauth-protected-resource" + strings.TrimSuffix(u.Path, "/")
	}

	prm, err := oauthex.GetProtectedResourceMetadata(ctx, metadataURL, endpoint, c.http)
	if err != nil {
		return nil, "", errors.Wrap(err, "could not read the protected resource metadata")
	}
	if len(prm.AuthorizationServers) == 0 {
		return nil, "", errors.New("the MCP server names no authorization server")
	}
	issuer := prm.AuthorizationServers[0]
	asm, err := oauthex.GetAuthServerMeta(ctx, strings.TrimSuffix(issuer, "/")+"/.well-known/oauth-authorization-server", issuer, c.http)
	if err != nil || asm == nil {
		return nil, "", errors.New("could not read the authorization server metadata")
	}
	return asm, prm.Resource, nil
}

// start registers Xolo as a client of the authorization server and returns
// the URL the user must open to authorize.
func (c *oauthClient) start(ctx context.Context, userID, orgID, nodeID, endpoint, redirectURI string) (string, error) {
	asm, resource, err := c.discover(ctx, endpoint)
	if err != nil {
		return "", err
	}
	if asm.RegistrationEndpoint == "" {
		return "", errors.New("the authorization server does not support dynamic client registration")
	}
	registered, err := oauthex.RegisterClient(ctx, asm.RegistrationEndpoint, &oauthex.ClientRegistrationMetadata{
		ClientName:              "Xolo",
		RedirectURIs:            []string{redirectURI},
		TokenEndpointAuthMethod: "none",
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
	}, c.http)
	if err != nil {
		return "", errors.Wrap(err, "could not register with the authorization server")
	}

	state, err := randomString(24)
	if err != nil {
		return "", err
	}
	verifier := oauth2.GenerateVerifier()
	config := &oauth2.Config{
		ClientID:     registered.ClientID,
		ClientSecret: registered.ClientSecret,
		RedirectURL:  redirectURI,
		Scopes:       asm.ScopesSupported,
		Endpoint:     oauth2.Endpoint{AuthURL: asm.AuthorizationEndpoint, TokenURL: asm.TokenEndpoint, AuthStyle: oauth2.AuthStyleInParams},
	}

	c.mu.Lock()
	for key, p := range c.pending {
		if time.Since(p.created) > pendingTTL {
			delete(c.pending, key)
		}
	}
	c.pending[state] = &pendingAuthorization{
		userID: userID, orgID: orgID, nodeID: nodeID, verifier: verifier, config: config, resource: resource, created: time.Now(),
		token: oauthToken{Endpoint: endpoint, TokenURL: asm.TokenEndpoint, ClientID: registered.ClientID, ClientSecret: registered.ClientSecret, AuthStyle: oauth2.AuthStyleInParams},
	}
	c.mu.Unlock()

	return config.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("resource", resource)), nil
}

// ErrUnknownAuthorization reports a callback matching no request of the
// user: expired, replayed or forged.
var ErrUnknownAuthorization = errors.New("unknown or expired authorization request")

// complete exchanges the code of a callback and stores the token of the
// user. The callback must come from the user who started the request.
func (c *oauthClient) complete(ctx context.Context, host pluginsdk.HostClient, pluginName, userID, state, code string) (*pendingAuthorization, error) {
	c.mu.Lock()
	pending, ok := c.pending[state]
	if ok {
		delete(c.pending, state)
	}
	c.mu.Unlock()
	if !ok || pending.userID != userID || time.Since(pending.created) > pendingTTL {
		return nil, errors.WithStack(ErrUnknownAuthorization)
	}

	token, err := pending.config.Exchange(ctx, code, oauth2.VerifierOption(pending.verifier), oauth2.SetAuthURLParam("resource", pending.resource))
	if err != nil {
		return nil, errors.Wrap(err, "could not exchange the authorization code")
	}
	stored := pending.token
	stored.AccessToken, stored.RefreshToken, stored.Expiry = token.AccessToken, token.RefreshToken, token.Expiry
	if err := saveToken(ctx, host, pluginName, pending.orgID, pending.nodeID, userID, &stored); err != nil {
		return nil, err
	}
	return pending, nil
}

func saveToken(ctx context.Context, host pluginsdk.HostClient, pluginName, orgID, nodeID, userID string, token *oauthToken) error {
	data, err := json.Marshal(token)
	if err != nil {
		return errors.WithStack(err)
	}
	return errors.WithStack(host.SetSecret(ctx, orgID, pluginName, nodeID, oauthSecretKey(userID), string(data)))
}

// ErrNotAuthorized reports a user who has not authorized the MCP server
// yet, or whose authorization was revoked.
var ErrNotAuthorized = errors.New("the user has not authorized this MCP server")

// accessToken returns a valid access token of the user for the endpoint,
// refreshing and storing it when it expired.
func (c *oauthClient) accessToken(ctx context.Context, host pluginsdk.HostClient, pluginName, orgID, nodeID, userID, endpoint string) (string, error) {
	if userID == "" {
		return "", errors.WithStack(ErrNotAuthorized)
	}
	l := c.lock(nodeID + ":" + userID)
	l.Lock()
	defer l.Unlock()

	raw, found, err := host.GetSecret(ctx, orgID, pluginName, nodeID, oauthSecretKey(userID))
	if err != nil {
		return "", errors.WithStack(err)
	}
	if !found {
		return "", errors.WithStack(ErrNotAuthorized)
	}
	var token oauthToken
	if err := json.Unmarshal([]byte(raw), &token); err != nil || token.Endpoint != endpoint {
		// Obtained for another server: never sent here.
		return "", errors.WithStack(ErrNotAuthorized)
	}

	current := &oauth2.Token{AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, Expiry: token.Expiry}
	if current.Valid() {
		return token.AccessToken, nil
	}
	if token.RefreshToken == "" {
		return "", errors.WithStack(ErrNotAuthorized)
	}

	refreshed, err := token.config().TokenSource(context.WithValue(ctx, oauth2.HTTPClient, c.http), current).Token()
	if err != nil {
		// Refused (revoked, expired): the user must authorize again.
		return "", errors.Wrap(ErrNotAuthorized, err.Error())
	}
	token.AccessToken, token.Expiry = refreshed.AccessToken, refreshed.Expiry
	if refreshed.RefreshToken != "" {
		token.RefreshToken = refreshed.RefreshToken
	}
	if err := saveToken(ctx, host, pluginName, orgID, nodeID, userID, &token); err != nil {
		return "", err
	}
	return token.AccessToken, nil
}

// connectURL is the page where a user authorizes the MCP server of a node.
func connectURL(cfg Config, nodeID string) string {
	return strings.TrimSuffix(cfg.PublicBaseURL, "/") + "/profile/plugins/mcp-bridge/ui/connect?" + url.Values{
		"nodeId":   {nodeID},
		"endpoint": {cfg.Endpoint},
	}.Encode()
}
