# Configuration

This page has not been translated yet. See the [French version](https://xolo-gateway.org/latest/) for the full content.

## XOLO_STORAGE_AUTO_MIGRATE

Apply migrations at startup (default: `true`). With `false`, schema compatibility is checked without migration. See the [UUID upgrade procedure](./uuid-migration.md); all old replicas must be stopped.

## OIDC sessions and back-channel logout

An interactive sign-in through an OAuth2/OIDC provider opens a session stored in the database; the cookie only carries its identifier. A copied cookie does not survive the logout, and a session stays valid across restarts and replicas as long as they all share `XOLO_HTTP_SESSION_KEYS`. A session expires after `XOLO_HTTP_SESSION_COOKIE_MAX_AGE` (default: `24h`), which must be positive or the server refuses to start. Every replica removes the expired entries every 10 minutes.

A sign-in must start at Xolo (`/auth/oidc/providers/{provider}`) and come back to the same browser within 15 minutes. A sign-in initiated by the identity provider, or a callback without the cookie set at the start, fails with `sign-in start missing` in the log: the user signs in again from Xolo.

An OIDC provider can revoke sessions through [OpenID Connect Back-Channel Logout](https://openid.net/specs/openid-connect-backchannel-1_0.html) at `https://{host}/auth/oidc/providers/{provider}/backchannel-logout`:

- only providers proving their issuer, with a client ID and a valid `jwks_uri`, accept it (named OIDC providers, Gitea with `DISCOVERY_URL`, Google); the others answer 404;
- the `logout_token` must be signed (RS256, RS384 or RS512) by the issuer, for the client ID, and carry `sub` and `jti`. Revocation is **by subject**: every session of that issuer and subject is closed, in every tenant, and a sign-in started before it is refused. A token carrying only `sid` is refused (400);
- a token is only accepted within 5 minutes of its issuance; a token already processed answers 200 without effect. Replica and provider clocks must agree within 5 minutes;
- in multi-tenant mode any active domain works. The route is not rate limited per IP: each request is authenticated by its signed token.

Durable logout only covers these interactive sessions. An OIDC ID token presented to the API (`oidctoken`) stays valid until `exp` plus `XOLO_HTTP_AUTHN_OIDCTOKEN_EXPIRY_LEEWAY`; an opaque access token (`oauth2token`) until its validation cache entry expires (`XOLO_HTTP_AUTHN_OAUTH2TOKEN_CACHE_TTL`, 60s); a `/auth/token/login` session for the lifetime of its cookie. A local logout does not end the session at the identity provider. See the [French version](https://xolo-gateway.org/latest/) for details.
