package oidctoken

import (
	"context"
	"encoding/json"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn"
)

func verifiedClaims(ctx context.Context, raw, issuer, audience, jwksURL string, claims jwt.Claims, options ...jwt.ParserOption) error {
	if issuer == "" || audience == "" || jwksURL == "" {
		return errInvalidToken
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	keys, err := fetchJWKS(ctx, jwksURL)
	if err != nil {
		return err
	}
	opts := []jwt.ParserOption{jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(issuer), jwt.WithAudience(audience), jwt.WithIssuedAt()}
	opts = append(opts, options...)
	token, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		kid, ok := t.Header["kid"].(string)
		if !ok || kid == "" {
			return nil, errInvalidToken
		}
		for _, key := range keys.Keys {
			if key.Kid == kid && key.Kty == "RSA" && (key.Use == "" || key.Use == "sig") && (key.Alg == "" || key.Alg == "RS256") {
				return parseRSAPublicKey(key)
			}
		}
		return nil, errInvalidToken
	}, opts...)
	if err != nil || !token.Valid {
		return errInvalidToken
	}
	return nil
}
func VerifyIDToken(ctx context.Context, raw, issuer, audience, jwksURL string) (*authn.User, error) {
	var c claims
	if err := verifiedClaims(ctx, raw, issuer, audience, jwksURL, &c, jwt.WithExpirationRequired()); err != nil {
		return nil, err
	}
	if c.Subject == "" || c.IssuedAt == nil || len(c.Events) != 0 {
		return nil, errInvalidToken
	}
	return &authn.User{Issuer: issuer, Subject: c.Subject, Email: c.Email, EmailVerified: c.EmailVerified, DisplayName: c.Name, AuthenticatedAt: c.IssuedAt.Time}, nil
}

type LogoutClaims struct {
	jwt.RegisteredClaims
	AuthorizedParty string                     `json:"azp"`
	Events          map[string]json.RawMessage `json:"events"`
	Nonce           json.RawMessage            `json:"nonce"`
	SID             json.RawMessage            `json:"sid"`
}

// Subject-only OIDC back-channel logout profile: revoke all RP sessions for
// issuer+subject, across tenants. sid-only tokens are deliberately unsupported.
func VerifyLogoutToken(ctx context.Context, raw, issuer, audience, jwksURL string) (*LogoutClaims, error) {
	var c LogoutClaims
	if err := verifiedClaims(ctx, raw, issuer, audience, jwksURL, &c, jwt.WithExpirationRequired()); err != nil {
		return nil, err
	}
	if c.Subject == "" || c.ID == "" || len(c.ID) > 255 || c.IssuedAt == nil || len(c.Nonce) != 0 || len(c.Events) != 1 || len(c.Audience) != 1 || (c.AuthorizedParty != "" && c.AuthorizedParty != audience) {
		return nil, errInvalidToken
	}
	event, ok := c.Events["http://schemas.openid.net/event/backchannel-logout"]
	var object map[string]json.RawMessage
	if !ok || json.Unmarshal(event, &object) != nil || object == nil || len(object) != 0 {
		return nil, errInvalidToken
	}
	if c.ExpiresAt == nil || c.ExpiresAt.Sub(c.IssuedAt.Time) > 5*time.Minute {
		return nil, errInvalidToken
	}
	now := time.Now()
	if c.IssuedAt.Time.After(now) || c.IssuedAt.Time.Before(now.Add(-5*time.Minute)) {
		return nil, errInvalidToken
	}
	return &c, nil
}
