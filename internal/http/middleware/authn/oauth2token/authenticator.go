package oauth2token

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn"
)

var errInvalidToken = errors.New("invalid token")

// Options configures a Handler.
type Options struct {
	// CacheTTL bounds how long a positive introspection result is cached.
	CacheTTL time.Duration
	// CacheSize is the maximum number of cached results.
	CacheSize int
	// HTTPClient is used for introspection requests (defaults to http.DefaultClient).
	HTTPClient *http.Client
}

type OptionFunc func(*Options)

func WithCacheTTL(d time.Duration) OptionFunc {
	return func(o *Options) { o.CacheTTL = d }
}

func WithCacheSize(n int) OptionFunc {
	return func(o *Options) { o.CacheSize = n }
}

func WithHTTPClient(c *http.Client) OptionFunc {
	return func(o *Options) { o.HTTPClient = c }
}

// cachedIdentity is a previously-resolved identity, kept until ExpiresAt.
type cachedIdentity struct {
	user      *authn.User
	expiresAt time.Time
}

// Handler authenticates API requests by validating the bearer access token
// against an OAuth2 provider's RFC 7662 introspection endpoint.
type Handler struct {
	providers []Provider
	options   Options
	client    *http.Client
	cache     *expirable.LRU[string, cachedIdentity]
}

// introspectionResponse is the subset of RFC 7662 fields we consume.
type introspectionResponse struct {
	Active            bool            `json:"active"`
	Subject           string          `json:"sub"`
	Username          string          `json:"username"`
	PreferredUsername string          `json:"preferred_username"`
	Name              string          `json:"name"`
	EmailVerified     bool            `json:"email_verified"`
	Email             string          `json:"email"`
	Scope             string          `json:"scope"`
	Audience          json.RawMessage `json:"aud"`
	Expiry            int64           `json:"exp"`
}

func NewHandler(providers []Provider, funcs ...OptionFunc) *Handler {
	opts := Options{
		CacheTTL:   60 * time.Second,
		CacheSize:  1024,
		HTTPClient: http.DefaultClient,
	}
	for _, f := range funcs {
		f(&opts)
	}
	if opts.CacheSize <= 0 {
		opts.CacheSize = 1024
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = http.DefaultClient
	}

	return &Handler{
		providers: providers,
		options:   opts,
		client:    opts.HTTPClient,
		// The expirable LRU evicts on its own TTL; we also store a per-entry
		// expiry so short-lived tokens are dropped before the LRU TTL elapses.
		cache: expirable.NewLRU[string, cachedIdentity](opts.CacheSize, nil, opts.CacheTTL),
	}
}

func (h *Handler) Authenticate(w http.ResponseWriter, r *http.Request) (*authn.User, error) {
	token := bearerToken(r)
	if token == "" {
		return nil, nil
	}

	key := cacheKey(token)
	if entry, ok := h.cache.Get(key); ok {
		if time.Now().Before(entry.expiresAt) {
			return entry.user, nil
		}
		h.cache.Remove(key)
	}

	ctx := r.Context()
	for _, provider := range h.providers {
		// Prefer introspection (validates scope/audience/active state); fall back
		// to userinfo for providers without an introspection endpoint (e.g. Auth0).
		var (
			user      *authn.User
			expiresAt time.Time
			err       error
		)
		switch {
		case provider.IntrospectionURL != "":
			user, expiresAt, err = h.introspect(ctx, token, provider)
		case provider.UserInfoURL != "":
			user, expiresAt, err = h.resolveViaUserInfo(ctx, token, provider)
		default:
			continue
		}

		if err != nil {
			if errors.Is(err, errInvalidToken) {
				continue
			}
			// Transport / endpoint failure: don't fail the whole chain, let
			// other authenticators (or the 401 fallback) handle it.
			slog.WarnContext(ctx, "oauth2token: token validation failed",
				slog.String("provider", provider.ID),
				slog.Any("error", err))
			continue
		}

		h.cache.Add(key, cachedIdentity{user: user, expiresAt: expiresAt})
		return user, nil
	}

	return nil, nil
}

// resolveViaUserInfo validates an opaque access token by calling the OIDC
// UserInfo endpoint with it: a 200 response carrying a subject means the token
// is valid. Used for providers without an introspection endpoint. Note that
// userinfo exposes neither scope nor audience, so RequiredScope/RequiredAudience
// cannot be enforced on this path.
func (h *Handler) resolveViaUserInfo(ctx context.Context, token string, provider Provider) (*authn.User, time.Time, error) {
	info, err := h.fetchUserInfo(ctx, token, provider.UserInfoURL)
	if err != nil {
		return nil, time.Time{}, err
	}
	if info.Subject == "" {
		return nil, time.Time{}, errInvalidToken
	}

	user := &authn.User{
		Provider:      provider.ID,
		Issuer:        provider.Issuer,
		Subject:       info.Subject,
		Email:         info.Email,
		EmailVerified: info.EmailVerified,
	}
	switch {
	case info.PreferredUsername != "":
		user.DisplayName = info.PreferredUsername
	case info.Name != "":
		user.DisplayName = info.Name
	}

	// UserInfo carries no expiry; rely on the configured cache TTL.
	return user, time.Now().Add(h.options.CacheTTL), nil
}

func (h *Handler) introspect(ctx context.Context, token string, provider Provider) (*authn.User, time.Time, error) {
	if provider.IntrospectionURL == "" {
		return nil, time.Time{}, errInvalidToken
	}

	form := url.Values{}
	form.Set("token", token)
	form.Set("token_type_hint", "access_token")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.IntrospectionURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, time.Time{}, errors.WithStack(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if provider.ClientID != "" {
		req.SetBasicAuth(provider.ClientID, provider.ClientSecret)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, time.Time{}, errors.WithStack(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, time.Time{}, errors.Errorf("introspection failed with status %d", resp.StatusCode)
	}

	var out introspectionResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, time.Time{}, errors.WithStack(err)
	}

	if !out.Active {
		return nil, time.Time{}, errInvalidToken
	}

	if provider.RequiredScope != "" && !slices.Contains(strings.Fields(out.Scope), provider.RequiredScope) {
		slog.DebugContext(ctx, "oauth2token: token missing required scope",
			slog.String("provider", provider.ID),
			slog.String("requiredScope", provider.RequiredScope))
		return nil, time.Time{}, errInvalidToken
	}

	if provider.RequiredAudience != "" && !audienceContains(out.Audience, provider.RequiredAudience) {
		slog.DebugContext(ctx, "oauth2token: token missing required audience",
			slog.String("provider", provider.ID),
			slog.String("requiredAudience", provider.RequiredAudience))
		return nil, time.Time{}, errInvalidToken
	}

	subject := out.Subject
	if subject == "" {
		subject = out.Username
	}
	if subject == "" {
		return nil, time.Time{}, errInvalidToken
	}

	user := &authn.User{
		Provider:      provider.ID,
		Issuer:        provider.Issuer,
		Subject:       subject,
		Email:         out.Email,
		EmailVerified: out.EmailVerified,
	}
	switch {
	case out.PreferredUsername != "":
		user.DisplayName = out.PreferredUsername
	case out.Username != "":
		user.DisplayName = out.Username
	case out.Name != "":
		user.DisplayName = out.Name
	}

	// Introspection may omit identity attributes (Gitea omits email); enrich
	// from the UserInfo endpoint using the access token itself.
	if provider.UserInfoURL != "" && (user.Email == "" || user.DisplayName == "") {
		h.enrichFromUserInfo(ctx, token, provider, user)
	}

	// Cap the cache entry lifetime by the token's own expiry.
	expiresAt := time.Now().Add(h.options.CacheTTL)
	if out.Expiry > 0 {
		if tokenExp := time.Unix(out.Expiry, 0); tokenExp.Before(expiresAt) {
			expiresAt = tokenExp
		}
	}

	return user, expiresAt, nil
}

// userInfoResponse is the subset of OIDC UserInfo fields we consume.
type userInfoResponse struct {
	Subject           string `json:"sub"`
	EmailVerified     bool   `json:"email_verified"`
	Email             string `json:"email"`
	PreferredUsername string `json:"preferred_username"`
	Name              string `json:"name"`
}

// fetchUserInfo calls the OIDC UserInfo endpoint with the access token as a
// bearer credential and decodes the response.
func (h *Handler) fetchUserInfo(ctx context.Context, token, userInfoURL string) (*userInfoResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userInfoURL, nil)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, errInvalidToken
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errors.Errorf("userinfo failed with status %d", resp.StatusCode)
	}

	var info userInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, errors.WithStack(err)
	}
	return &info, nil
}

// enrichFromUserInfo fills the user's missing email/display name from the OIDC
// UserInfo endpoint. Failures are non-fatal: the token is already validated by
// introspection, so we keep whatever identity we have.
func (h *Handler) enrichFromUserInfo(ctx context.Context, token string, provider Provider, user *authn.User) {
	info, err := h.fetchUserInfo(ctx, token, provider.UserInfoURL)
	if err != nil {
		slog.WarnContext(ctx, "oauth2token: userinfo enrichment failed", slog.Any("error", err))
		return
	}

	// Guard against a UserInfo response for a different subject.
	if info.Subject != "" && user.Subject != "" && info.Subject != user.Subject {
		slog.WarnContext(ctx, "oauth2token: userinfo subject mismatch, ignoring",
			slog.String("introspectionSub", user.Subject),
			slog.String("userinfoSub", info.Subject))
		return
	}

	if user.Email == "" {
		user.Email = info.Email
		user.EmailVerified = info.EmailVerified
	}
	if user.DisplayName == "" {
		switch {
		case info.PreferredUsername != "":
			user.DisplayName = info.PreferredUsername
		case info.Name != "":
			user.DisplayName = info.Name
		}
	}
}

// bearerToken extracts the token from the Authorization header.
func bearerToken(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if len(authHeader) < len("Bearer ") || !strings.EqualFold(authHeader[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(authHeader[7:])
}

func cacheKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// audienceContains reports whether the RFC 7662 `aud` value (either a string or
// an array of strings) contains want.
func audienceContains(raw json.RawMessage, want string) bool {
	if len(raw) == 0 {
		return false
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return single == want
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		return slices.Contains(many, want)
	}
	return false
}

var _ authn.Authenticator = &Handler{}
