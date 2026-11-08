package oidctoken

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// backchannelLogoutEvent is the event a logout token must carry (OIDC
// Back-Channel Logout 1.0, section 2.4).
const backchannelLogoutEvent = "http://schemas.openid.net/event/backchannel-logout"

// jwksFetchTimeout bounds the retrieval of the keys of a provider.
const jwksFetchTimeout = 10 * time.Second

// ErrKeysUnavailable reports that the key set of the provider could not be
// retrieved: unlike an invalid token, the provider may retry the request.
var ErrKeysUnavailable = errors.New("provider keys unavailable")

// LogoutClaims are the claims of a verified logout token.
type LogoutClaims struct {
	jwt.RegisteredClaims
	AuthorizedParty string                     `json:"azp"`
	Events          map[string]json.RawMessage `json:"events"`
	Nonce           json.RawMessage            `json:"nonce"`
	// SessionID is decoded but ignored on purpose: Xolo revokes by subject,
	// every session of the identity, whatever the sid names.
	SessionID string `json:"sid"`
}

// VerifyLogoutToken verifies a logout token signed by the provider issuer for
// the client audience. Xolo revokes by subject: a token without sub, carrying
// only a sid, is refused. The token must be recent (see
// model.LogoutTokenMaxAge), which bounds how long its jti must be remembered
// to refuse a replay. It returns ErrKeysUnavailable when the key set cannot be
// retrieved, and an invalid token error otherwise.
func VerifyLogoutToken(ctx context.Context, raw, issuer, audience, jwksURL string) (*LogoutClaims, error) {
	if issuer == "" || audience == "" || jwksURL == "" {
		return nil, errInvalidToken
	}
	jwks, err := fetchJWKS(ctx, jwksURL)
	if err != nil {
		return nil, err
	}

	findSigningKey := func(jwks JWKS, kid string) (JWK, bool) {
		for _, key := range jwks.Keys {
			if key.Kid == kid && key.Kty == "RSA" && (key.Use == "" || key.Use == "sig") {
				return key, true
			}
		}
		return JWK{}, false
	}

	// keysUnavailable is set when the token names a key the cached set does
	// not hold and the provider cannot serve a newer one: it may have rotated
	// its keys during an outage, and the logout must be retried, not refused.
	var keysUnavailable error
	var claims LogoutClaims
	_, err = jwt.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) {
		kid, ok := t.Header["kid"].(string)
		if !ok || kid == "" {
			return nil, errInvalidToken
		}
		key, found := findSigningKey(jwks, kid)
		// The logout endpoint is reached without authentication: a token of
		// another issuer never makes the provider be called. The issuer is
		// read before the signature is checked, as in validateToken, so a
		// forged token claiming it still can, once per jwksRefetchInterval.
		if iss, err := t.Claims.GetIssuer(); !found && err == nil && iss == issuer {
			refreshed, ok, err := refetchJWKS(ctx, jwksURL)
			switch {
			case err != nil:
				keysUnavailable = fmt.Errorf("%w: %w", ErrKeysUnavailable, err)
				return nil, keysUnavailable
			case ok:
				// The provider has just served its keys: a key it does not
				// hold does not exist, and the token is refused for good.
				// Deferring it would let a forged key ID hold a logout
				// forever. A logout is deferred only while the keys cannot
				// be downloaded.
				key, found = findSigningKey(refreshed, kid)
			default:
				// No refetch allowed yet: the cached set, fresh or not,
				// cannot tell the key does not exist.
				keysUnavailable = errors.Wrapf(ErrKeysUnavailable, "%s: key %q unknown and no refetch allowed yet", jwksURL, kid)
				return nil, keysUnavailable
			}
		}
		if !found {
			return nil, errInvalidToken
		}
		return parseRSAPublicKey(key)
	},
		jwt.WithValidMethods([]string{"RS256", "RS384", "RS512"}),
		jwt.WithIssuer(issuer),
		jwt.WithAudience(audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(model.LogoutClockSkew),
	)
	if keysUnavailable != nil {
		return nil, keysUnavailable
	}
	if err != nil {
		return nil, errInvalidToken
	}

	if claims.Subject == "" || claims.ID == "" || len(claims.ID) > 255 || len(claims.Nonce) != 0 {
		return nil, errInvalidToken
	}
	if claims.AuthorizedParty != "" && claims.AuthorizedParty != audience {
		return nil, errInvalidToken
	}
	if len(claims.Events) != 1 {
		return nil, errInvalidToken
	}
	var event map[string]json.RawMessage
	if data, ok := claims.Events[backchannelLogoutEvent]; !ok || json.Unmarshal(data, &event) != nil || event == nil {
		return nil, errInvalidToken
	}

	if claims.IssuedAt == nil || claims.ExpiresAt == nil {
		return nil, errInvalidToken
	}
	issuedAt, expiresAt := claims.IssuedAt.Time, claims.ExpiresAt.Time
	now := time.Now()
	if expiresAt.Sub(issuedAt) > model.LogoutTokenMaxAge || issuedAt.Before(now.Add(-model.LogoutTokenMaxAge)) {
		return nil, errInvalidToken
	}
	return &claims, nil
}
